package apps

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// Uninstall timeouts.
const (
	// removeTimeout bounds the method's own removal (helm uninstall --wait,
	// deleting a HelmRelease/Application and waiting for it to go).
	removeTimeout = 5 * time.Minute
	// goneTimeout bounds waiting for the app's workloads to disappear.
	goneTimeout = 2 * time.Minute
	// namespaceTimeout bounds deleting a namespace with --purge (volumes
	// are released, finalizers run).
	namespaceTimeout = 5 * time.Minute
)

// manifestMarker labels a resource installed with the manifest method. Plain
// `kubectl apply` leaves no record of who applied what, and without it
// uninstall could not tell its own install from one it must not remove, such
// as kind's built-in local-path-provisioner.
const manifestMarker = "k8slocalcli.io/installed-by"

// protectedNamespaces are never deleted, whatever --purge says.
var protectedNamespaces = []string{"default", "kube-system", "kube-public", "kube-node-lease", GatewayNamespace}

func (e *Env) markManifestInstall(ctx context.Context, app App) error {
	for _, ref := range app.Detect {
		if e.exists(ctx, app.Namespace, ref) {
			_, err := e.kubectlQuiet(ctx, "label", "--overwrite", ref, "-n", app.Namespace, manifestMarker+"=manifest")
			return err
		}
	}
	return nil
}

// InstallState says whether an app is in the cluster and whether
// k8slocalcli put it there.
type InstallState int

const (
	// NotInstalled: none of the app's Detect resources exist.
	NotInstalled InstallState = iota
	// InstalledByUs: a Helm release, HelmRelease, Application or labelled
	// manifest install that k8slocalcli made, so it can be removed.
	InstalledByUs
	// InstalledByOthers: present, but not through any of those (e.g. kind's
	// own local-path-provisioner); uninstall leaves it alone.
	InstalledByOthers
)

// Installation is how an installed app was put into the cluster, worked out
// from the cluster itself.
type Installation struct {
	State  InstallState
	Method cluster.InstallMethod
	// Release and ReleaseNamespace name the Helm release (helm, flux, argo).
	Release          string
	ReleaseNamespace string
	// Found describes what exists when State is InstalledByOthers.
	Found string
}

// DetectInstallation finds how app is installed: an Argo CD Application or
// Flux HelmRelease named after it, a Helm release, or a labelled manifest
// install.
func DetectInstallation(ctx context.Context, env *Env, app App) Installation {
	var inst Installation
	if app.chart != nil {
		inst.Release, inst.ReleaseNamespace = app.chart.Release, app.chart.Namespace
	}
	ours := func(m cluster.InstallMethod) Installation {
		inst.State, inst.Method = InstalledByUs, m
		return inst
	}
	switch {
	case env.crdExists(ctx, argoApplicationCRD) && env.exists(ctx, argoCDNamespace, argoApplicationCRD+"/"+app.Name):
		return ours(cluster.MethodArgo)
	case env.crdExists(ctx, fluxHelmReleaseCRD) && env.exists(ctx, fluxNamespace, fluxHelmReleaseCRD+"/"+app.Name):
		return ours(cluster.MethodFlux)
	case inst.Release != "" && env.helmReleaseExists(ctx, inst.Release, inst.ReleaseNamespace):
		return ours(cluster.MethodHelm)
	}
	for _, ref := range app.Detect {
		out, err := env.kubectlQuiet(ctx, "get", ref, "-n", app.Namespace,
			"-o", "jsonpath={.metadata.labels.k8slocalcli\\.io/installed-by}")
		if err != nil {
			continue
		}
		if strings.TrimSpace(out) == "manifest" {
			return ours(cluster.MethodManifest)
		}
		inst.State = InstalledByOthers
		inst.Found = fmt.Sprintf("%s exists but was not installed by k8slocalcli (no Helm release, HelmRelease, Application or %s label)", ref, manifestMarker)
		return inst
	}
	return inst
}

func (e *Env) helmReleaseExists(ctx context.Context, release, namespace string) bool {
	if !runner.LookPath("helm") {
		return false
	}
	_, err := e.helmQuiet(ctx, "status", release, "--namespace", namespace)
	return err == nil
}

// Dependants returns why app cannot be removed safely: installed apps that
// require it, apps handed to it as a GitOps controller, and the gateway
// certificate when it is cert-manager. Apps in removing are being removed in
// the same run and do not count.
func Dependants(ctx context.Context, env *Env, app App, removing map[string]bool) []string {
	var out []string
	for _, other := range Catalog() {
		if removing[other.Name] || !slices.Contains(other.Requires, app.Name) {
			continue
		}
		if appInstalled(ctx, env, other) {
			out = append(out, other.Name+" requires it")
		}
	}
	listOthers := func(kind, ns string) {
		names, err := env.kubectlQuiet(ctx, "get", kind, "-n", ns, "-o", "jsonpath={.items[*].metadata.name}")
		if err != nil {
			return
		}
		for _, n := range strings.Fields(names) {
			if !removing[n] {
				out = append(out, fmt.Sprintf("%s %s is managed by it", strings.Split(kind, ".")[0], n))
			}
		}
	}
	switch app.Name {
	case fluxApp.Name:
		listOthers(fluxHelmReleaseCRD, fluxNamespace)
	case argoCDApp.Name:
		listOthers(argoApplicationCRD, argoCDNamespace)
	case certManagerApp.Name:
		if env.exists(ctx, GatewayNamespace, "certificates.cert-manager.io/"+TLSSecretName) {
			out = append(out, "the gateway's verified HTTPS certificate is issued by it")
		}
	}
	return out
}

