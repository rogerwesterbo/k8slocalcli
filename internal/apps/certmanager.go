package apps

import (
	"context"
	"fmt"
	"time"
)

// certManagerApp is cert-manager as a catalog app. SetupExposure installs it
// through the same entry when verified TLS is chosen, so both paths install
// the same pinned version with the same settings.
var certManagerApp = App{
	Name:        "cert-manager",
	Description: "cert-manager " + certManagerVersion,
	Category:    CategorySecurity,
	Namespace:   certManagerNamespace,
	Detect:      []string{"deployment/cert-manager"},
	CRD:         "certificates.cert-manager.io",
	chart: &helmChart{
		Release:   "cert-manager",
		Chart:     certManagerChart,
		Version:   certManagerVersion,
		Namespace: certManagerNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		values := map[string]any{"crds": map[string]any{"enabled": true}}
		if env.production() {
			values["replicaCount"] = 2
			values["webhook"] = map[string]any{"replicaCount": 2}
			values["cainjector"] = map[string]any{"replicaCount": 2}
			values["podDisruptionBudget"] = map[string]any{"enabled": true}
		}
		return values, nil
	},
	manifestURL: "https://github.com/cert-manager/cert-manager/releases/download/" + certManagerVersion + "/cert-manager.yaml",
	check:       certManagerCheck,
	notes: func(_ context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   Issuers:  kubectl --context %s get clusterissuers,issuers -A\n", env.KubeContext)
	},
}

// certManagerCheck proves the admission webhook answers by validating a
// self-signed ClusterIssuer server-side (dry run, nothing is created). The
// webhook can reject requests for a short while after it reports Available.
func certManagerCheck(ctx context.Context, env *Env) error {
	issuer := `apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: k8slocalcli-smoke
spec:
  selfSigned: {}
`
	return retry(ctx, remaining(ctx, 2*time.Minute), 5*time.Second, func(ctx context.Context) error {
		return env.applyYAMLDryRun(ctx, issuer)
	})
}
