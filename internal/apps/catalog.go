package apps

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// Protocol is how an app's exposed Service is routed through the gateway.
type Protocol string

const (
	ProtocolHTTP Protocol = "http" // HTTPRoute
	ProtocolGRPC Protocol = "grpc" // GRPCRoute
)

// Expose describes the Service an app makes reachable from the host.
type Expose struct {
	// DefaultHost is used when the request gives no hostname; it is always a
	// single label under localtest.me.
	DefaultHost string
	Service     string
	Port        int
	Protocol    Protocol
	// LocalPort is suggested for kubectl port-forward when the app is not
	// routed; zero means Port. Distinct ports let several apps be forwarded
	// at once.
	LocalPort int
}

// Gateway API route kinds (also their CRD names).
const (
	httpRouteCRD = "httproutes.gateway.networking.k8s.io"
	grpcRouteCRD = "grpcroutes.gateway.networking.k8s.io"
)

// routeKind is the Gateway API route resource serving this Expose.
func (x *Expose) routeKind() string {
	if x.Protocol == ProtocolGRPC {
		return grpcRouteCRD
	}
	return httpRouteCRD
}

// routeYAML renders the route for app at host.
func (x *Expose) routeYAML(app, namespace, host string) string {
	if x.Protocol == ProtocolGRPC {
		return grpcRouteYAML(app, namespace, host, x.Service, x.Port)
	}
	return httpRouteYAML(app, namespace, host, x.Service, x.Port)
}

// Category groups apps in listings and the TUI picker.
type Category string

const (
	CategoryNetworking    Category = "networking"
	CategoryStorage       Category = "storage"
	CategoryGitOps        Category = "gitops"
	CategoryPlatform      Category = "platform"
	CategoryData          Category = "data"
	CategorySecurity      Category = "security"
	CategoryObservability Category = "observability"
	CategoryManagement    Category = "management"
	CategoryDemo          Category = "demo"
)

