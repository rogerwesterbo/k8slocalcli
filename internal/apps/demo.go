package apps

import (
	"context"
	"fmt"
	"strings"
)

// k8sdockside and nyan-cat are installed from their OCI charts without a pin:
// Helm takes the newest release, and the flux/argo methods resolve the newest
// version once at install time (resolveVersion).
const (
	k8sDocksideChart       = "oci://ghcr.io/k8sdockside/helm/k8sdockside"
	k8sDocksideNamespace   = "k8sdockside"
	k8sDocksideAdminSecret = "k8sdockside-admin" // #nosec G101 -- a Secret name, not a credential
	nyanCatChart           = "oci://ghcr.io/rogerwesterbo/helm/nyan-cat"
	nyanCatNamespace       = "nyan-cat"
)

var k8sDocksideApp = App{
	Name:        "k8sdockside",
	Aliases:     []string{"dockside"},
	Description: "k8sdockside (latest, Kubernetes dashboard)",
	Category:    CategoryDemo,
	Namespace:   k8sDocksideNamespace,
	Detect:      []string{"deployment/k8sdockside"},
	Expose: &Expose{
		DefaultHost: "k8sdockside.localtest.me",
		Service:     "k8sdockside",
		Port:        80,
		Protocol:    ProtocolHTTP,
		LocalPort:   8083,
	},
	preflight: requireStorageForProduction,
	prepare: func(ctx context.Context, env *Env) error {
		return env.ensurePasswordSecret(ctx, k8sDocksideNamespace, k8sDocksideAdminSecret, "admin")
	},
	chart: &helmChart{
		Release:   "k8sdockside",
		Chart:     k8sDocksideChart,
		Namespace: k8sDocksideNamespace,
	},
	values: func(ctx context.Context, env *Env, host string) (map[string]any, error) {
		auth := map[string]any{
			// Skip the first-visitor setup page: the admin comes from our
			// generated Secret (only used while no user exists yet).
			"bootstrapAdmin": map[string]any{"existingSecret": map[string]any{
				"name": k8sDocksideAdminSecret, "usernameKey": "username", "passwordKey": "password",
			}},
		}
		if host != "" {
			// Behind the gateway: it sets X-Forwarded-*, and OAuth callbacks
			// need the external address.
			auth["publicURL"] = env.URL("https", host)
			auth["trustForwardedHeaders"] = true
		}
		// Everyone who signs in acts with the pod's ServiceAccount. dev is a
		// local throwaway cluster, so the dashboard may do everything;
		// production keeps the chart's read-only default. Either can be
		// changed with --values k8sdockside=<file> (rbac.mode, rbac.rules,
		// rbac.extraClusterRoles).
		rbac := "admin"
		if env.production() {
			rbac = "readonly"
		}
		values := map[string]any{"auth": auth, "rbac": map[string]any{"mode": rbac}}
		// Users, sessions and uploaded kubeconfigs live in /data. Keep them on
		// a volume of the default StorageClass whenever there is one (both
		// profiles); without one, the chart's emptyDir (lost on restart).
		// The production preflight already requires a default StorageClass.
		values["persistence"] = map[string]any{"enabled": env.defaultStorageClass(ctx) != ""}
		return values, nil
	},
	notes: func(ctx context.Context, env *Env, _ string) {
		printCredentials(ctx, env, k8sDocksideNamespace, k8sDocksideAdminSecret, "username", "password")
		if out, err := env.kubectlQuiet(ctx, "get", "pvc", "-n", k8sDocksideNamespace, "-o", "name"); err == nil && strings.TrimSpace(out) != "" {
			fmt.Fprintf(env.Out, "   Data:     on a persistent volume (%s)\n", strings.TrimSpace(strings.Split(out, "\n")[0]))
		} else {
			fmt.Fprintf(env.Out, "   Data:     emptyDir — users and uploaded kubeconfigs are lost when the pod restarts (no default StorageClass at install time)\n")
		}
		fmt.Fprintf(env.Out, "   Access:   %s — every signed-in user acts with these; change with --values k8sdockside=<file> (rbac.mode: readonly|admin|custom|none)\n",
			k8sDocksideAccess(ctx, env))
	},
}

var nyanCatApp = App{
	Name:        "nyancat",
	Aliases:     []string{"nyan-cat"},
	Description: "nyan-cat (latest, demo web app)",
	Category:    CategoryDemo,
	Namespace:   nyanCatNamespace,
	Detect:      []string{"deployment/nyan-cat"},
	Expose: &Expose{
		DefaultHost: "nyancat.localtest.me",
		Service:     "nyan-cat",
		Port:        8080,
		Protocol:    ProtocolHTTP,
		LocalPort:   8084,
	},
	chart: &helmChart{
		Release:   "nyan-cat",
		Chart:     nyanCatChart,
		Namespace: nyanCatNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		values := map[string]any{}
		if env.production() {
			values["replicaCount"] = 2
		}
		return values, nil
	},
}

// k8sDocksideAccess describes, from the live ClusterRoleBindings, what the
// k8sdockside ServiceAccount may do. Read from the cluster rather than the
// profile so `info` stays right after --values changes.
func k8sDocksideAccess(ctx context.Context, env *Env) string {
	out, err := env.kubectlQuiet(ctx, "get", "clusterrolebindings", "-o",
		`jsonpath={range .items[?(@.subjects[0].namespace=="`+k8sDocksideNamespace+`")]}{.subjects[0].name}{"="}{.roleRef.name}{"\n"}{end}`)
	if err != nil {
		return "unknown"
	}
	return describeRoles(roleNamesFor(out, "k8sdockside"))
}

// roleNamesFor picks the role names bound to sa from "sa=role" lines.
func roleNamesFor(lines, sa string) []string {
	var roles []string
	for _, l := range strings.Split(lines, "\n") {
		name, role, ok := strings.Cut(strings.TrimSpace(l), "=")
		if ok && name == sa {
			roles = append(roles, role)
		}
	}
	return roles
}

// describeRoles renders bound ClusterRoles for people: the chart's own role
// (readonly, or custom rules) and well-known built-ins get a plain label.
func describeRoles(roles []string) string {
	if len(roles) == 0 {
		return "none (no ClusterRoleBinding)"
	}
	labels := map[string]string{
		"cluster-admin": "cluster-admin (everything)",
		"k8sdockside":   "ClusterRole k8sdockside (read-only, or your custom rules)",
		"edit":          "edit",
		"view":          "view",
		"admin":         "admin",
	}
	parts := make([]string, len(roles))
	for i, r := range roles {
		parts[i] = r
		if l, ok := labels[r]; ok {
			parts[i] = l
		}
	}
	return strings.Join(parts, " + ")
}
