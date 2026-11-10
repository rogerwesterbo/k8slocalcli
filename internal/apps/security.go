package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// OpenBao pins: chart 0.30.7 ships OpenBao v2.7.2.
const (
	openBaoVersion      = "v2.7.2"
	openBaoChartVersion = "0.30.7"
	openBaoNamespace    = "openbao"
	openBaoDevToken     = "root" // #nosec G101 -- the documented dev-mode root token, dev profile only
)

var openBaoApp = App{
	Name:        "openbao",
	Description: "OpenBao " + openBaoVersion + " (secrets management)",
	Category:    CategorySecurity,
	Namespace:   openBaoNamespace,
	Detect:      []string{"statefulset/openbao"},
	Expose: &Expose{
		DefaultHost: "openbao.localtest.me",
		Service:     "openbao",
		Port:        8200,
		Protocol:    ProtocolHTTP,
	},
	preflight: requireStorageForProduction,
	chart: &helmChart{
		Release:   "openbao",
		Chart:     "openbao",
		Repo:      "https://openbao.github.io/openbao-helm",
		Version:   openBaoChartVersion,
		Namespace: openBaoNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		server := map[string]any{
			"dev": map[string]any{"enabled": true, "devRootToken": openBaoDevToken},
		}
		if env.production() {
			// A real server with file storage on a PV. It starts sealed and
			// uninitialised; the probe counts that as ready (it is up and
			// waiting for an operator) so the rollout can be verified, and
			// the postcheck reports the seal status.
			server = map[string]any{
				"standalone":  map[string]any{"enabled": true},
				"dataStorage": map[string]any{"enabled": true, "size": "10Gi"},
				"readinessProbe": map[string]any{
					"enabled": true,
					"path":    "/v1/sys/health?standbyok=true&sealedcode=204&uninitcode=204",
				},
				"resources": map[string]any{
					"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
					"limits":   map[string]any{"memory": "512Mi"},
				},
			}
		}
		return map[string]any{
			"server": server,
			"ui":     map[string]any{"enabled": true},
		}, nil
	},
	check: func(ctx context.Context, env *Env) error {
		st, err := openBaoStatus(ctx, env)
		if err != nil {
			return err
		}
		if openBaoDevMode(ctx, env) && (st.Sealed || !st.Initialized) {
			return fmt.Errorf("dev server should be initialised and unsealed, got initialized=%t sealed=%t", st.Initialized, st.Sealed)
		}
		return nil
	},
	notes: openBaoNotes,
}

type openBaoSealStatus struct {
	Initialized bool `json:"initialized"`
	Sealed      bool `json:"sealed"`
}

// openBaoStatus runs `bao status` in the server pod. It exits 2 when sealed,
// but still prints the JSON, so the output is parsed regardless of the exit.
func openBaoStatus(ctx context.Context, env *Env) (openBaoSealStatus, error) {
	var st openBaoSealStatus
	var out string
	err := retry(ctx, remaining(ctx, time.Minute), 5*time.Second, func(ctx context.Context) error {
		o, err := env.kubectlQuiet(ctx, "exec", "-n", openBaoNamespace, "openbao-0", "--", "bao", "status", "-format=json")
		out = o
		if strings.TrimSpace(o) != "" {
			return nil
		}
		return err
	})
	if err != nil {
		return st, fmt.Errorf("bao status: %w", err)
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return st, fmt.Errorf("parsing bao status: %w", err)
	}
	return st, nil
}

// openBaoDevMode reports whether the running server is the in-memory dev
// server. Read from the StatefulSet rather than the profile, because `info`
// does not know which profile an app was installed with.
func openBaoDevMode(ctx context.Context, env *Env) bool {
	out, err := env.kubectlQuiet(ctx, "get", "statefulset", "openbao", "-n", openBaoNamespace,
		"-o", "jsonpath={.spec.template.spec.containers[0].args}")
	return err == nil && strings.Contains(out, "server -dev")
}

func openBaoNotes(ctx context.Context, env *Env, _ string) {
	if openBaoDevMode(ctx, env) {
		fmt.Fprintf(env.Out, "   Token:    %s (dev server: in-memory, unsealed, data is lost on restart)\n", openBaoDevToken)
		return
	}
	st, err := openBaoStatus(ctx, env)
	switch {
	case err != nil:
		fmt.Fprintf(env.Out, "   Status:   unknown (%v)\n", err)
	case !st.Initialized:
		fmt.Fprintf(env.Out, "   Status:   not initialised — keep the output somewhere safe:\n")
		fmt.Fprintf(env.Out, "             kubectl --context %s exec -it -n %s openbao-0 -- bao operator init\n", env.KubeContext, openBaoNamespace)
		fmt.Fprintf(env.Out, "             then unseal with 3 of the keys: ... -- bao operator unseal\n")
	case st.Sealed:
		fmt.Fprintf(env.Out, "   Status:   sealed — unseal: kubectl --context %s exec -it -n %s openbao-0 -- bao operator unseal\n", env.KubeContext, openBaoNamespace)
	default:
		fmt.Fprintf(env.Out, "   Status:   initialised and unsealed\n")
	}
}

