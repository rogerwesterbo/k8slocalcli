package apps

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// kube-prometheus-stack pins: chart 93.2.2 ships Prometheus Operator v0.95.2;
// bump kpsOperatorVersion with the chart (TestImageArchAudit checks they
// match). Avoid charts 93.0.0–93.1.1: their operator v0.95.1 image index
// labels every platform amd64, so arm64 nodes fail with "no match for platform
// in manifest".
const (
	kpsChartVersion    = "93.2.2"
	kpsOperatorVersion = "v0.95.2"
	kpsNamespace       = "monitoring"
	kpsRelease         = "kube-prometheus-stack"
)

var kubePrometheusStackApp = App{
	Name:        "kube-prometheus-stack",
	Aliases:     []string{"prometheus", "kps"},
	Description: "kube-prometheus-stack " + kpsChartVersion + " (Prometheus, Alertmanager, Grafana)",
	Category:    CategoryObservability,
	Namespace:   kpsNamespace,
	Detect:      []string{"deployment/" + kpsRelease + "-operator"},
	Privileged:  true, // node-exporter uses the host network, PID namespace and hostPath
	CRD:         "servicemonitors.monitoring.coreos.com",
	Timeout:     15 * time.Minute,
	Expose: &Expose{
		DefaultHost: "grafana.localtest.me",
		Service:     kpsRelease + "-grafana",
		Port:        80,
		Protocol:    ProtocolHTTP,
		LocalPort:   3000,
	},
	preflight: requireStorageForProduction,
	prepare:   kpsInstallCRDs,
	chart: &helmChart{
		Release:   kpsRelease,
		Chart:     "kube-prometheus-stack",
		Repo:      "https://prometheus-community.github.io/helm-charts",
		Version:   kpsChartVersion,
		Namespace: kpsNamespace,
	},
	values: kpsValues,
	notes: func(ctx context.Context, env *Env, _ string) {
		printCredentials(ctx, env, kpsNamespace, kpsRelease+"-grafana", "admin-user", "admin-password")
		fmt.Fprintf(env.Out, "   Prometheus: kubectl --context %s port-forward -n %s svc/%s-prometheus 9090:9090\n", env.KubeContext, kpsNamespace, kpsRelease)
	},
}

// kpsCRDsURL is the Prometheus Operator's CRD bundle for kpsOperatorVersion.
var kpsCRDsURL = "https://github.com/prometheus-operator/prometheus-operator/releases/download/" + kpsOperatorVersion + "/stripped-down-crds.yaml"

// kpsInstallCRDs installs the operator's CRDs and waits until they are
// Established, before the chart (installed with crds.enabled=false) starts
// the operator. The operator checks once, at startup, which CRDs exist and
// silently disables the controller of any that is not served yet: with Argo
// CD applying CRDs and operator together, a busy API server establishing the
// large prometheuses CRD late left Prometheus never created (Nov 2026).
//
// Earlier Argo CD installs tracked these CRDs as part of the Application;
// that annotation goes, or Argo CD (prune: true) would delete them — and
// every Prometheus with them — now that the chart no longer renders them.
func kpsInstallCRDs(ctx context.Context, env *Env) error {
	if err := env.applyURL(ctx, kpsCRDsURL); err != nil {
		return fmt.Errorf("installing Prometheus Operator CRDs: %w", err)
	}
	if err := env.kubectl(ctx, "wait", "--for=condition=Established", "-f", kpsCRDsURL,
		"--timeout="+remaining(ctx, 5*time.Minute).String()); err != nil {
		return fmt.Errorf("waiting for the Prometheus Operator CRDs to be established: %w", err)
	}
	// A safeguard: Argo CD 3.5 lists the chart's CRDs under the Application
	// but did not annotate them in tests; with label/annotation tracking
	// configured differently, prune would delete them.
	if err := env.kubectl(ctx, "annotate", "-f", kpsCRDsURL, "argocd.argoproj.io/tracking-id-"); err != nil {
		return fmt.Errorf("detaching Prometheus Operator CRDs from Argo CD: %w", err)
	}
	return kpsRestartStaleOperator(ctx, env)
}