// App is a catalog application. An app only describes what is specific to
// it; everything else — install methods, timeouts, checks, routing, uninstall —
// is shared (lifecycle.go, uninstall.go). Install methods are derived:
//
//   - chart       → helm, flux, argo (flux/argo unless noGitOps)
//   - manifestURL → manifest
//
// The hook fields are listed in the order they run. Install: preflight →
// prepare → (chart+values | manifestURL+afterManifest) → verify Workloads →
// configure → CRD Established → check → route → notes. Uninstall:
// beforeRemove → remove by method → route deleted → wait until gone →
// (--purge) dataNamespaces and Namespace deleted.
//
// Rules a new app must follow:
//   - Pin chart versions after checking them (`helm show chart`), then run
//     TestImageArchAudit for the app: every image needs an amd64 and an
//     arm64 build unless the app narrows Arches.
//   - values must be plain maps (users merge their own over them with
//     --values) and may read the cluster (default StorageClass, Talos).
//   - notes must read live state, never env.Profile: `info` does not know
//     which profile the app was installed with.
//   - Apps whose pods need host access set privileged.
//
// Adding an app = an App var in the file for its category, an entry in
// catalog below, optionally a preset, and — for a controller other apps are
// handed to — a case in Dependants. TestCatalogEntries checks the rest.
type App struct {
	// --- identity ---
	Name string
	// Aliases are extra names accepted on the command line.
	Aliases     []string
	Description string
	Category    Category
	// Methods lists the supported install methods; the first is the default.
	// Derived from chart/manifestURL by finalize unless set explicitly.
	Methods   []cluster.InstallMethod
	Namespace string

	// --- what "installed" and "ready" mean ---

	// Detect lists resources ("kind/name" in Namespace); any of them existing
	// means the app is installed. Several are needed when the chart and the
	// manifest name things differently. `info` uses it too.
	Detect []string
	// CRD is a CustomResourceDefinition the app provides. Dependants check it
	// (so an operator installed by other means still counts), and the
	// postcheck waits for it to be Established.
	CRD string
	// Workloads limits verification to these "kind/name" resources; empty
	// means every Deployment, StatefulSet and DaemonSet in Namespace.
	Workloads []string
	// Requires lists catalog apps that must be installed first. Plan adds
	// them automatically.
	Requires []string
	// Expose is nil for apps with nothing to reach from the host.
	Expose *Expose
	// Timeout bounds the install step (and helm/flux/argo waits inside it).
	// Zero means defaultInstallTimeout.
	Timeout time.Duration
	// Privileged marks apps whose pods need host access (hostPath, host
	// network, extra capabilities). Their namespace is labelled for the
	// privileged Pod Security level before installing; Talos (and any
	// cluster enforcing baseline) rejects those pods otherwise.
	Privileged bool
	// DataNamespaces are namespaces outside Namespace that the app creates
	// for data (cnpg's sample postgres cluster); --purge deletes them too.
	DataNamespaces []string
	// Arches lists the node CPU architectures (as a node reports them in
	// status.nodeInfo.architecture) the app's images are built for; empty
	// means defaultArches. The precheck refuses clusters with no such node,
	// and TestImageArchAudit fails when a pinned image lacks one of them.
	Arches []string

	// --- how to install (sources) ---

	// chart is the Helm chart; nil when the app has none. Static, so
	// uninstall can learn the release without touching the cluster.
	chart *helmChart
	// values returns the chart values for env; host is the routed hostname
	// or "". Nil means the chart defaults.
	values func(ctx context.Context, env *Env, host string) (map[string]any, error)
	// manifestURL is the upstream install manifest, applied server-side;
	// uninstall deletes what it contains (except CRDs and Namespaces).
	manifestURL string
	// manifestNamespace is created and passed as -n for manifests that name
	// no namespace.
	manifestNamespace string
	// noGitOps keeps flux/argo out of the derived methods (the GitOps
	// controllers themselves).
	noGitOps bool

	// --- install hooks, in the order they run ---

	// preflight runs during precheck, outside any progress step, so it may
	// prompt. It returns an error to stop the install.
	preflight func(ctx context.Context, env *Env) error
	// prepare runs before the install, e.g. Secrets or a database the chart
	// expects to exist.
	prepare func(ctx context.Context, env *Env) error
	// afterManifest patches what manifestURL created (manifest method only).
	afterManifest func(ctx context.Context, env *Env) error
	// configure runs after the workloads are verified, e.g. creating custom
	// resources served by the operator just installed.
	configure func(ctx context.Context, env *Env) error
	// check is the app-specific postcheck: does it actually work?
	check func(ctx context.Context, env *Env) error
	// notes prints how to use the app (credentials, CLI hints) after its URL.
	// host is the routed hostname, or "" when the app is not routed.
	notes func(ctx context.Context, env *Env, host string)

	// --- uninstall hooks ---

	// beforeRemove runs before uninstalling, e.g. Longhorn's deletion flag.
	beforeRemove func(ctx context.Context, env *Env) error
}

// defaultArches are the node architectures every app must support unless it
// sets Arches: Intel/AMD machines and Apple Silicon (whose Docker nodes are
// arm64).
var defaultArches = []string{"amd64", "arm64"}

func (a App) arches() []string {
	if len(a.Arches) > 0 {
		return a.Arches
	}
	return defaultArches
}

// catalog is ordered as shown to the user: by category, then roughly by how
// foundational the app is.
var catalog = finalize([]App{
	metalLBApp,
	metricsServerApp,
	certManagerApp,
	localPathApp,
	nfsApp,
	longhornApp,
	fluxApp,
	argoCDApp,
	crossplaneApp,
	cnpgApp,
	valkeyApp,
	openBaoApp,
	keycloakApp,
	trivyOperatorApp,
	kubePrometheusStackApp,
	k8sDocksideApp,
	nyanCatApp,
})