// RemovalOrder resolves names and orders them so dependants are removed
// before what they depend on (the reverse of Plan).
func RemovalOrder(names []string) ([]App, error) {
	reqs := make([]cluster.AppRequest, len(names))
	for i, n := range names {
		reqs[i] = cluster.AppRequest{Name: n}
	}
	plan, _, err := Plan(reqs)
	if err != nil {
		return nil, err
	}
	asked := map[string]bool{}
	for _, n := range names {
		a, _ := Lookup(n) // Plan has already rejected unknown names
		asked[a.Name] = true
	}
	var out []App
	for i := len(plan) - 1; i >= 0; i-- {
		if asked[plan[i].Name] { // Plan adds dependencies; only remove what was asked
			a, _ := Lookup(plan[i].Name)
			out = append(out, a)
		}
	}
	return out, nil
}

// Removal is one app to uninstall and how it was installed.
type Removal struct {
	App          App
	Installation Installation
}

// PlanUninstall checks every named app before anything is removed: it orders
// them (dependants first), finds how each was installed, and refuses the
// whole run when one was not installed by k8slocalcli or is still needed
// (unless force). notInstalled lists the names that were not there at all.
func PlanUninstall(ctx context.Context, env *Env, names []string, force bool) (plan []Removal, notInstalled []string, err error) {
	order, err := RemovalOrder(names)
	if err != nil {
		return nil, nil, err
	}
	removing := map[string]bool{}
	for _, a := range order {
		removing[a.Name] = true
	}
	var problems []string
	for _, a := range order {
		inst := DetectInstallation(ctx, env, a)
		switch inst.State {
		case NotInstalled:
			notInstalled = append(notInstalled, a.Name)
			continue
		case InstalledByOthers:
			problems = append(problems, a.Name+": "+inst.Found)
			continue
		}
		if deps := Dependants(ctx, env, a, removing); len(deps) > 0 && !force {
			problems = append(problems, fmt.Sprintf("%s is still needed: %s (remove those first, or --force)", a.Name, strings.Join(deps, "; ")))
			continue
		}
		plan = append(plan, Removal{App: a, Installation: inst})
	}
	if len(problems) > 0 {
		return nil, notInstalled, fmt.Errorf("not uninstalling anything:\n  %s", strings.Join(problems, "\n  "))
	}
	return plan, notInstalled, nil
}

// PurgeNamespaces are the namespaces --purge deletes for the app.
func (a App) PurgeNamespaces() []string {
	var out []string
	for _, ns := range append([]string{a.Namespace}, a.DataNamespaces...) {
		if !slices.Contains(protectedNamespaces, ns) {
			out = append(out, ns)
		}
	}
	return out
}

// UninstallOptions controls Uninstall.
type UninstallOptions struct {
	// Purge also deletes the app's namespace (and dataNamespaces) with
	// every PersistentVolumeClaim in them. Without it data is kept.
	Purge bool
}

// Uninstall removes one app the way it was installed, then its route, waits
// for its workloads to be gone and, with Purge, deletes its namespaces.
// CRDs are kept (as Helm does): deleting one deletes every resource of that
// kind in the cluster.
func Uninstall(ctx context.Context, env *Env, app App, inst Installation, opts UninstallOptions) error {
	var kept []string
	// Data the app's operator manages goes first, while the operator is
	// still there to run its finalizers.
	for _, ns := range app.DataNamespaces {
		k, err := env.purgeNamespace(ctx, ns, opts.Purge)
		if err != nil {
			return fmt.Errorf("removing %s: %w", app.Name, err)
		}
		kept = append(kept, k...)
	}

	title := fmt.Sprintf("Removing %s (installed via %s)", app.Name, inst.Method)
	if err := env.step(ctx, title, app.Namespace, func(se *Env) error {
		return withTimeout(ctx, removeTimeout, "remove", func(ctx context.Context) error {
			if app.beforeRemove != nil {
				if err := app.beforeRemove(ctx, se); err != nil {
					return err
				}
			}
			if err := removeByMethod(ctx, se, app, inst); err != nil {
				return err
			}
			if app.Expose != nil && se.crdExists(ctx, app.Expose.routeKind()) {
				_, _ = se.kubectlQuiet(ctx, "delete", app.Expose.routeKind(), app.Name, "-n", app.Namespace, "--ignore-not-found")
			}
			return nil
		})
	}); err != nil {
		return fmt.Errorf("removing %s: %w", app.Name, err)
	}

	if err := env.step(ctx, "Checking "+app.Name+" is gone", app.Namespace, func(se *Env) error {
		return withTimeout(ctx, goneTimeout, "waiting for removal", func(ctx context.Context) error {
			return retry(ctx, goneTimeout, 3*time.Second, func(ctx context.Context) error {
				if appInstalled(ctx, se, app) {
					return fmt.Errorf("%s is still present", strings.Join(app.Detect, " / "))
				}
				return nil
			})
		})
	}); err != nil {
		return fmt.Errorf("removing %s: %w", app.Name, err)
	}

	k, err := env.purgeNamespace(ctx, app.Namespace, opts.Purge)
	if err != nil {
		return fmt.Errorf("removing %s: %w", app.Name, err)
	}
	kept = append(kept, k...)
	fmt.Fprintf(env.Out, "🗑️  %s removed", app.Name)
	if len(kept) > 0 {
		fmt.Fprintf(env.Out, " — kept namespace %s (may hold generated passwords and data volumes); delete it too with --purge", strings.Join(kept, ", "))
	}
	if app.CRD != "" {
		fmt.Fprintf(env.Out, "\n   CRDs are kept (deleting %s would delete every resource of that kind)", app.CRD)
	}
	fmt.Fprintln(env.Out)
	return nil
}

