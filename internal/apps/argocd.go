package apps

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// Argo CD pins: chart 10.11.7 ships Argo CD v3.5.7.
const (
	argoCDVersion      = "v3.5.7"
	argoCDChartVersion = "10.11.7"
	argoCDNamespace    = "argocd"
)

var argoCD = App{
	Name:        "argocd",
	Description: "Argo CD " + argoCDVersion,
	Methods:     []cluster.InstallMethod{cluster.MethodHelm, cluster.MethodManifest},
	Namespace:   argoCDNamespace,
	Deployment:  "argocd-server",
	// The UI and the CLI (with --grpc-web) both work over the HTTP route.
	Expose: &Expose{
		DefaultHost: "argocd." + cluster.LocalDomain,
		Service:     "argocd-server",
		Port:        80,
		Protocol:    ProtocolHTTP,
	},
	install: installArgoCD,
	notes:   argoCDNotes,
}

// installArgoCD installs Argo CD with server.insecure=true: TLS terminates at
// the gateway, and argocd-server would otherwise redirect plain HTTP to HTTPS
// in a loop.
func installArgoCD(ctx context.Context, env *Env, method cluster.InstallMethod) error {
	switch method {
	case cluster.MethodHelm:
		return env.helmInstall(ctx, helmChart{
			Release:   "argocd",
			Chart:     "argo-cd",
			Repo:      "https://argoproj.github.io/argo-helm",
			Version:   argoCDChartVersion,
			Namespace: argoCDNamespace,
			Set:       []string{`configs.params.server\.insecure=true`},
		})
	case cluster.MethodManifest:
		if err := env.applyYAML(ctx, namespaceYAML(argoCDNamespace)); err != nil {
			return err
		}
		url := fmt.Sprintf("https://raw.githubusercontent.com/argoproj/argo-cd/%s/manifests/install.yaml", argoCDVersion)
		if err := env.kubectl(ctx, "apply", "--server-side", "--force-conflicts", "-n", argoCDNamespace, "-f", url); err != nil {
			return err
		}
		if err := env.kubectl(ctx, "patch", "configmap", "argocd-cmd-params-cm", "-n", argoCDNamespace,
			"--type", "merge", "-p", `{"data":{"server.insecure":"true"}}`); err != nil {
			return err
		}
		if err := env.kubectl(ctx, "rollout", "restart", "deployment/argocd-server", "-n", argoCDNamespace); err != nil {
			return err
		}
		return env.waitAvailable(ctx, argoCDNamespace, 5*time.Minute)
	default:
		return fmt.Errorf("unsupported method %q", method)
	}
}

func argoCDNotes(ctx context.Context, env *Env, host string) {
	if host != "" {
		fmt.Fprintf(env.Out, "   UI:       %s  ·  %s\n", env.URL("http", host), env.URL("https", host))
	} else {
		fmt.Fprintf(env.Out, "   UI:       http://localhost:8080  (after: kubectl --context %s port-forward -n argocd svc/argocd-server 8080:80)\n", env.KubeContext)
	}
	fmt.Fprintf(env.Out, "   Username: admin\n")
	if pw := argoCDPassword(ctx, env); pw != "" {
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

func argoCDPassword(ctx context.Context, env *Env) string {
	out, err := env.kubectlQuiet(ctx, "-n", argoCDNamespace, "get", "secret", "argocd-initial-admin-secret",
		"-o", "jsonpath={.data.password}")
	if err != nil {
		return ""
	}
	pw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return ""
	}
	return string(pw)
}