// finalize derives each app's install methods from what it provides.
func finalize(apps []App) []App {
	for i := range apps {
		a := &apps[i]
		if len(a.Methods) > 0 {
			continue
		}
		if a.chart != nil {
			a.Methods = append(a.Methods, cluster.MethodHelm)
		}
		if a.manifestURL != "" {
			a.Methods = append(a.Methods, cluster.MethodManifest)
		}
		if a.chart != nil && !a.noGitOps {
			a.Methods = append(a.Methods, cluster.MethodFlux, cluster.MethodArgo)
		}
	}
	return apps
}

// Catalog returns every installable app.
func Catalog() []App { return catalog }

// Lookup returns the catalog app called name (or one of its aliases).
func Lookup(name string) (App, bool) {
	for _, a := range catalog {
		if a.Name == name {
			return a, true
		}
		for _, alias := range a.Aliases {
			if alias == name {
				return a, true
			}
		}
	}
	return App{}, false
}

// ParseRequest parses the --app flag form name[:method][=host], e.g. "argocd",
// "argocd:manifest" or "argocd=cd.localtest.me", filling in the app's default
// method and host.
func ParseRequest(s string) (cluster.AppRequest, error) {
	nameMethod, host, _ := strings.Cut(s, "=")
	name, method, _ := strings.Cut(nameMethod, ":")
	return Resolve(cluster.AppRequest{Name: name, Method: cluster.InstallMethod(method), Host: host})
}

// Resolve checks a request against the catalog and fills in defaults. Aliases
// are replaced by the canonical name.
func Resolve(req cluster.AppRequest) (cluster.AppRequest, error) {
	app, ok := Lookup(req.Name)
	if !ok {
		return req, fmt.Errorf("unknown app %q (available: %s)", req.Name, strings.Join(names(), ", "))
	}
	req.Name = app.Name
	if req.Method == "" {
		req.Method, req.MethodDefaulted = app.Methods[0], true
	}
	if !supports(app.Methods, req.Method) {
		return req, fmt.Errorf("%s cannot be installed via %s (supported: %s)", app.Name, req.Method, MethodsText(app.Methods))
	}
	if req.Host != "" && app.Expose == nil {
		return req, fmt.Errorf("%s has nothing to expose, so it takes no hostname", app.Name)
	}
	if req.Host == "" && app.Expose != nil {
		req.Host = app.Expose.DefaultHost
	}
	if req.Host != "" && !cluster.ValidHostname(req.Host) {
		return req, fmt.Errorf("%s: %q is not a valid hostname", app.Name, req.Host)
	}
	return req, nil
}

func names() []string {
	out := make([]string, len(catalog))
	for i, a := range catalog {
		out[i] = a.Name
	}
	return out
}

// dependencies are the apps req needs installed first: the app's own
// Requires plus the GitOps controller its method hands the install to.
func dependencies(app App, method cluster.InstallMethod) []string {
	deps := append([]string(nil), app.Requires...)
	switch method {
	case cluster.MethodFlux:
		deps = append(deps, fluxApp.Name)
	case cluster.MethodArgo:
		deps = append(deps, argoCDApp.Name)
	}
	return deps
}

// Plan resolves requests and orders them so every app comes after what it
// depends on, adding missing dependencies (with their default method, marked
// Implicit so an already-installed one is skipped silently). added lists the
// dependencies that were not requested, for telling the user.
// storageRank orders storage apps first, by catalog position; every other
// app ranks after them, equally.
func storageRank(name string) int {
	for i, a := range catalog {
		if a.Name == name && a.Category == CategoryStorage {
			return i
		}
	}
	return len(catalog)
}

