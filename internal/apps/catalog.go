package apps

import (
	"context"
	"fmt"
	"strings"

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
}

// App is a catalog application.
type App struct {
	Name        string
	Description string
	// Methods lists the supported install methods; the first is the default.
	// The user is only asked to choose when there is more than one.
	Methods   []cluster.InstallMethod
	Namespace string
	// Deployment is a Deployment in Namespace whose presence means the app is
	// installed; `info` uses it to find apps in a running cluster.
	Deployment string
	// Expose is nil for apps with nothing to reach from the host.
	Expose *Expose

	install func(ctx context.Context, env *Env, method cluster.InstallMethod) error
	// notes prints how to use the app once installed (URLs, credentials, CLI
	// login). host is the routed hostname, or "" when the app is not exposed.
	notes func(ctx context.Context, env *Env, host string)
}

// catalog is ordered as shown to the user.
var catalog = []App{argoCD}

// Catalog returns every installable app.
func Catalog() []App { return catalog }

// Lookup returns the catalog app called name.
func Lookup(name string) (App, bool) {
	for _, a := range catalog {
		if a.Name == name {
			return a, true
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

// Resolve checks a request against the catalog and fills in defaults.
func Resolve(req cluster.AppRequest) (cluster.AppRequest, error) {
	app, ok := Lookup(req.Name)
	if !ok {
		return req, fmt.Errorf("unknown app %q (available: %s)", req.Name, strings.Join(names(), ", "))
	}
	if req.Method == "" {
		req.Method = app.Methods[0]
	}
	if !supports(app.Methods, req.Method) {
		return req, fmt.Errorf("%s cannot be installed via %s (supported: %s)", app.Name, req.Method, methodsText(app.Methods))
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

// Install installs one resolved request and, when the cluster is exposed,
// routes its hostname through the shared Gateway.
func Install(ctx context.Context, env *Env, req cluster.AppRequest) error {
	req, err := Resolve(req)
	if err != nil {
		return err
	}
	app, _ := Lookup(req.Name)

	route := app.Expose != nil && env.Exposed && GatewayInstalled(ctx, env)
	title := fmt.Sprintf("Installing %s via %s — pulling images, waiting for pods", app.Description, req.Method)
	if err := env.step(ctx, title, app.Namespace, func(se *Env) error {
		if err := app.install(ctx, se, req.Method); err != nil {
			return err
		}
		if route {
			return exposeApp(ctx, se, app, req.Host)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("installing %s: %w", app.Name, err)
	}

	routed := ""
	if app.Expose != nil {
		switch {
		case !env.Exposed:
			fmt.Fprintf(env.Out, "ℹ️  %s is not exposed: cluster %q publishes no gateway ports (created without host exposure, or by an older k8slocalcli). Use: kubectl --context %s port-forward -n %s svc/%s <local-port>:%d\n",
				app.Name, env.Name, env.KubeContext, app.Namespace, app.Expose.Service, app.Expose.Port)
		case !GatewayInstalled(ctx, env):
			fmt.Fprintf(env.Out, "⚠️  %s is not exposed: no Gateway %s/%s found in cluster %q\n", app.Name, GatewayNamespace, GatewayName, env.Name)
		default:
			routed = req.Host
			checkHostResolves(ctx, env, req.Host)
		}
	}

	if app.notes != nil {
		app.notes(ctx, env, routed)
	}
	fmt.Fprintf(env.Out, "✅ %s installed  (show URLs and credentials again: k8slocalcli info %s)\n", app.Name, env.Name)
	return nil
}

func exposeApp(ctx context.Context, env *Env, app App, host string) error {
	route := httpRouteYAML
	if app.Expose.Protocol == ProtocolGRPC {
		route = grpcRouteYAML
	}
	manifest := route(app.Name, app.Namespace, host, app.Expose.Service, app.Expose.Port)
	if err := env.applyYAML(ctx, manifest); err != nil {
		return fmt.Errorf("routing %s to %s: %w", host, app.Name, err)
	}
	return nil
}
