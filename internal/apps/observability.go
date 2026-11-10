package apps

import (
	"context"
	"fmt"
	"time"
)

// kube-prometheus-stack pins: chart 93.1.1 ships Prometheus Operator v0.95.1.
const (
	kpsChartVersion = "93.1.1"
	kpsNamespace    = "monitoring"
	kpsRelease      = "kube-prometheus-stack"
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
