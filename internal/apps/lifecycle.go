package apps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// Lifecycle timeouts. Each phase gets its own budget so a hang is reported
// where it happens ("verifying keycloak timed out after 5m0s") rather than as
// one opaque failure.
const (
	// connectTimeout bounds the kube context + API server reachability check.
	connectTimeout = 20 * time.Second
	// precheckTimeout bounds the read-only checks before installing.
	precheckTimeout = time.Minute
	// defaultInstallTimeout bounds prepare + install (helm --wait, Flux
	// HelmRelease Ready, Argo CD Synced/Healthy) unless an app sets Timeout.
	defaultInstallTimeout = 10 * time.Minute
	// verifyTimeout bounds waiting for every workload to finish rolling out.
	verifyTimeout = 5 * time.Minute
	// postcheckTimeout bounds configure + the app's functional check.
	postcheckTimeout = 5 * time.Minute
)

// CheckConnection verifies that the target's kube context exists and its API
// server answers. It is cached per Env, so installing several apps checks once.
func CheckConnection(ctx context.Context, env *Env) error {
	if env.connected {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	if !runner.LookPath("kubectl") {
		return fmt.Errorf("kubectl is required (brew install kubectl, or https://kubernetes.io/docs/tasks/tools/)")
	}
	contexts, err := runner.New(nil).Capture(ctx, "kubectl", "config", "get-contexts", "-o", "name")
	if err == nil && !containsLine(contexts, env.KubeContext) {
		return fmt.Errorf("kube context %q is not in your kubeconfig (see: kubectl config get-contexts)", env.KubeContext)
	}
	// /readyz is the API server's own health endpoint; some RBAC setups deny
	// it, so fall back to /version, which every authenticated user may read.
	if _, err := env.kubectlQuiet(ctx, "--request-timeout=10s", "get", "--raw=/readyz"); err != nil {
		if _, verr := env.kubectlQuiet(ctx, "--request-timeout=10s", "version", "-o", "json"); verr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("the API server of context %q did not answer within %s", env.KubeContext, connectTimeout)
			}
			return fmt.Errorf("cannot reach the API server of context %q: %w", env.KubeContext, verr)
		}
	}
	env.connected = true
	return nil
}

