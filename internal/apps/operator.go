// operator.go holds operatorCR: apps whose manifest installs only an
// operator, which then deploys everything else because of one custom
// resource (KubeVirt, CDI).

package apps

import (
	"context"
	"fmt"
	"time"
)

// operatorCR is the custom resource that tells an operator what to deploy.
// The app's afterManifest calls deploy and its beforeRemove is remove.
type operatorCR struct {
	// ref names it for kubectl: <resource>.<group>/<name>.
	ref string
	// crd is its CustomResourceDefinition, from the operator manifest.
	crd string
	// namespace is its own namespace, "" when it is cluster-scoped.
	namespace string
	// deploysTo is the namespace the operator deploys its components into,
	// and managedBy the app.kubernetes.io/managed-by label it gives them.
	deploysTo, managedBy string
}

// args is a kubectl command line for verb on the resource.
func (c operatorCR) args(verb string, more ...string) []string {
	args := append([]string{verb, c.ref}, more...)
	if c.namespace != "" {
		args = append(args, "-n", c.namespace)
	}
	return args
}

// deploy applies the resource and waits for the operator to report it
// Available. The CRD is established and the operator's webhook serving only
// a while after the operator manifest is applied, hence the retry.
func (c operatorCR) deploy(ctx context.Context, env *Env, manifest string) error {
	if err := env.applyYAMLRetry(ctx, manifest, remaining(ctx, 3*time.Minute)); err != nil {
		return fmt.Errorf("creating %s: %w", c.ref, err)
	}
	if err := env.kubectl(ctx, c.args("wait", "--for=condition=Available",
		"--timeout="+remaining(ctx, 10*time.Minute).String())...); err != nil {
		return fmt.Errorf("%s is not Available: %w", c.ref, err)
	}
	return nil
}

// remove deletes the resource and waits until what the operator deployed is
// gone, before uninstall deletes the operator itself (without it the
// resource's finalizer never runs). The resource can be gone while its
// components are still being garbage-collected (CDI), hence the second wait.
// With uninstallStrategy BlockUninstallIfWorkloadsExist the operator's
// webhook refuses while VMs or DataVolumes exist, and says so.
//
// Unlike other apps' CRDs, the resource's own CRD is deleted too. The
// operator has just deleted every other CRD it served, and this one only
// ever held the resource just removed, so nothing is lost. Kept, it leaves
// the API group served without its other kinds, and KubeVirt's
// virt-template-controller (v0.2.3) crash-loops: it takes cdi.kubevirt.io
// being served to mean DataVolumes are ("no matches for kind DataVolume").
// With the CRD already gone (an earlier uninstall that failed later on)
// there is nothing left to remove here; kubectl would fail on the unknown
// resource type instead.
func (c operatorCR) remove(ctx context.Context, env *Env) error {
	if !env.crdExists(ctx, c.crd) {
		return nil
	}
	if _, err := env.kubectlQuiet(ctx, c.args("delete", "--ignore-not-found", "--wait=true",
		"--timeout="+remaining(ctx, removeTimeout).String())...); err != nil {
		return err
	}
	if _, err := env.kubectlQuiet(ctx, "wait", "--for=delete", "deployments,daemonsets", "-n", c.deploysTo,
		"-l", "app.kubernetes.io/managed-by="+c.managedBy, "--timeout="+remaining(ctx, removeTimeout).String()); err != nil {
		return err
	}
	_, err := env.kubectlQuiet(ctx, "delete", "crd", c.crd, "--ignore-not-found")
	return err
}
