package apps

import (
	"context"
	"fmt"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// The shared Gateway every exposed app attaches its routes to.
const (
	GatewayNamespace = "k8slocalcli-gateway"
	GatewayName      = "local"
	// TLSSecretName holds the *.localtest.me certificate the HTTPS listener
	// serves, whether self-signed or issued by cert-manager.
	TLSSecretName = "local-tls"
)

// gatewayAPIManifestURL is the upstream Gateway API CRD bundle for a channel.
// There is no official Helm chart for these CRDs, so manifest is the only
// method.
func gatewayAPIManifestURL(version string, channel cluster.GatewayChannel) string {
	return fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/%s/%s-install.yaml", version, channel)
}

// GatewayController is a Gateway API implementation. Envoy Gateway is the only
// one today; adding another (Cilium, Traefik, NGINX Gateway Fabric, ...) means
// implementing this, registering it in controllers, and adding its name to
// cluster.GatewayControllers.
//
// The contract that keeps the rest of k8slocalcli controller-agnostic:
//   - SetupExposure owns the Gateway API CRDs (so the user's channel choice is
//     what ends up installed); Install must not install or overwrite them.
//   - The controller's data-plane Service must be type NodePort on
//     cluster.GatewayHTTPNodePort/GatewayHTTPSNodePort for listener ports
//     80/443, because those NodePorts on the first control-plane node are what
//     the host ports are published to.
//   - The data-plane pods must run on Target.GatewayNode (tolerating the
//     control-plane taint) with externalTrafficPolicy Local. Docker Desktop's
//     port forwarder delivers packets whose TCP checksum is left to offload;
//     kube-proxy forwarding them to a pod on another node over a VXLAN CNI
//     (flannel) never fills it in, so the remote node drops every SYN and the
//     host sees a hanging connection. Serving them on the receiving node
//     avoids the tunnel hop for the client-facing leg entirely.
//   - Install creates the GatewayClass named by GatewayClassName.
//
// A controller that needs the CRDs before the CNI starts (Cilium) will need a
// pre-CNI hook in the providers; Envoy does not.
type GatewayController interface {
	Name() cluster.GatewayController
	Description() string
	// Methods lists the supported install methods; the first is the default.
	Methods() []cluster.InstallMethod
	// GatewayAPIVersion is the Gateway API release the controller is built
	// against; its CRDs are installed at exactly this version.
	GatewayAPIVersion() string
	Install(ctx context.Context, env *Env, method cluster.InstallMethod) error
	GatewayClassName() string
}

var controllers = map[cluster.GatewayController]GatewayController{
	cluster.GatewayEnvoy: envoyGateway{},
}

// Controller returns the implementation for name.
func Controller(name cluster.GatewayController) (GatewayController, error) {
	c, ok := controllers[name]
	if !ok {
		return nil, fmt.Errorf("gateway controller %q is not implemented", name)
	}
	return c, nil
}

// SetupExposure installs the host-exposure stack: Gateway API CRDs on the
// chosen channel, the gateway controller, the HTTPS certificate, and the shared
// Gateway with HTTP :80 and HTTPS :443 listeners. It is a no-op when the target
// is not exposed.
func SetupExposure(ctx context.Context, env *Env, exp cluster.Exposure) error {
	if !env.Exposed || !exp.Enabled {
		return nil
	}
	if env.GatewayNode == "" {
		return fmt.Errorf("no gateway node known for cluster %q", env.Name)
	}
	ctrl, err := Controller(exp.Controller)
	if err != nil {
		return err
	}
	if !supports(ctrl.Methods(), exp.ControllerMethod) {
		return fmt.Errorf("%s cannot be installed via %s (supported: %s)", ctrl.Description(), exp.ControllerMethod, MethodsText(ctrl.Methods()))
	}

	fmt.Fprintf(env.Out, "\n🌐 Setting up host exposure (%s and %s)\n",
		env.URL("http", "<app>."+cluster.LocalDomain), env.URL("https", "<app>."+cluster.LocalDomain))

	if err := env.step(ctx, fmt.Sprintf("Installing Gateway API %s CRDs (%s channel)", ctrl.GatewayAPIVersion(), exp.Channel), "",
		func(ctx context.Context, se *Env) error {
			return se.applyURL(ctx, gatewayAPIManifestURL(ctrl.GatewayAPIVersion(), exp.Channel))
		}); err != nil {
		return fmt.Errorf("installing Gateway API CRDs: %w", err)
	}

	if err := env.rolloutStep(ctx, fmt.Sprintf("Installing %s via %s — pulling images, starting the controller", ctrl.Description(), exp.ControllerMethod), envoyNamespace,
		func(ctx context.Context, se *Env) error { return ctrl.Install(ctx, se, exp.ControllerMethod) }); err != nil {
		return fmt.Errorf("installing %s: %w", ctrl.Description(), err)
	}

	// The certificate must exist before the Gateway: a listener whose
	// certificateRef does not resolve is not programmed, and the controller
	// then omits its port from the Service the NodePorts are pinned on.
	if err := env.applyYAML(ctx, namespaceYAML(GatewayNamespace)); err != nil {
		return fmt.Errorf("creating namespace %s: %w", GatewayNamespace, err)
	}
	if err := setupTLS(ctx, env, exp); err != nil {
		return err
	}

	if err := env.rolloutStep(ctx, fmt.Sprintf("Starting the gateway proxy on node %s", env.GatewayNode), envoyNamespace,
		func(ctx context.Context, se *Env) error {
			if err := se.applyYAML(ctx, gatewayYAML(ctrl.GatewayClassName())); err != nil {
				return fmt.Errorf("creating gateway: %w", err)
			}
			if err := se.kubectl(ctx, "wait", "--for=condition=Programmed", "gateway/"+GatewayName,
				"-n", GatewayNamespace, "--timeout="+(5*time.Minute).String()); err != nil {
				return fmt.Errorf("gateway was not programmed in time: %w", err)
			}
			return nil
		}); err != nil {
		return err
	}

	return nil
}

// GatewayInstalled reports whether the shared Gateway exists in the cluster.
func GatewayInstalled(ctx context.Context, env *Env) bool {
	_, err := env.kubectlQuiet(ctx, "get", "gateway", GatewayName, "-n", GatewayNamespace, "-o", "name")
	return err == nil
}

// gatewayYAML is the shared Gateway. Listener hostnames are left open so apps
// can use names outside localtest.me; routes pick their own hostnames.
func gatewayYAML(className string) string {
	return fmt.Sprintf(`apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  gatewayClassName: %[3]s
  listeners:
    - name: http
      protocol: HTTP
      port: 80
      allowedRoutes:
        namespaces:
          from: All
    - name: https
      protocol: HTTPS
      port: 443
      tls:
        mode: Terminate
        certificateRefs:
          - kind: Secret
            name: %[4]s
      allowedRoutes:
        namespaces:
          from: All
`, GatewayName, GatewayNamespace, className, TLSSecretName)
}

// httpRouteYAML routes hostname on both listeners of the shared Gateway to a
// Service.
func httpRouteYAML(name, namespace, hostname, service string, port int) string {
	return fmt.Sprintf(`apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  parentRefs:
    - name: %[3]s
      namespace: %[4]s
  hostnames:
    - %[5]s
  rules:
    - backendRefs:
        - name: %[6]s
          port: %[7]d
`, name, namespace, GatewayName, GatewayNamespace, hostname, service, port)
}

// grpcRouteYAML is httpRouteYAML for a gRPC backend. gRPC rides on HTTP/2 over
// the same listeners, so it shares the gateway host ports; with TLS clients
// reach it on the HTTPS port, in plaintext (h2c) on the HTTP port.
func grpcRouteYAML(name, namespace, hostname, service string, port int) string {
	return fmt.Sprintf(`apiVersion: gateway.networking.k8s.io/v1
kind: GRPCRoute
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  parentRefs:
    - name: %[3]s
      namespace: %[4]s
  hostnames:
    - %[5]s
  rules:
    - backendRefs:
        - name: %[6]s
          port: %[7]d
`, name, namespace, GatewayName, GatewayNamespace, hostname, service, port)
}

func supports(ms []cluster.InstallMethod, m cluster.InstallMethod) bool {
	for _, x := range ms {
		if x == m {
			return true
		}
	}
	return false
}
