package apps

import (
	"context"
	"fmt"
	"strings"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// Argo CD pins: chart 10.11.7 ships Argo CD v3.5.7.
const (
	argoCDVersion      = "v3.5.7"
	argoCDChartVersion = "10.11.7"
	argoCDNamespace    = "argocd"
)

var argoCDApp = App{
	Name:        "argocd",
	Description: "Argo CD " + argoCDVersion,
	Category:    CategoryGitOps,
	Namespace:   argoCDNamespace,
	Detect:      []string{"deployment/argocd-server"},
	CRD:         argoApplicationCRD,
	// Argo CD cannot install itself through the argo method, and Flux
	// managing Argo CD is not a setup worth offering.
	Methods: []cluster.InstallMethod{cluster.MethodHelm, cluster.MethodManifest},
	// The UI and the CLI (with --grpc-web) both work over the HTTP route.
	Expose: &Expose{
		DefaultHost: "argocd." + cluster.LocalDomain,
		Service:     "argocd-server",
		Port:        80,
		Protocol:    ProtocolHTTP,
		LocalPort:   8080,
	},
	chart: &helmChart{
		Release:   "argocd",
		Chart:     "argo-cd",
		Repo:      "https://argoproj.github.io/argo-helm",
		Version:   argoCDChartVersion,
		Namespace: argoCDNamespace,
	},
	values:            argoCDValues,
	manifestURL:       argoCDManifestURL,
	manifestNamespace: argoCDNamespace,
	afterManifest:     argoCDAfterManifest,
	// The repo server pulls charts for the argo method.
	configure: func(ctx context.Context, env *Env) error {
		return env.trustCABundle(ctx, argoCDNamespace, "argocd-repo-server")
	},
	notes: argoCDNotes,
}

// argoCDValues runs argocd-server with server.insecure=true: TLS terminates at
// the gateway, and argocd-server would otherwise redirect plain HTTP to HTTPS
// in a loop.
func argoCDValues(_ context.Context, env *Env, _ string) (map[string]any, error) {
	values := map[string]any{
		"configs": map[string]any{"params": map[string]any{"server.insecure": true}},
	}
	if env.production() {
		// Stateless components scale out; the application controller is
		// sharded rather than replicated, and redis-ha needs 3+ nodes with
		// hard anti-affinity, so both stay single.
		values["server"] = map[string]any{"replicas": 2}
		values["repoServer"] = map[string]any{"replicas": 2}
		values["applicationSet"] = map[string]any{"replicas": 2}
	}
	return values, nil
}

// argoCDManifestURL has no namespace in it; it is applied into argocd.
const argoCDManifestURL = "https://raw.githubusercontent.com/argoproj/argo-cd/" + argoCDVersion + "/manifests/install.yaml"

// argoCDAfterManifest turns on server.insecure for the manifest install (the
// chart sets it through values): TLS terminates at the gateway, and
// argocd-server would otherwise redirect plain HTTP to HTTPS in a loop.
func argoCDAfterManifest(ctx context.Context, env *Env) error {
	if err := env.kubectl(ctx, "patch", "configmap", "argocd-cmd-params-cm", "-n", argoCDNamespace,
		"--type", "merge", "-p", `{"data":{"server.insecure":"true"}}`); err != nil {
		return err
	}
	return env.kubectl(ctx, "rollout", "restart", "deployment/argocd-server", "-n", argoCDNamespace)
}

func argoCDNotes(ctx context.Context, env *Env, host string) {
	fmt.Fprintf(env.Out, "   Username: admin\n")
	if pw := env.secretValue(ctx, argoCDNamespace, "argocd-initial-admin-secret", "password"); pw != "" {
		fmt.Fprintf(env.Out, "   Password: %s\n", pw)
	} else {
		// Argo CD recommends deleting the initial secret once the password
		// has been changed, so its absence is normal.
		fmt.Fprintf(env.Out, "   Password: (argocd-initial-admin-secret is gone — use the password you set; reset: https://argo-cd.readthedocs.io/en/stable/faq/#i-forgot-the-admin-password-how-do-i-reset-it)\n")
	}
	if host != "" {
		addr := strings.TrimPrefix(env.URL("http", host), "http://")
		fmt.Fprintf(env.Out, "   CLI:      argocd login %s --grpc-web --plaintext --username admin\n", addr)
	}
}
