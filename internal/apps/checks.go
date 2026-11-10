// checks.go holds reusable preflights (run before installing) and
// functional checks (run after), shared by several catalog apps.

package apps

import (
	"context"
	"fmt"
	"time"
)

// smokeImage is a small image with a shell for functional checks.
const smokeImage = "busybox:1.37"

// storageSmokeTest proves a StorageClass works end to end: a PVC binds, a pod
// mounts it, writes and reads a file, and both are removed again.
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
      command: ["sh", "-c", "echo ok > /data/smoke && cat /data/smoke"]
      volumeMounts:
        - name: data
          mountPath: /data
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