func Plan(reqs []cluster.AppRequest) (plan []cluster.AppRequest, added []string, err error) {
	requested := map[string]cluster.AppRequest{}
	var order []string
	for _, r := range reqs {
		r, err := Resolve(r)
		if err != nil {
			return nil, nil, err
		}
		if _, dup := requested[r.Name]; dup {
			return nil, nil, fmt.Errorf("app %q requested more than once", r.Name)
		}
		requested[r.Name] = r
		order = append(order, r.Name)
	}
	// Storage first (in catalog order: local-path before nfs, which needs a
	// default StorageClass itself): apps with volumes check for a default
	// StorageClass, so `kube-prometheus-stack local-path` must still work.
	slices.SortStableFunc(order, func(a, b string) int {
		return cmp.Compare(storageRank(a), storageRank(b))
	})

	state := map[string]int{} // 0 unvisited, 1 visiting, 2 done
	var visit func(name, by string) error
	visit = func(name, by string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("dependency cycle at %s", name)
		case 2:
			return nil
		}
		state[name] = 1
		req, ok := requested[name]
		if !ok {
			r, err := Resolve(cluster.AppRequest{Name: name})
			if err != nil {
				return fmt.Errorf("%s (required by %s): %w", name, by, err)
			}
			r.Implicit = true
			req = r
			added = append(added, fmt.Sprintf("%s (required by %s)", name, by))
		}
		app, _ := Lookup(name)
		for _, d := range dependencies(app, req.Method) {
			if err := visit(d, name); err != nil {
				return err
			}
		}
		state[name] = 2
		plan = append(plan, req)
		return nil
	}
	for _, name := range order {
		if err := visit(name, ""); err != nil {
			return nil, nil, err
		}
	}
	return plan, added, nil
}

// Preset is a named bundle of catalog apps.
type Preset struct {
	Name        string
	Description string
	Apps        []string
}

// presets only name catalog apps; TestPresets keeps them honest.
var presets = []Preset{
	{"observability", "metrics-server + kube-prometheus-stack (Prometheus, Alertmanager, Grafana)",
		[]string{"metrics-server", "kube-prometheus-stack"}},
	{"platform", "cert-manager, OpenBao, CloudNativePG, Keycloak, Trivy Operator",
		[]string{"cert-manager", "openbao", "cnpg", "keycloak", "trivy-operator"}},
	{"gitops", "Flux + Argo CD, with nyancat and k8sdockside as workloads",
		[]string{"flux", "argocd", "nyancat", "k8sdockside"}},
	{"data", "CloudNativePG (with a postgres cluster), Valkey, in-cluster NFS",
		[]string{"cnpg", "valkey", "nfs"}},
	{"storage", "local-path (default StorageClass) + in-cluster NFS (ReadWriteMany)",
		[]string{"local-path", "nfs"}},
	{"demo", "nyancat + k8sdockside",
		[]string{"nyancat", "k8sdockside"}},
}

// Presets returns every preset.
func Presets() []Preset { return presets }

// LookupPreset returns the preset called name.
func LookupPreset(name string) (Preset, bool) {
	for _, p := range presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// ExpandPresets turns preset names into app requests (default method and
// host), skipping apps already in existing.
func ExpandPresets(names []string, existing []cluster.AppRequest) ([]cluster.AppRequest, error) {
	have := map[string]bool{}
	for _, r := range existing {
		have[r.Name] = true
	}
	var out []cluster.AppRequest
	for _, n := range names {
		p, ok := LookupPreset(n)
		if !ok {
			all := make([]string, len(presets))
			for i, p := range presets {
				all[i] = p.Name
			}
			return nil, fmt.Errorf("unknown preset %q (available: %s)", n, strings.Join(all, ", "))
		}
		for _, a := range p.Apps {
			if have[a] {
				continue
			}
			have[a] = true
			r, err := Resolve(cluster.AppRequest{Name: a})
			if err != nil {
				return nil, fmt.Errorf("preset %s: %w", n, err)
			}
			out = append(out, r)
		}
	}
	return out, nil
}
