// checks.go holds reusable preflights (run before installing) and
// functional checks (run after), shared by several catalog apps.

package apps

import (
	"context"
	"errors"
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

// requireNodeArch stops an install before its pods sit in ImagePullBackOff
// when no node has a CPU architecture the app's images are built for. When
// only some nodes lack it, it warns: pods scheduled there will not start.
// Best effort: when the nodes cannot be read, the install goes ahead.
func requireNodeArch(ctx context.Context, env *Env, app App) error {
	out, err := env.kubectlQuiet(ctx, "get", "nodes", "-o", "jsonpath={.items[*].status.nodeInfo.architecture}")
	if err != nil {
		return nil
	}
	missing, someSupported := unsupportedArches(strings.Fields(out), app.arches())
	if len(missing) == 0 {
		return nil
	}
	msg := fmt.Sprintf("%s has no images for %s nodes (it supports %s)", app.Name, strings.Join(missing, "/"), strings.Join(app.arches(), ", "))
	if !someSupported {
		return errors.New(msg)
	}
	fmt.Fprintf(env.Out, "⚠️  %s: its pods will not start on those nodes\n", msg)
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
