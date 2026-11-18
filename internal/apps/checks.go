// checks.go holds reusable preflights (run before installing) and
// functional checks (run after), shared by several catalog apps.

package apps

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// smokeImage is a small image with a shell for functional checks.
const smokeImage = "busybox:1.37"

// storageSmokeTest proves a StorageClass works end to end: a PVC binds, a pod
// mounts it, writes and reads a file, and both are removed again. The file is
// written through a subPath mount and read back from the volume root, as many
// charts (Prometheus, Alertmanager) mount a subPath: on Talos a hostPath that
// kubelet cannot see resolves subPaths inside kubelet's own container, so the
// pod silently writes somewhere other than the volume.
func (e *Env) storageSmokeTest(ctx context.Context, ns, storageClass, accessMode string) error {
	name := "k8slocalcli-smoke-" + storageClass
	manifest := fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  storageClassName: %[3]s
  accessModes: [%[4]s]
  resources:
    requests:
      storage: 64Mi
---
apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  restartPolicy: Never
  containers:
    - name: smoke
      image: %[5]s
      command: ["sh", "-c", "echo ok > /sub/smoke && cat /data/sub/smoke"]
      volumeMounts:
        - name: data
          mountPath: /data
        - name: data
          mountPath: /sub
          subPath: sub
      securityContext:
        allowPrivilegeEscalation: false
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: %[1]s
`, name, ns, storageClass, accessMode, smokeImage)
	defer func() {
		// Own short context: ctx may be the one that just timed out.
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = e.kubectlQuiet(cctx, "delete", "pod,pvc", name, "-n", ns, "--ignore-not-found", "--wait=false")
	}()
	if err := e.applyYAML(ctx, manifest); err != nil {
		return fmt.Errorf("creating storage smoke test: %w", err)
	}
	if err := e.kubectl(ctx, "wait", "--for=jsonpath={.status.phase}=Succeeded", "pod/"+name,
		"-n", ns, "--timeout="+remaining(ctx, 3*time.Minute).String()); err != nil {
		return fmt.Errorf("a pod could not write to a %s volume of StorageClass %s: %w", accessMode, storageClass, err)
	}
	return nil
}

// smokeObject is a throwaway resource a functional check creates, waits on
// and deletes again (a test VM, a test import).
type smokeObject struct {
	kind, namespace, name string // kind as kubectl names it, e.g. "vmi"
	manifest              string
	// waitFor is the kubectl wait --for condition that means it works.
	waitFor string
	// what starts the error when it does not, e.g. "the test VM did not start".
	what string
}

// smokeTest applies obj (retrying while a just-started admission webhook
// rejects it), waits for obj.waitFor and deletes it again. Custom resources
// stuck short of ready usually say why only in an event, so a failure quotes
// the object's newest Warning.
func (e *Env) smokeTest(ctx context.Context, obj smokeObject) error {
	defer func() {
		// Own short deadline: ctx may be the one that just timed out.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = e.kubectlQuiet(cctx, "delete", obj.kind, obj.name, "-n", obj.namespace, "--ignore-not-found", "--wait=false")
	}()
	if err := e.applyYAMLRetry(ctx, obj.manifest, remaining(ctx, time.Minute)); err != nil {
		return fmt.Errorf("creating %s %s/%s: %w", obj.kind, obj.namespace, obj.name, err)
	}
	if err := e.kubectl(ctx, "wait", "--for="+obj.waitFor, obj.kind+"/"+obj.name, "-n", obj.namespace,
		"--timeout="+remaining(ctx, 4*time.Minute).String()); err != nil {
		return fmt.Errorf("%s (%s %s/%s): %s: %w", obj.what, obj.kind, obj.namespace, obj.name,
			cmp.Or(e.lastWarning(ctx, obj.namespace, obj.name), "no warning events"), err)
	}
	return nil
}

// Node is what platform checks read of a node: its CPU architecture as
// Kubernetes reports it (amd64, arm64) and its kernel version. For a cluster
// about to be created it describes the container engine's host, which every
// kind/Talos node container shares.
type Node struct{ Arch, Kernel string }

// Nodes returns the cluster's nodes, or nil when they cannot be read.
func (e *Env) Nodes(ctx context.Context) []Node {
	out, err := e.kubectlQuiet(ctx, "get", "nodes", "-o",
		`jsonpath={range .items[*]}{.status.nodeInfo.architecture} {.status.nodeInfo.kernelVersion}{"\n"}{end}`)
	if err != nil {
		return nil
	}
	var nodes []Node
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if arch, kernel, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
			nodes = append(nodes, Node{Arch: arch, Kernel: kernel})
		}
	}
	return nodes
}

func nodeArches(nodes []Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Arch
	}
	return out
}

// Unavailable says, per catalog app that cannot work on any of nodes, why
// not — for the app pickers to lock those rows, and for create to refuse
// them before building anything. external is true for clusters k8slocalcli
// did not create. Unknown nodes (nil) make nothing unavailable: the
// install's own precheck decides then.
func Unavailable(nodes []Node, external bool) map[string]string {
	out := map[string]string{}
	for _, a := range catalog {
		if reason := a.unavailableOn(nodes, external); reason != "" {
			out[a.Name] = reason
		}
	}
	return out
}

// unavailableOn says why the app cannot work on any of nodes, or "": no node
// has an architecture its images are built for, or its own unsupported check.
func (a App) unavailableOn(nodes []Node, external bool) string {
	if len(nodes) == 0 {
		return ""
	}
	if missing, someSupported := unsupportedArches(nodeArches(nodes), a.arches()); !someSupported {
		return fmt.Sprintf("it has no images for %s nodes (only %s)", strings.Join(missing, "/"), strings.Join(a.arches(), ", "))
	}
	if a.unsupported != nil {
		return a.unsupported(nodes, external)
	}
	return ""
}

// requireSupportedNodes stops an install that cannot work on the cluster's
// nodes (see unavailableOn) before its pods sit in ImagePullBackOff or its
// check fails ten minutes later. When only some nodes lack an architecture
// the app's images are built for, it warns: pods scheduled there will not
// start. Best effort: when the nodes cannot be read, the install goes ahead.
func requireSupportedNodes(ctx context.Context, env *Env, app App) error {
	nodes := env.Nodes(ctx)
	if reason := app.unavailableOn(nodes, env.External()); reason != "" {
		return fmt.Errorf("%s cannot work on %s: %s", app.Name, env.Name, reason)
	}
	if missing, _ := unsupportedArches(nodeArches(nodes), app.arches()); len(missing) > 0 {
		fmt.Fprintf(env.Out, "⚠️  %s has no images for %s nodes (it supports %s): its pods will not start on those nodes\n",
			app.Name, strings.Join(missing, "/"), strings.Join(app.arches(), ", "))
	}
	return nil
}

// unsupportedArches returns the distinct node architectures not in supported
// (sorted), and whether any node is supported.
func unsupportedArches(nodes, supported []string) (missing []string, someSupported bool) {
	for _, a := range nodes {
		switch {
		case slices.Contains(supported, a):
			someSupported = true
		case !slices.Contains(missing, a):
			missing = append(missing, a)
		}
	}
	slices.Sort(missing)
	return missing, someSupported
}

// requireDefaultStorageClass is a preflight for apps whose PersistentVolume
// Claims name no StorageClass.
func requireDefaultStorageClass(ctx context.Context, env *Env) error {
	if env.defaultStorageClass(ctx) != "" {
		return nil
	}
	return fmt.Errorf("%s has no default StorageClass, so its volumes would stay Pending; install one first: k8slocalcli app install local-path", env.Name)
}

// requireStorageForProduction applies requireDefaultStorageClass only to the
// production profile, where the app gets persistent volumes.
func requireStorageForProduction(ctx context.Context, env *Env) error {
	if !env.production() {
		return nil
	}
	return requireDefaultStorageClass(ctx, env)
}