// Keycloak pins (codecentric keycloakx; chart 7.4.0 ships Keycloak 26.8.1).
const (
	keycloakVersion      = "26.8.1"
	keycloakChartVersion = "7.4.0"
	keycloakNamespace    = "keycloak"
	keycloakAdminSecret  = "keycloak-admin" // #nosec G101 -- a Secret name, not a credential
	keycloakDB           = "keycloak-db"
)

var keycloakApp = App{
	Name:        "keycloak",
	Description: "Keycloak " + keycloakVersion + " (on a CloudNativePG database)",
	Category:    CategorySecurity,
	Namespace:   keycloakNamespace,
	Detect:      []string{"statefulset/keycloak-keycloakx"},
	Requires:    []string{"cnpg"},
	Timeout:     15 * time.Minute,
	Expose: &Expose{
		DefaultHost: "keycloak.localtest.me",
		Service:     "keycloak-keycloakx-http",
		Port:        80,
		Protocol:    ProtocolHTTP,
		LocalPort:   8082,
	},
	preflight: requireDefaultStorageClass,
	prepare: func(ctx context.Context, env *Env) error {
		if err := env.ensurePasswordSecret(ctx, keycloakNamespace, keycloakAdminSecret, "admin"); err != nil {
			return err
		}
		// CloudNativePG stores the owner's credentials in keycloak-db-app.
		return env.ensurePostgres(ctx, keycloakNamespace, keycloakDB, "keycloak", "keycloak")
	},
	chart: &helmChart{
		Release:   "keycloak",
		Chart:     "keycloakx",
		Repo:      "https://codecentric.github.io/helm-charts",
		Version:   keycloakChartVersion,
		Namespace: keycloakNamespace,
	},
	values: keycloakValues,
	notes: func(ctx context.Context, env *Env, _ string) {
		printCredentials(ctx, env, keycloakNamespace, keycloakAdminSecret, "username", "password")
		fmt.Fprintf(env.Out, "   Note:     the bootstrap admin is temporary — create a permanent admin in the master realm\n")
	},
}

func keycloakValues(_ context.Context, env *Env, _ string) (map[string]any, error) {
	// start-dev: no hostname checks, local caches. start: the production
	// server, behind the gateway (TLS terminates there, so HTTP is enabled
	// and X-Forwarded-* headers are trusted for the external URL).
	args := []any{"start-dev"}
	replicas := 1
	cache := map[string]any{"stack": "custom"}
	if env.production() {
		args = []any{"start", "--http-enabled=true", "--hostname-strict=false"}
		replicas = 2
		cache = map[string]any{"stack": "default"}
	}
	extraEnv := fmt.Sprintf(`- name: KC_BOOTSTRAP_ADMIN_USERNAME
  valueFrom:
    secretKeyRef:
      name: %[1]s
      key: username
- name: KC_BOOTSTRAP_ADMIN_PASSWORD
  valueFrom:
    secretKeyRef:
      name: %[1]s
      key: password
`, keycloakAdminSecret)
	return map[string]any{
		"command":  []any{"/opt/keycloak/bin/kc.sh"},
		"args":     args,
		"replicas": replicas,
		"http":     map[string]any{"relativePath": "/"},
		"proxy":    map[string]any{"enabled": true, "mode": "xforwarded"},
		"cache":    cache,
		"extraEnv": extraEnv,
		"database": map[string]any{
			"vendor":            "postgres",
			"hostname":          keycloakDB + "-rw",
			"port":              5432,
			"database":          "keycloak",
			"username":          "keycloak",
			"existingSecret":    keycloakDB + "-app",
			"existingSecretKey": "password",
		},
	}, nil
}

// Trivy Operator pins: chart 0.38.1 ships operator 0.36.1.
const (
	trivyVersion      = "0.36.1"
	trivyChartVersion = "0.38.1"
	trivyNamespace    = "trivy-system"
)

var trivyOperatorApp = App{
	Name:        "trivy-operator",
	Aliases:     []string{"trivy"},
	Description: "Trivy Operator " + trivyVersion + " (vulnerability and config scanning)",
	Category:    CategorySecurity,
	Namespace:   trivyNamespace,
	Detect:      []string{"deployment/trivy-operator"},
	Privileged:  true, // node-collector scan jobs mount node paths
	Workloads:   []string{"deployment/trivy-operator"},
	CRD:         "vulnerabilityreports.aquasecurity.github.io",
	chart: &helmChart{
		Release:   "trivy-operator",
		Chart:     "trivy-operator",
		Repo:      "https://aquasecurity.github.io/helm-charts/",
		Version:   trivyChartVersion,
		Namespace: trivyNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		values := map[string]any{}
		if !env.production() {
			// Gentle on a laptop: few concurrent scan jobs, and only
			// vulnerabilities that have a fix.
			values["operator"] = map[string]any{"scanJobsConcurrentLimit": 2}
			values["trivy"] = map[string]any{"ignoreUnfixed": true}
		}
		return values, nil
	},
	manifestURL: "https://raw.githubusercontent.com/aquasecurity/trivy-operator/v" + trivyVersion + "/deploy/static/trivy-operator.yaml",
	notes: func(_ context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   Reports:  kubectl --context %s get vulnerabilityreports,configauditreports -A  (scans take a few minutes)\n", env.KubeContext)
	},
}