// purgeNamespace deletes ns (and its volumes) when purge is set and returns
// it as kept otherwise. Protected and missing namespaces are skipped.
func (e *Env) purgeNamespace(ctx context.Context, ns string, purge bool) (kept []string, err error) {
	if slices.Contains(protectedNamespaces, ns) || !e.exists(ctx, "", "namespace/"+ns) {
		return nil, nil
	}
	if !purge {
		return []string{ns}, nil
	}
	return nil, e.step(ctx, "Deleting namespace "+ns+" and its volumes", "", func(se *Env) error {
		return withTimeout(ctx, namespaceTimeout, "namespace deletion", func(ctx context.Context) error {
			return se.kubectl(ctx, "delete", "namespace", ns, "--wait=true", "--timeout="+remaining(ctx, namespaceTimeout).String())
		})
	})
}

func removeByMethod(ctx context.Context, env *Env, app App, inst Installation) error {
	timeout := remaining(ctx, removeTimeout).String()
	switch inst.Method {
	case cluster.MethodArgo:
		// The resources finalizer makes Argo CD delete what it synced
		// before the Application goes away.
		if err := env.kubectl(ctx, "delete", argoApplicationCRD, app.Name, "-n", argoCDNamespace,
			"--wait=true", "--timeout="+timeout); err != nil {
			return err
		}
		_, _ = env.kubectlQuiet(ctx, "delete", "secret", "k8slocalcli-oci-"+app.Name, "-n", argoCDNamespace, "--ignore-not-found")
		return nil
	case cluster.MethodFlux:
		// Deleting the HelmRelease makes helm-controller uninstall it.
		if err := env.kubectl(ctx, "delete", fluxHelmReleaseCRD, app.Name, "-n", fluxNamespace,
			"--wait=true", "--timeout="+timeout); err != nil {
			return err
		}
		for _, src := range []string{fluxOCIRepositoryCRD, fluxHelmRepositoryCRD} {
			_, _ = env.kubectlQuiet(ctx, "delete", src, app.Name, "-n", fluxNamespace, "--ignore-not-found")
		}
		return nil
	case cluster.MethodHelm:
		return env.helm(ctx, "uninstall", inst.Release, "--namespace", inst.ReleaseNamespace, "--wait", "--timeout", timeout)
	case cluster.MethodManifest:
		return env.deleteManifest(ctx, app)
	default:
		return fmt.Errorf("unsupported method %q", inst.Method)
	}
}

// deleteManifest deletes what the app's manifest created, except CRDs and
// Namespaces: CRDs because deleting one deletes all its resources cluster-wide
// (Helm keeps them too), Namespaces because that is what --purge decides.
func (e *Env) deleteManifest(ctx context.Context, app App) error {
	if app.manifestURL == "" {
		return fmt.Errorf("%s has no manifest to remove", app.Name)
	}
	manifest, err := fetch(ctx, app.manifestURL)
	if err != nil {
		return err
	}
	kept, err := withoutKinds(manifest, "CustomResourceDefinition", "Namespace")
	if err != nil {
		return err
	}
	f, err := writeTemp("k8slocalcli-*.yaml", kept)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f) }()
	args := []string{"delete", "-f", f, "--ignore-not-found", "--wait=true", "--timeout=" + remaining(ctx, removeTimeout).String()}
	if app.manifestNamespace != "" {
		args = append(args, "-n", app.manifestNamespace)
	}
	_, err = e.kubectlQuiet(ctx, args...)
	return err
}

// withoutKinds drops documents of the given kinds from a YAML stream.
func withoutKinds(manifest string, kinds ...string) (string, error) {
	var kept []string
	for _, doc := range splitDocuments(manifest) {
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			return "", err
		}
		if meta.Kind == "" || slices.Contains(kinds, meta.Kind) {
			continue
		}
		kept = append(kept, doc)
	}
	return strings.Join(kept, "\n---\n") + "\n", nil
}