func containsLine(s, want string) bool {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

// InstallAll plans reqs (adding dependencies, ordering them) and installs each
// in turn. It stops at the first failure.
func InstallAll(ctx context.Context, env *Env, reqs []cluster.AppRequest) error {
	if len(reqs) == 0 {
		return nil
	}
	plan, added, err := Plan(reqs)
	if err != nil {
		return err
	}
	for _, a := range added {
		fmt.Fprintf(env.Out, "➕ Adding %s\n", a)
	}
	if err := CheckConnection(ctx, env); err != nil {
		return err
	}
	for _, req := range plan {
		if err := Install(ctx, env, req); err != nil {
			return err
		}
	}
	return nil
}

// Install runs one app through the lifecycle: connection check → precheck →
// prepare + install → verify → configure + postcheck → route and notes.
func Install(ctx context.Context, env *Env, req cluster.AppRequest) error {
	implicit := req.Implicit
	req, err := Resolve(req)
	if err != nil {
		return err
	}
	app, _ := Lookup(req.Name)

	if err := CheckConnection(ctx, env); err != nil {
		return err
	}

	// Precheck runs outside any progress step: preflight and the "already
	// installed" question may prompt.
	installed, err := precheck(ctx, env, app, req.Method)
	if err != nil {
		return fmt.Errorf("%s precheck: %w", app.Name, err)
	}
	if installed {
		switch reapply, why := shouldReapply(ctx, env, app, req.Method, implicit); {
		case !reapply:
			fmt.Fprintf(env.Out, "⏭️  %s is already installed in %s%s — leaving it as it is\n", app.Name, env.Name, why)
			if !implicit {
				printAccess(ctx, env, app, routedHost(ctx, env, app))
			}
			return nil
		default:
			fmt.Fprintf(env.Out, "🔁 Re-applying %s\n", app.Name)
		}
	}

	route := app.Expose != nil && env.Exposed && GatewayInstalled(ctx, env)
	host := ""
	if route {
		host = req.Host
	}

	title := fmt.Sprintf("Installing %s via %s (%s profile)", app.Description, req.Method, env.Profile.OrDefault())
	if err := env.step(ctx, title, app.Namespace, func(se *Env) error {
		return withTimeout(ctx, app.timeout(), "install", func(ctx context.Context) error {
			return installApp(ctx, se, app, req.Method, host)
		})
	}); err != nil {
		return fmt.Errorf("installing %s: %w", app.Name, err)
	}

	if err := env.step(ctx, "Verifying "+app.Name+" — waiting for every workload to be ready", app.Namespace, func(se *Env) error {
		return withTimeout(ctx, verifyTimeout, "verify", func(ctx context.Context) error {
			return verifyWorkloads(ctx, se, app)
		})
	}); err != nil {
		return fmt.Errorf("verifying %s: %w", app.Name, err)
	}

	if err := env.step(ctx, "Checking "+app.Name+" works", "", func(se *Env) error {
		return withTimeout(ctx, postcheckTimeout, "postcheck", func(ctx context.Context) error {
			return postcheck(ctx, se, app, route, req.Host)
		})
	}); err != nil {
		return fmt.Errorf("postcheck of %s: %w", app.Name, err)
	}

	routed := ""
	if app.Expose != nil {
		switch {
		case !env.Exposed && env.External():
			fmt.Fprintf(env.Out, "ℹ️  %s is not routed: context %q is not a k8slocalcli cluster. Use: kubectl --context %s port-forward -n %s svc/%s <local-port>:%d\n",
				app.Name, env.KubeContext, env.KubeContext, app.Namespace, app.Expose.Service, app.Expose.Port)
		case !env.Exposed:
			fmt.Fprintf(env.Out, "ℹ️  %s is not exposed: cluster %q publishes no gateway ports (created without host exposure, or by an older k8slocalcli). Use: kubectl --context %s port-forward -n %s svc/%s <local-port>:%d\n",
				app.Name, env.Name, env.KubeContext, app.Namespace, app.Expose.Service, app.Expose.Port)
		case !route:
			fmt.Fprintf(env.Out, "⚠️  %s is not exposed: no Gateway %s/%s found in cluster %q\n", app.Name, GatewayNamespace, GatewayName, env.Name)
		default:
			routed = req.Host
			checkHostResolves(ctx, env, req.Host)
		}
	}

	printAccess(ctx, env, app, routed)
	fmt.Fprintf(env.Out, "✅ %s installed  (show URLs and credentials again: k8slocalcli info %s)\n", app.Name, env.Name)
	return nil
}

func (a App) timeout() time.Duration {
	if a.Timeout > 0 {
		return a.Timeout
	}
	return defaultInstallTimeout
}

// withTimeout runs fn under a deadline and names the phase when it runs out,
// since the underlying error ("signal: killed", "context deadline exceeded")
// says nothing about what was being waited for.
func withTimeout(ctx context.Context, d time.Duration, phase string, fn func(ctx context.Context) error) error {
	tctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	err := fn(tctx)
	if err != nil && errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return fmt.Errorf("%s timed out after %s: %w", phase, d, err)
	}
	return err
}

