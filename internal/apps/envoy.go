package apps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// Envoy Gateway pins. Each Envoy Gateway release is built against one Gateway
// API release (its go.mod; the bundle-version in its install.yaml), and the
// CRDs are installed at exactly that version.
const (
	envoyGatewayVersion = "v1.9.3"
	envoyGatewayAPI     = "v1.6.1"
	envoyNamespace      = "envoy-gateway-system"
	envoyClassName      = "k8slocalcli-envoy"
	envoyProxyName      = "k8slocalcli"
	envoyChart          = "oci://docker.io/envoyproxy/gateway-helm"
)

func envoyReleaseURL(asset string) string {
	return fmt.Sprintf("https://github.com/envoyproxy/gateway/releases/download/%s/%s", envoyGatewayVersion, asset)
}

type envoyGateway struct{}

func (envoyGateway) Name() cluster.GatewayController { return cluster.GatewayEnvoy }
func (envoyGateway) Description() string             { return "Envoy Gateway " + envoyGatewayVersion }
func (envoyGateway) GatewayAPIVersion() string       { return envoyGatewayAPI }
func (envoyGateway) GatewayClassName() string        { return envoyClassName }
func (envoyGateway) Methods() []cluster.InstallMethod {
	return []cluster.InstallMethod{cluster.MethodHelm, cluster.MethodManifest}
}

// Install installs Envoy Gateway without touching the Gateway API CRDs, which
// SetupExposure already installed on the user's channel. Both of Envoy's own
// install paths would otherwise overwrite them: the chart's crds subchart and
// install.yaml both bundle the experimental channel.
func (g envoyGateway) Install(ctx context.Context, env *Env, method cluster.InstallMethod) error {
	switch method {
	case cluster.MethodHelm:
		// envoy-gateway-crds.yaml holds only Envoy's own CRDs (EnvoyProxy, the
		// policies, ...); crds.enabled=false then skips the subchart that
		// carries both CRD sets and the Gateway API safe-upgrade policy.
		if err := env.applyURL(ctx, envoyReleaseURL("envoy-gateway-crds.yaml")); err != nil {
			return fmt.Errorf("installing Envoy Gateway CRDs: %w", err)
		}
		if err := env.helmInstall(ctx, helmChart{
			Release:   "eg",
			Chart:     envoyChart,
			Version:   envoyGatewayVersion,
			Namespace: envoyNamespace,
			Values:    map[string]any{"crds": map[string]any{"enabled": false}},
		}, defaultInstallTimeout); err != nil {
			return err
		}
	case cluster.MethodManifest:
		manifest, err := fetch(ctx, envoyReleaseURL("install.yaml"))
		if err != nil {
			return err
		}
		filtered, dropped, err := withoutGatewayAPIResources(manifest)
		if err != nil {
			return fmt.Errorf("filtering Envoy Gateway install.yaml: %w", err)
		}
		fmt.Fprintf(env.Out, "ℹ️  Skipping %d bundled Gateway API resources (already installed on the chosen channel)\n", dropped)
		if err := env.applyYAML(ctx, filtered); err != nil {
			return fmt.Errorf("applying Envoy Gateway install.yaml: %w", err)
		}
	default:
		return fmt.Errorf("unsupported method %q", method)
	}

	if err := env.waitAvailable(ctx, envoyNamespace, 5*time.Minute); err != nil {
		return fmt.Errorf("envoy gateway not ready in time: %w", err)
	}

	// The class points at an EnvoyProxy that makes the data plane a NodePort
	// Service on the fixed gateway NodePorts.
	if err := env.applyYAML(ctx, envoyClassYAML(env.GatewayNode)); err != nil {
		return fmt.Errorf("creating GatewayClass: %w", err)
	}
	return nil
}

// envoyClassYAML is the EnvoyProxy (data-plane settings) plus the GatewayClass
// that uses it.
//
// The Envoy pods are pinned to the gateway node (see the GatewayController
// contract for why traffic must not be forwarded to another node), so
// externalTrafficPolicy Local is both correct and keeps the client address. The
// strategic-merge patch pins the NodePorts by Service port (listener 80/443).
func envoyClassYAML(gatewayNode string) string {
	return fmt.Sprintf(`apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyDeployment:
        pod:
          nodeSelector:
            kubernetes.io/hostname: %[6]s
          tolerations:
            - key: node-role.kubernetes.io/control-plane
              operator: Exists
              effect: NoSchedule
      envoyService:
        type: NodePort
        externalTrafficPolicy: Local
        patch:
          type: StrategicMerge
          value:
            spec:
              ports:
                - port: 80
                  nodePort: %[3]d
                - port: 443
                  nodePort: %[4]d
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: %[5]s
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
  parametersRef:
    group: gateway.envoyproxy.io
    kind: EnvoyProxy
    name: %[1]s
    namespace: %[2]s
`, envoyProxyName, envoyNamespace, cluster.GatewayHTTPNodePort, cluster.GatewayHTTPSNodePort, envoyClassName, gatewayNode)
}

// fetch downloads a release manifest.
func fetch(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// The URL is built from pinned constants in this package, never user input.
	resp, err := http.DefaultClient.Do(req) // #nosec G107
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	return string(body), nil
}