// kpsRestartStaleOperator restarts an operator that started before the
// prometheuses CRD was established (an install from before kpsInstallCRDs):
// it runs without its Prometheus controller until restarted.
func kpsRestartStaleOperator(ctx context.Context, env *Env) error {
	established, err := env.kubectlQuiet(ctx, "get", "crd", "prometheuses.monitoring.coreos.com",
		"-o", `jsonpath={.status.conditions[?(@.type=="Established")].lastTransitionTime}`)
	if err != nil {
		return nil
	}
	starts, err := env.kubectlQuiet(ctx, "get", "pods", "-n", kpsNamespace, "-l", "app="+kpsRelease+"-operator",
		"-o", "jsonpath={.items[*].status.startTime}")
	if err != nil || !startedBefore(strings.Fields(starts), strings.TrimSpace(established)) {
		return nil
	}
	fmt.Fprintf(env.Out, "ℹ️  Restarting the Prometheus Operator: it started before its CRDs were ready and is not managing Prometheus\n")
	return env.kubectl(ctx, "rollout", "restart", "deployment/"+kpsRelease+"-operator", "-n", kpsNamespace)
}

// startedBefore reports whether any RFC 3339 start time is before ref.
func startedBefore(starts []string, ref string) bool {
	r, err := time.Parse(time.RFC3339, ref)
	if err != nil {
		return false
	}
	for _, s := range starts {
		if t, err := time.Parse(time.RFC3339, s); err == nil && t.Before(r) {
			return true
		}
	}
	return false
}

func kpsValues(_ context.Context, env *Env, _ string) (map[string]any, error) {
	prometheusSpec := map[string]any{
		"retention": "1d",
		// Pick up ServiceMonitors/PodMonitors/rules from every release, not
		// only ones labelled for this chart.
		"serviceMonitorSelectorNilUsesHelmValues": false,
		"podMonitorSelectorNilUsesHelmValues":     false,
		"ruleSelectorNilUsesHelmValues":           false,
		"probeSelectorNilUsesHelmValues":          false,
	}
	alertmanagerSpec := map[string]any{}
	grafana := map[string]any{}
	if env.production() {
		prometheusSpec["retention"] = "15d"
		prometheusSpec["resources"] = map[string]any{
			"requests": map[string]any{"cpu": "200m", "memory": "1Gi"},
		}
		prometheusSpec["storageSpec"] = pvcTemplate("20Gi")
		alertmanagerSpec["storage"] = pvcTemplate("2Gi")
		grafana["persistence"] = map[string]any{"enabled": true, "size": "5Gi"}
	}
	values := map[string]any{
		"crds":         map[string]any{"enabled": false}, // kpsInstallCRDs owns them
		"prometheus":   map[string]any{"prometheusSpec": prometheusSpec},
		"alertmanager": map[string]any{"alertmanagerSpec": alertmanagerSpec},
		"grafana":      grafana,
	}
	if !env.External() {
		// kind and Talos bind etcd, the scheduler, the controller-manager and
		// kube-proxy metrics to localhost, so these targets are always down
		// and only produce alerts.
		for _, k := range []string{"kubeEtcd", "kubeScheduler", "kubeControllerManager", "kubeProxy"} {
			values[k] = map[string]any{"enabled": false}
		}
	}
	return values, nil
}

// pvcTemplate is a Prometheus Operator storage spec for a volume of size on
// the default StorageClass.
func pvcTemplate(size string) map[string]any {
	return map[string]any{"volumeClaimTemplate": map[string]any{"spec": map[string]any{
		"accessModes": []any{"ReadWriteOnce"},
		"resources":   map[string]any{"requests": map[string]any{"storage": size}},
	}}}
}
