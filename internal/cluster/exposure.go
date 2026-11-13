package cluster

import (
	"fmt"
	"regexp"
	"strings"
)

// InstallMethod is how an application is installed into a cluster. Not every
// application offers every method; the app catalog lists what each supports.
type InstallMethod string

const (
	MethodHelm     InstallMethod = "helm"
	MethodManifest InstallMethod = "manifest"
	// MethodFlux installs a Helm chart through Flux: a source plus a
	// HelmRelease that Flux reconciles. Flux must be installed (or requested).
	MethodFlux InstallMethod = "flux"
	// MethodArgo installs a Helm chart through Argo CD: an Application that
	// Argo CD syncs. Argo CD must be installed (or requested).
	MethodArgo InstallMethod = "argo"
)

// InstallMethods is the ordered list of known install methods.
var InstallMethods = []InstallMethod{MethodHelm, MethodManifest, MethodFlux, MethodArgo}

// DirectMethods are the methods that do not go through a GitOps controller.
// The exposure stack (gateway controller, cert-manager) is installed before any
// app, so only these apply to it.
var DirectMethods = []InstallMethod{MethodHelm, MethodManifest}

// GitOps reports whether m hands the install to a GitOps controller.
func (m InstallMethod) GitOps() bool {
	return m == MethodFlux || m == MethodArgo
}

// Valid reports whether m is a known install method.
func (m InstallMethod) Valid() bool {
	for _, known := range InstallMethods {
		if m == known {
			return true
		}
	}
	return false
}

// GatewayChannel is the Gateway API release channel whose CRDs are installed.
type GatewayChannel string

const (
	// ChannelStandard installs only GA/beta Gateway API resources.
	ChannelStandard GatewayChannel = "standard"
	// ChannelExperimental adds the experimental resources and fields on top.
	ChannelExperimental GatewayChannel = "experimental"
)

// GatewayChannels is the ordered list of channels (index 0 is the default).
var GatewayChannels = []GatewayChannel{ChannelStandard, ChannelExperimental}

// Valid reports whether c is a known channel.
func (c GatewayChannel) Valid() bool {
	for _, known := range GatewayChannels {
		if c == known {
			return true
		}
	}
	return false
}

// GatewayController identifies the Gateway API implementation that serves the
// cluster's Gateway. Only Envoy Gateway is implemented; the type and the
// apps.GatewayController interface are the seam for adding others.
type GatewayController string

const (
	GatewayEnvoy GatewayController = "envoy"
)

// GatewayControllers is the ordered list of implemented controllers.
var GatewayControllers = []GatewayController{GatewayEnvoy}

// Valid reports whether g is a known controller.
func (g GatewayController) Valid() bool {
	for _, known := range GatewayControllers {
		if g == known {
			return true
		}
	}
	return false
}

// TLSMode selects where the Gateway's HTTPS certificate comes from.
type TLSMode string

const (
	// TLSSelfSigned serves a self-signed wildcard certificate; browsers warn.
	TLSSelfSigned TLSMode = "self-signed"
	// TLSVerified installs cert-manager, issues the certificate from a
	// k8slocalcli CA kept in ~/.k8slocalcli/ca, and offers to trust that CA on
	// this machine so browsers and curl accept the endpoints.
	TLSVerified TLSMode = "verified"
)

// TLSModes is the ordered list of TLS modes (index 0 is the default).
var TLSModes = []TLSMode{TLSSelfSigned, TLSVerified}

// Valid reports whether t is a known TLS mode.
func (t TLSMode) Valid() bool {
	for _, known := range TLSModes {
		if t == known {
			return true
		}
	}
	return false
}

// Fixed NodePorts the gateway's Service listens on. The host ports (Spec.HTTPPort
// and Spec.HTTPSPort) are published to these on the first control-plane node,
// so any controller works as long as its Service uses them, without pinning its
// pods to that node.
const (
	GatewayHTTPNodePort  = 30080
	GatewayHTTPSNodePort = 30443
)

// LocalDomain is the wildcard DNS domain that resolves to 127.0.0.1
// (https://github.com/localtest-dot-me/localtest-dot-me.github.com), so
// <app>.localtest.me reaches the host-published gateway ports with no
// /etc/hosts edits.
const LocalDomain = "localtest.me"

// Exposure describes how services inside the cluster are reached from the host
// machine running the container engine.
type Exposure struct {
	// Enabled publishes the gateway host ports and installs Gateway API. Host
	// ports are fixed when the cluster is created, so it cannot be turned on
	// afterwards.
	Enabled bool

	Controller       GatewayController
	ControllerMethod InstallMethod
	Channel          GatewayChannel

	TLS TLSMode
	// CertManagerMethod is used only when TLS is TLSVerified.
	CertManagerMethod InstallMethod
}

