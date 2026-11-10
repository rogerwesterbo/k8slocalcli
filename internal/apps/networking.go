package apps

import (
	"context"
	"fmt"
	"time"
)

// MetalLB pins.
const (
	metalLBVersion   = "0.16.2"
	metalLBNamespace = "metallb-system"
	metalLBPool      = "k8slocalcli"
)

var metalLBApp = App{
	Name:        "metallb",
	Description: "MetalLB v" + metalLBVersion + " (LoadBalancer Services, L2)",
	Category:    CategoryNetworking,
	Namespace:   metalLBNamespace,
	// The chart prefixes the release name; the upstream manifest does not.
	Detect:     []string{"deployment/metallb-controller", "deployment/controller"},
	Privileged: true, // the speaker uses the host network and NET_RAW
	CRD:        "ipaddresspools.metallb.io",
	chart: &helmChart{
		Release:   "metallb",
		Chart:     "metallb",
		Repo:      "https://metallb.github.io/metallb",
		Version:   metalLBVersion,
		Namespace: metalLBNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		values := map[string]any{}
		if !env.production() {
			// L2 mode only: frr-k8s (the BGP backend, on by default) adds a
			// DaemonSet per node that a local cluster never uses.
			values["frrk8s"] = map[string]any{"enabled": false}
		}
		return values, nil
	},
	manifestURL: "https://raw.githubusercontent.com/metallb/metallb/v" + metalLBVersion + "/config/manifests/metallb-native.yaml",
	configure:   metalLBConfigure,
	notes:       metalLBNotes,
}

// metalLBConfigure gives MetalLB an address pool on the nodes' container
// network. Without a known range (a cluster k8slocalcli did not create) the
// pool is left to the user, since guessing one could hand out addresses that
// are in use.
func metalLBConfigure(ctx context.Context, env *Env) error {
	if env.LoadBalancerRange == "" {
		return nil
	}
	pool := fmt.Sprintf(`apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  addresses:
    - %[3]s
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  ipAddressPools:
    - %[1]s
`, metalLBPool, metalLBNamespace, env.LoadBalancerRange)
	// MetalLB's webhook rejects requests until its controller is serving.
	return env.applyYAMLRetry(ctx, pool, remaining(ctx, 2*time.Minute))
}

func metalLBNotes(ctx context.Context, env *Env, _ string) {
	if env.exists(ctx, metalLBNamespace, "ipaddresspool/"+metalLBPool) {
		out, _ := env.kubectlQuiet(ctx, "get", "ipaddresspool", metalLBPool, "-n", metalLBNamespace, "-o", "jsonpath={.spec.addresses[*]}")
		fmt.Fprintf(env.Out, "   Pool:     %s (IPAddressPool %s/%s, L2)\n", out, metalLBNamespace, metalLBPool)
		if !env.External() {
			fmt.Fprintf(env.Out, "   Note:     the addresses live on the container network; on macOS/Windows (Docker Desktop) they are not reachable from the host — use the gateway or port-forward\n")
		}
		return
	}
	fmt.Fprintf(env.Out, "   Pool:     none — create an IPAddressPool + L2Advertisement in %s with free addresses on your node network\n", metalLBNamespace)
}

// metrics-server pins.
const (
	metricsServerVersion      = "0.9.0"
	metricsServerChartVersion = "3.14.0"
)

var metricsServerApp = App{
	Name:        "metrics-server",
	Description: "metrics-server v" + metricsServerVersion + " (kubectl top, HPA)",
	Category:    CategoryObservability,
	Namespace:   "kube-system",
	Detect:      []string{"deployment/metrics-server"},
	Workloads:   []string{"deployment/metrics-server"},
	chart: &helmChart{
		Release:   "metrics-server",
		Chart:     "metrics-server",
		Repo:      "https://kubernetes-sigs.github.io/metrics-server/",
		Version:   metricsServerChartVersion,
		Namespace: "kube-system",
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		values := map[string]any{}
		if args := metricsServerArgs(env); len(args) > 0 {
			values["args"] = args
		}
		if env.production() {
			values["replicas"] = 2
			values["podDisruptionBudget"] = map[string]any{"enabled": true, "minAvailable": 1}
		}
		return values, nil
	},
	manifestURL: metricsServerManifestURL,
	afterManifest: func(ctx context.Context, env *Env) error {
		for _, a := range metricsServerArgs(env) {
			patch := fmt.Sprintf(`[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":%q}]`, a)
			if err := env.kubectl(ctx, "patch", "deployment/metrics-server", "-n", "kube-system", "--type", "json", "-p", patch); err != nil {
				return err
			}
		}
		return nil
	},
	check: func(ctx context.Context, env *Env) error {
		// The metrics API is only served once the first scrape is in.
		return retry(ctx, remaining(ctx, 2*time.Minute), 5*time.Second, func(ctx context.Context) error {
			_, err := env.kubectlQuiet(ctx, "get", "--raw", "/apis/metrics.k8s.io/v1beta1/nodes")
			return err
		})
	},
	notes: func(_ context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   Try:      kubectl --context %s top nodes\n", env.KubeContext)
	},
}

var metricsServerManifestURL = "https://github.com/kubernetes-sigs/metrics-server/releases/download/v" + metricsServerVersion + "/components.yaml"

// metricsServerArgs are the extra flags for kind/Talos nodes, whose kubelet
// serving certificates are self-signed and whose node names do not resolve.
// Clusters k8slocalcli did not create keep the secure defaults.
func metricsServerArgs(env *Env) []string {
	if env.External() {
		return nil
	}
	return []string{"--kubelet-insecure-tls", "--kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname"}
}
