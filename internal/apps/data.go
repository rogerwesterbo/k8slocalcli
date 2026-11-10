package apps

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CloudNativePG pins: chart 0.29.1 ships operator 1.30.1.
const (
	cnpgVersion      = "1.30.1"
	cnpgChartVersion = "0.29.1"
	cnpgNamespace    = "cnpg-system"
	cnpgClusterCRD   = "clusters.postgresql.cnpg.io"
	// The sample database cluster installed with the operator.
	postgresNamespace = "postgres"
	postgresCluster   = "postgres"
)

var cnpgApp = App{
	Name:        "cnpg",
	Aliases:     []string{"cloudnative-pg", "postgres"},
	Description: "CloudNativePG " + cnpgVersion + " + a postgres cluster",
	Category:    CategoryData,
	Namespace:   cnpgNamespace,
	// The chart names the deployment after the release; the manifest does not.
	Detect:         []string{"deployment/cnpg-cloudnative-pg", "deployment/cnpg-controller-manager"},
	CRD:            cnpgClusterCRD,
	preflight:      requireDefaultStorageClass,
	DataNamespaces: []string{postgresNamespace},
	chart: &helmChart{
		Release:   "cnpg",
		Chart:     "cloudnative-pg",
		Repo:      "https://cloudnative-pg.github.io/charts",
		Version:   cnpgChartVersion,
		Namespace: cnpgNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		values := map[string]any{}
		if env.production() {
			values["replicaCount"] = 2
		}
		return values, nil
	},
	manifestURL: "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-" +
		cnpgVersion[:strings.LastIndex(cnpgVersion, ".")] + "/releases/cnpg-" + cnpgVersion + ".yaml",
	configure: func(ctx context.Context, env *Env) error {
		return env.ensurePostgres(ctx, postgresNamespace, postgresCluster, "app", "app")
	},
	notes: func(_ context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   Postgres: cluster %s/%s · service %s-rw.%s.svc:5432 · database app\n",
			postgresNamespace, postgresCluster, postgresCluster, postgresNamespace)
		fmt.Fprintf(env.Out, "   Login:    secret %s/%s-app (username, password, uri)\n", postgresNamespace, postgresCluster)
		fmt.Fprintf(env.Out, "   psql:     kubectl --context %s exec -it -n %s %s-1 -- psql app\n", env.KubeContext, postgresNamespace, postgresCluster)
	},
}

// ensurePostgres creates a CloudNativePG Cluster (1 instance for dev, 3 for
// production) with one database and owner, and waits until it is Ready.
// CloudNativePG then keeps the owner's credentials in Secret <name>-app.
func (e *Env) ensurePostgres(ctx context.Context, ns, name, database, owner string) error {
	instances, size := 1, "1Gi"
	if e.production() {
		instances, size = 3, "10Gi"
	}
	manifest := namespaceYAML(ns) + fmt.Sprintf(`---
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  instances: %[3]d
  storage:
    size: %[4]s
  bootstrap:
    initdb:
      database: %[5]s
      owner: %[6]s
`, name, ns, instances, size, database, owner)
	// The operator's webhook rejects requests until it is serving.
	if err := e.applyYAMLRetry(ctx, manifest, remaining(ctx, 2*time.Minute)); err != nil {
		return fmt.Errorf("creating postgres cluster %s/%s: %w", ns, name, err)
	}
	if err := e.kubectl(ctx, "wait", "--for=condition=Ready", "cluster.postgresql.cnpg.io/"+name,
		"-n", ns, "--timeout="+remaining(ctx, 5*time.Minute).String()); err != nil {
		return fmt.Errorf("postgres cluster %s/%s not ready: %w", ns, name, err)
	}
	return nil
}

// Valkey pins (the official valkey-io chart, not Bitnami's).
const (
	valkeyChartVersion = "0.12.1"
	valkeyNamespace    = "valkey"
	valkeyUsersSecret  = "valkey-users" // #nosec G101 -- a Secret name, not a credential
)

var valkeyApp = App{
	Name:        "valkey",
	Description: "Valkey (chart " + valkeyChartVersion + ")",
	Category:    CategoryData,
	Namespace:   valkeyNamespace,
	// A Deployment standalone, a StatefulSet with replicas; the Service is
	// there either way.
	Detect:    []string{"service/valkey"},
	preflight: requireStorageForProduction,
	prepare: func(ctx context.Context, env *Env) error {
		if !env.production() {
			return nil
		}
		pw, err := randomPassword(18)
		if err != nil {
			return err
		}
		return env.ensureSecret(ctx, valkeyNamespace, valkeyUsersSecret, map[string]string{"default": pw})
	},
	chart: &helmChart{
		Release:   "valkey",
		Chart:     "valkey",
		Repo:      "https://valkey.io/valkey-helm/",
		Version:   valkeyChartVersion,
		Namespace: valkeyNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		values := map[string]any{}
		if env.production() {
			values["replica"] = map[string]any{
				"enabled":     true,
				"replicas":    2,
				"persistence": map[string]any{"size": "8Gi"},
			}
			values["auth"] = map[string]any{
				"enabled":             true,
				"usersExistingSecret": valkeyUsersSecret,
				"aclUsers": map[string]any{
					"default": map[string]any{"permissions": "~* &* +@all"},
				},
			}
		}
		return values, nil
	},
	check: func(ctx context.Context, env *Env) error {
		args := []string{"exec", "-n", valkeyNamespace, "svc/valkey", "--", "valkey-cli"}
		if env.exists(ctx, valkeyNamespace, "secret/"+valkeyUsersSecret) {
			args = append(args, "--no-auth-warning", "-a", env.secretValue(ctx, valkeyNamespace, valkeyUsersSecret, "default"))
		}
		out, err := env.kubectlQuiet(ctx, append(args, "ping")...)
		if err != nil {
			return fmt.Errorf("valkey-cli ping: %w", err)
		}
		if strings.TrimSpace(out) != "PONG" {
			return fmt.Errorf("valkey-cli ping answered %q, want PONG", strings.TrimSpace(out))
		}
		return nil
	},
	notes: func(ctx context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   Service:  valkey.%s.svc:6379\n", valkeyNamespace)
		if env.exists(ctx, valkeyNamespace, "secret/"+valkeyUsersSecret) {
			fmt.Fprintf(env.Out, "   Password: user default, key \"default\" in secret %s/%s · replicas read via valkey-read\n", valkeyNamespace, valkeyUsersSecret)
		}
		fmt.Fprintf(env.Out, "   CLI:      kubectl --context %s exec -it -n %s svc/valkey -- valkey-cli\n", env.KubeContext, valkeyNamespace)
	},
}