// DefaultExposure is exposure on, with Envoy Gateway on the standard channel and
// a self-signed certificate.
func DefaultExposure() Exposure {
	return Exposure{
		Enabled:           true,
		Controller:        GatewayEnvoy,
		ControllerMethod:  MethodHelm,
		Channel:           ChannelStandard,
		TLS:               TLSSelfSigned,
		CertManagerMethod: MethodHelm,
	}
}

// Validate checks the exposure settings. Nothing is checked when exposure is
// off, since the other fields are then unused.
func (e Exposure) Validate() error {
	if !e.Enabled {
		return nil
	}
	if !e.Controller.Valid() {
		return fmt.Errorf("unknown gateway controller %q (supported: %s)", e.Controller, joinValues(GatewayControllers))
	}
	if !e.ControllerMethod.Valid() || e.ControllerMethod.GitOps() {
		return fmt.Errorf("unknown gateway install method %q (supported: %s)", e.ControllerMethod, joinValues(DirectMethods))
	}
	if !e.Channel.Valid() {
		return fmt.Errorf("unknown Gateway API channel %q (supported: %s)", e.Channel, joinValues(GatewayChannels))
	}
	if !e.TLS.Valid() {
		return fmt.Errorf("unknown TLS mode %q (supported: %s)", e.TLS, joinValues(TLSModes))
	}
	if e.TLS == TLSVerified && (!e.CertManagerMethod.Valid() || e.CertManagerMethod.GitOps()) {
		return fmt.Errorf("unknown cert-manager install method %q (supported: %s)", e.CertManagerMethod, joinValues(DirectMethods))
	}
	return nil
}

// AppRequest asks for one catalog application to be installed. Which names and
// methods exist is defined by the apps catalog, which parses requests before
// they reach a Spec; Validate only checks their shape.
type AppRequest struct {
	Name   string
	Method InstallMethod
	// MethodDefaulted marks a Method filled in with the app's default rather
	// than asked for; re-applying an installed app then keeps the method it
	// was installed with.
	MethodDefaulted bool
	// Host is the hostname the app is exposed on through the gateway, e.g.
	// argocd.localtest.me. Empty means the app's default; ignored when exposure
	// is off or the app has nothing to expose.
	Host string
	// Implicit marks a dependency added by the planner rather than asked for:
	// when it is already installed it is left alone without asking.
	Implicit bool
}

// hostnamePattern is a lowercase RFC 1123 DNS name with at least two labels.
var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`)

// ValidHostname reports whether h can be used as a route hostname.
func ValidHostname(h string) bool {
	return len(h) <= 253 && hostnamePattern.MatchString(h)
}

func validateApps(reqs []AppRequest) error {
	seen := map[string]bool{}
	for _, r := range reqs {
		if r.Name == "" {
			return fmt.Errorf("app name cannot be empty")
		}
		if seen[r.Name] {
			return fmt.Errorf("app %q requested more than once", r.Name)
		}
		seen[r.Name] = true
		if r.Method != "" && !r.Method.Valid() {
			return fmt.Errorf("app %q: unknown install method %q (supported: %s)", r.Name, r.Method, joinValues(InstallMethods))
		}
		if r.Host != "" && !ValidHostname(r.Host) {
			return fmt.Errorf("app %q: %q is not a valid hostname", r.Name, r.Host)
		}
	}
	return nil
}

// Warnings returns the consequences of the chosen settings that are not errors
// but that the user should see before the cluster is created, because they
// cannot be changed afterwards.
func (s Spec) Warnings() []string {
	var w []string
	if !s.Exposure.Enabled {
		w = append(w, "Host exposure is off: no ports are published to this machine, and they cannot be added after the cluster is created. "+
			"Web UIs and APIs will only be reachable with `kubectl port-forward`.")
		for _, a := range s.Apps {
			if a.Host != "" {
				w = append(w, fmt.Sprintf("%s: hostname %s is ignored because host exposure is off.", a.Name, a.Host))
			}
		}
		return w
	}
	if s.Exposure.TLS == TLSVerified {
		w = append(w, "Verified HTTPS creates a local CA (~/.k8slocalcli/ca) and asks to add it to this machine's trust store.")
	}
	for _, a := range s.Apps {
		if a.Host != "" && !IsLocalDomain(a.Host) {
			w = append(w, fmt.Sprintf("%s: %s is outside *.%s, so it needs its own DNS/hosts entry pointing at 127.0.0.1, and the gateway certificate will not match it.",
				a.Name, a.Host, LocalDomain))
		}
	}
	return w
}

// IsLocalDomain reports whether host is a single label under LocalDomain, i.e.
// covered by both the localtest.me DNS wildcard and the *.localtest.me
// certificate.
func IsLocalDomain(host string) bool {
	label, ok := strings.CutSuffix(host, "."+LocalDomain)
	return ok && label != "" && !strings.Contains(label, ".")
}

func joinValues[T ~string](vs []T) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = string(v)
	}
	return strings.Join(parts, ", ")
}
