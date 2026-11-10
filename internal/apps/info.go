package apps

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// ShowInfo prints how to reach a running cluster's gateway and installed apps,
// with their credentials. Everything is read from the cluster and the container
// engine at call time rather than stored, so it stays correct after passwords
// are changed, apps are re-routed, or the cluster has been running for days.
func ShowInfo(ctx context.Context, env *Env) {
	out := env.Out

	if env.Exposed && GatewayInstalled(ctx, env) {
		fmt.Fprintf(out, "Gateway:  %s · %s\n", gatewayAPIText(ctx, env), tlsText(ctx, env))
		fmt.Fprintf(out, "          %s  ·  %s\n",
			env.URL("http", "<app>."+cluster.LocalDomain), env.URL("https", "<app>."+cluster.LocalDomain))
	} else if env.Exposed {
		fmt.Fprintf(out, "Gateway:  host ports %d/%d are published, but no Gateway %s/%s is installed\n",
			env.HTTPPort, env.HTTPSPort, GatewayNamespace, GatewayName)
	} else {
		fmt.Fprintf(out, "Gateway:  none — the cluster publishes no gateway ports; use kubectl port-forward\n")
	}

	found := false
	for _, app := range Catalog() {
		if !appInstalled(ctx, env, app) {
			continue
		}
		if !found {
			fmt.Fprintf(out, "\nApps:\n")
			found = true
		}
		fmt.Fprintf(out, "\n  %s (%s)\n", app.Name, app.Description)
		host := ""
		if env.Exposed {
			host = routedHost(ctx, env, app)
		}
		printAccess(ctx, env, app, host)
		if host != "" {
			checkHostResolves(ctx, env, host)
		}
	}
	if !found {
		fmt.Fprintf(out, "\nApps:     none installed (see: k8slocalcli app list)\n")
	}
}

// routedHost returns the hostname the app's route serves, or "".
func routedHost(ctx context.Context, env *Env, app App) string {
	if app.Expose == nil {
		return ""
	}
	host, err := env.kubectlQuiet(ctx, "get", app.Expose.routeKind(), app.Name, "-n", app.Namespace,
		"-o", "jsonpath={.spec.hostnames[0]}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(host)
}

// gatewayAPIText describes the installed Gateway API bundle from its CRD
// annotations, e.g. "Gateway API v1.6.1 (standard)".
func gatewayAPIText(ctx context.Context, env *Env) string {
	out, err := env.kubectlQuiet(ctx, "get", "crd", "gateways.gateway.networking.k8s.io", "-o",
		`go-template={{ index .metadata.annotations "gateway.networking.k8s.io/bundle-version" }} {{ index .metadata.annotations "gateway.networking.k8s.io/channel" }}`)
	version, channel, ok := strings.Cut(strings.TrimSpace(out), " ")
	if err != nil || !ok {
		return "Gateway API"
	}
	return fmt.Sprintf("Gateway API %s (%s)", version, channel)
}

// tlsText reports whether the gateway certificate is cert-manager issued
// (verified) or self-signed.
func tlsText(ctx context.Context, env *Env) string {
	if _, err := env.kubectlQuiet(ctx, "get", "certificates.cert-manager.io", TLSSecretName,
		"-n", GatewayNamespace, "-o", "name"); err == nil {
		if dir, err := caDir(); err == nil {
			return "verified HTTPS (trust " + filepath.Join(dir, "ca.crt") + ")"
		}
		return "verified HTTPS"
	}
	return "self-signed HTTPS (browsers warn)"
}

// InstalledApps reports which catalog apps are present in the cluster (by
// their Detect resources).
func InstalledApps(ctx context.Context, env *Env) map[string]bool {
	out := map[string]bool{}
	for _, a := range scanCatalog(ctx, func(ctx context.Context, a App) bool { return appInstalled(ctx, env, a) }) {
		out[a.Name] = true
	}
	return out
}

// Inventory returns every catalog app present in the cluster with how it was
// installed (InstalledByOthers included, so callers can show why it cannot be
// removed), in catalog order.
func Inventory(ctx context.Context, env *Env) []Removal {
	installs := map[string]Installation{}
	var mu sync.Mutex
	found := scanCatalog(ctx, func(ctx context.Context, a App) bool {
		inst := DetectInstallation(ctx, env, a)
		if inst.State == NotInstalled {
			return false
		}
		mu.Lock()
		installs[a.Name] = inst
		mu.Unlock()
		return true
	})
	out := make([]Removal, len(found))
	for i, a := range found {
		out[i] = Removal{App: a, Installation: installs[a.Name]}
	}
	return out
}

// scanCatalog runs match for every catalog app concurrently (each one is a
// few kubectl calls) and returns the matching apps in catalog order.
func scanCatalog(ctx context.Context, match func(ctx context.Context, a App) bool) []App {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	hit := make([]bool, len(catalog))
	var wg sync.WaitGroup
	for i, a := range catalog {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hit[i] = match(ctx, a)
		}()
	}
	wg.Wait()
	var out []App
	for i, a := range catalog {
		if hit[i] {
			out = append(out, a)
		}
	}
	return out
}
