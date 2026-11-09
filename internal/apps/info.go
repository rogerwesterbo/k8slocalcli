package apps

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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
		if app.notes != nil {
			app.notes(ctx, env, host)
		}
		if host != "" {
			checkHostResolves(ctx, env, host)
		}
	}
	if !found {
		fmt.Fprintf(out, "\nApps:     none installed (see: k8slocalcli app list)\n")
	}
}

func appInstalled(ctx context.Context, env *Env, app App) bool {
	if app.Deployment == "" {
		return false
	}
	_, err := env.kubectlQuiet(ctx, "get", "deployment", app.Deployment, "-n", app.Namespace, "-o", "name")
	return err == nil
}

// routedHost returns the hostname the app's route serves, or "".
func routedHost(ctx context.Context, env *Env, app App) string {
	if app.Expose == nil {
		return ""
	}
	kind := "httproute"
	if app.Expose.Protocol == ProtocolGRPC {
		kind = "grpcroute"
	}
	host, err := env.kubectlQuiet(ctx, "get", kind, app.Name, "-n", app.Namespace,
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