// precheck runs the read-only checks before installing and reports whether
// the app is already installed.
func precheck(ctx context.Context, env *Env, app App, method cluster.InstallMethod) (installed bool, err error) {
	// Preflight may prompt, so it gets the caller's ctx, not the precheck
	// deadline (a user thinking about a question is not a hang).
	cctx, cancel := context.WithTimeout(ctx, precheckTimeout)
	defer cancel()

	if len(env.Values[app.Name]) > 0 && method == cluster.MethodManifest {
		return false, fmt.Errorf("--values only applies to the chart methods (helm, flux, argo), not %s", method)
	}
	switch method {
	case cluster.MethodHelm:
		if !runner.LookPath("helm") {
			return false, fmt.Errorf("helm is required for the helm method (brew install helm, or https://helm.sh/docs/intro/install/) — or choose another method (supported: %s)", MethodsText(app.Methods))
		}
	case cluster.MethodFlux:
		if !env.crdExists(cctx, fluxHelmReleaseCRD) {
			return false, fmt.Errorf("the flux method needs Flux, which is not installed in %s; install it first: k8slocalcli app install flux", env.Name)
		}
	case cluster.MethodArgo:
		if !env.crdExists(cctx, argoApplicationCRD) {
			return false, fmt.Errorf("the argo method needs Argo CD, which is not installed in %s; install it first: k8slocalcli app install argocd", env.Name)
		}
	}
	for _, d := range app.Requires {
		dep, ok := Lookup(d)
		if !ok {
			return false, fmt.Errorf("unknown dependency %q", d)
		}
		if !dependencyPresent(cctx, env, dep) {
			return false, fmt.Errorf("%s needs %s, which is not installed; install it first: k8slocalcli app install %s", app.Name, dep.Name, dep.Name)
		}
	}
	if app.preflight != nil {
		if err := app.preflight(ctx, env); err != nil {
			return false, err
		}
	}
	return appInstalled(cctx, env, app), nil
}

// shouldReapply decides what to do with an app that is already installed.
// why explains a "no" that the user did not choose.
func shouldReapply(ctx context.Context, env *Env, app App, method cluster.InstallMethod, implicit bool) (bool, string) {
	if implicit {
		return false, ""
	}
	if method == cluster.MethodHelm && !env.helmManaged(ctx, app) {
		// e.g. kind's own local-path-provisioner: Helm refuses to adopt
		// resources it did not create ("invalid ownership metadata").
		return false, " (not managed by Helm, so Helm cannot take it over)"
	}
	if env.Reinstall {
		return true, ""
	}
	if env.Confirm == nil {
		return false, " (re-apply with --reinstall)"
	}
	ok, err := env.Confirm(fmt.Sprintf("%s is already installed in %s. Re-apply it (upgrade to the pinned version and current settings)?", app.Name, env.Name), false)
	if err != nil {
		return false, " (re-apply with --reinstall)"
	}
	return ok, ""
}

// installApp runs prepare and the chosen install method. It is also used by
// SetupExposure to install cert-manager.
func installApp(ctx context.Context, env *Env, app App, method cluster.InstallMethod, host string) error {
	if app.Privileged {
		if err := env.ensurePrivilegedNamespace(ctx, app.Namespace); err != nil {
			return err
		}
	}
	if app.prepare != nil {
		if err := app.prepare(ctx, env); err != nil {
			return fmt.Errorf("preparing: %w", err)
		}
	}
	timeout := app.timeout()
	if dl, ok := ctx.Deadline(); ok {
		// Leave helm/flux/argo a little less than the step's own budget, so
		// their timeout message (which names what was not ready) wins.
		if left := time.Until(dl) - 10*time.Second; left > 0 && left < timeout {
			timeout = left.Round(time.Second)
		}
	}
	switch method {
	case cluster.MethodManifest:
		if app.manifestURL == "" {
			return fmt.Errorf("%s has no manifest install", app.Name)
		}
		args := []string{"apply", "--server-side", "--force-conflicts", "-f", app.manifestURL}
		if ns := app.manifestNamespace; ns != "" {
			if err := env.applyYAML(ctx, namespaceYAML(ns)); err != nil {
				return err
			}
			args = append(args, "-n", ns)
		}
		if err := env.kubectl(ctx, args...); err != nil {
			return err
		}
		if app.afterManifest != nil {
			if err := app.afterManifest(ctx, env); err != nil {
				return err
			}
		}
		// Plain manifests leave no record of who applied them; the label
		// lets uninstall tell our install from one it must not touch
		// (kind's own local-path-provisioner).
		return env.markManifestInstall(ctx, app)
	case cluster.MethodHelm, cluster.MethodFlux, cluster.MethodArgo:
		if app.chart == nil {
			return fmt.Errorf("%s has no Helm chart", app.Name)
		}
		c, err := app.renderChart(ctx, env, host)
		if err != nil {
			return err
		}
		switch method {
		case cluster.MethodHelm:
			return env.helmInstall(ctx, c, timeout)
		case cluster.MethodFlux:
			return env.fluxInstall(ctx, app.Name, resolveVersion(ctx, c), timeout)
		default:
			return env.argoInstall(ctx, app.Name, resolveVersion(ctx, c), timeout)
		}
	default:
		return fmt.Errorf("unsupported method %q", method)
	}
}

// verifyWorkloads waits for the app's workloads to finish rolling out.
func verifyWorkloads(ctx context.Context, env *Env, app App) error {
	refs := app.Workloads
	if len(refs) == 0 {
		out, err := env.kubectlQuiet(ctx, "get", "deployments,statefulsets,daemonsets", "-n", app.Namespace, "-o", "name")
		if err != nil {
			return err
		}
		refs = strings.Fields(out)
		if len(refs) == 0 {
			return fmt.Errorf("no Deployments, StatefulSets or DaemonSets found in namespace %s", app.Namespace)
		}
	}
	for _, ref := range refs {
		if err := env.waitWorkload(ctx, app.Namespace, ref); err != nil {
			return fmt.Errorf("%s is not ready: %w", ref, err)
		}
	}
	return nil
}

// waitWorkload waits for a Deployment, StatefulSet or DaemonSet to be ready.
// `rollout status` only works for the RollingUpdate strategy; workloads using
// OnDelete (OpenBao's StatefulSet) are instead waited on until every desired
// replica is ready.
func (e *Env) waitWorkload(ctx context.Context, ns, ref string) error {
	left := remaining(ctx, verifyTimeout).String()
	strategy, err := e.kubectlQuiet(ctx, "get", ref, "-n", ns,
		"-o", "jsonpath={.spec.updateStrategy.type}")
	if err != nil || strings.TrimSpace(strategy) != "OnDelete" {
		return e.kubectl(ctx, "rollout", "status", ref, "-n", ns, "--timeout="+left)
	}
	field, desired := "readyReplicas", "{.spec.replicas}"
	if strings.HasPrefix(ref, "daemonset") {
		field, desired = "numberReady", "{.status.desiredNumberScheduled}"
	}
	want, err := e.kubectlQuiet(ctx, "get", ref, "-n", ns, "-o", "jsonpath="+desired)
	if err != nil {
		return err
	}
	return e.kubectl(ctx, "wait", ref, "-n", ns,
		"--for=jsonpath={.status."+field+"}="+strings.TrimSpace(want), "--timeout="+left)
}

// postcheck runs configure, waits for the app's CRD, runs its own check and
// adds its route.
func postcheck(ctx context.Context, env *Env, app App, route bool, host string) error {
	if app.configure != nil {
		if err := app.configure(ctx, env); err != nil {
			return fmt.Errorf("configuring: %w", err)
		}
	}
	if app.CRD != "" {
		if err := env.kubectl(ctx, "wait", "--for=condition=Established", "crd/"+app.CRD,
			"--timeout="+remaining(ctx, time.Minute).String()); err != nil {
			return err
		}
	}
	if app.check != nil {
		if err := app.check(ctx, env); err != nil {
			return err
		}
	}
	if route {
		return exposeApp(ctx, env, app, host)
	}
	return nil
}

func exposeApp(ctx context.Context, env *Env, app App, host string) error {
	if err := env.applyYAML(ctx, app.Expose.routeYAML(app.Name, app.Namespace, host)); err != nil {
		return fmt.Errorf("routing %s to %s: %w", host, app.Name, err)
	}
	return nil
}

// --- shared queries ---

// appInstalled reports whether any of the app's Detect resources exists.
func appInstalled(ctx context.Context, env *Env, app App) bool {
	for _, ref := range app.Detect {
		if env.exists(ctx, app.Namespace, ref) {
			return true
		}
	}
	return false
}

// dependencyPresent reports whether a dependency is usable: its CRD is served
// (however it was installed) or the catalog install is detected.
func dependencyPresent(ctx context.Context, env *Env, dep App) bool {
	if dep.CRD != "" && env.crdExists(ctx, dep.CRD) {
		return true
	}
	return appInstalled(ctx, env, dep)
}
