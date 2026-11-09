package provider

import (
	"context"
	"fmt"
	"io"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/engine"
)

// Several clusters can run side by side in Docker — kind puts every cluster on
// the shared "kind" network and lets Docker pick a random host port for each API
// server — but a host port can only be published once. Both providers publish
// the ingress ports on their first control-plane container (kind via
// extraPortMappings, talos via --exposed-ports), so the fixed 80/443 defaults
// are the only thing stopping a second cluster from coming up:
//
//	docker: Bind for 0.0.0.0:80 failed: port is already allocated
//
// kind only hits that after pulling the node image and starting every node, then
// rolls the whole cluster back, so the ports are resolved before creation starts.

// Fallback bases used when the default ingress ports are already taken. They
// keep the mapping recognisable for a second cluster (80 -> 8080, 443 -> 8443)
// and count up from there for the third and beyond.
const (
	fallbackHTTPPort  = 8080
	fallbackHTTPSPort = 8443
	// portScanLimit bounds the search so a busy host cannot hang cluster creation.
	portScanLimit = 100
)

// portRequest is one host port a spec needs.
type portRequest struct {
	Port     int    // the requested port
	Flag     string // the flag that sets it, for error messages
	Default  int    // the built-in default for this port
	Fallback int    // base to count up from when a defaulted port is taken
	Assign   func(*cluster.Spec, int)
}

// specPortRequests returns the host ports a spec needs, in a stable order.
func specPortRequests(spec cluster.Spec) []portRequest {
	return []portRequest{
		{
			Port: spec.HTTPPort, Flag: "--http-port",
			Default: cluster.DefaultHTTPPort, Fallback: fallbackHTTPPort,
			Assign: func(s *cluster.Spec, p int) { s.HTTPPort = p },
		},
		{
			Port: spec.HTTPSPort, Flag: "--https-port",
			Default: cluster.DefaultHTTPSPort, Fallback: fallbackHTTPSPort,
			Assign: func(s *cluster.Spec, p int) { s.HTTPSPort = p },
		},
	}
}

// hostPortOwner returns the container already publishing hostPort, or "".
func hostPortOwner(ctx context.Context, hostPort int) (string, error) {
	return engine.Current().PortOwner(ctx, hostPort)
}

// unbindable reports whether a defaulted port must be skipped because the
// engine cannot publish privileged ports (rootless engines). Explicit choices
// are left alone so the engine reports the real error.
func unbindable(eng engine.Engine, req portRequest) bool {
	return !eng.PrivilegedPorts && req.Port == req.Default && req.Port < 1024
}

// explicitPortConflictError explains that a port the user asked for by name is
// taken. Explicit choices are never silently moved.
func explicitPortConflictError(req portRequest, owner string) error {
	return fmt.Errorf("host port %d (%s) is already in use by container %q;\n"+
		"choose a free port, or delete the cluster that is using it",
		req.Port, req.Flag, owner)
}

// noFreePortError reports that the scan from a fallback base found nothing.
func noFreePortError(req portRequest) error {
	return fmt.Errorf("no free host port found for %s in %d-%d;\n"+
		"free one up, or pass %s explicitly",
		req.Flag, req.Fallback, req.Fallback+portScanLimit-1, req.Flag)
}

// ResolveHostPorts settles the ingress host ports for a cluster that is about to
// be created, mutating spec in place and reporting any change to out.
//
// A port that is free is kept. A port left at its default is moved to the next
// free fallback, which is what lets a second cluster be created without flags. A
// port the user chose explicitly is never moved — that returns an error instead,
// since silently publishing on a different port than asked for is worse than
// failing.
//
// It is best effort: when Docker cannot be queried at all it returns nil rather
// than blocking creation, leaving the real diagnosis to CheckPrerequisites and
// the provider's own error.
func ResolveHostPorts(ctx context.Context, spec *cluster.Spec, out io.Writer) error {
	if !spec.Exposure.Enabled {
		return nil // nothing is published
	}
	eng := engine.Current()

	// Ports claimed earlier in this same call, so the two ingress ports cannot
	// be assigned the same number.
	claimed := map[int]bool{}

	for _, req := range specPortRequests(*spec) {
		var owner string
		if unbindable(eng, req) {
			owner = "(ports below 1024 need root with " + eng.Name + ")"
		} else {
			var err error
			if owner, err = hostPortOwner(ctx, req.Port); err != nil {
				return nil // engine unreachable: not our error to report.
			}
		}

		if owner == "" && !claimed[req.Port] {
			claimed[req.Port] = true
			continue
		}

		if req.Port != req.Default {
			if owner == "" {
				// Taken by the other ingress port in this same spec.
				return fmt.Errorf("host ports %s and the other ingress port are both %d; use two different ports",
					req.Flag, req.Port)
			}
			return explicitPortConflictError(req, owner)
		}

		// Defaulted port is taken: find the next free one so a second cluster
		// can be created without having to pass flags.
		picked := 0
		for candidate := req.Fallback; candidate < req.Fallback+portScanLimit; candidate++ {
			if claimed[candidate] {
				continue
			}
			candidateOwner, err := hostPortOwner(ctx, candidate)
			if err != nil {
				return nil
			}
			if candidateOwner == "" {
				picked = candidate
				break
			}
		}
		if picked == 0 {
			return noFreePortError(req)
		}

		claimed[picked] = true
		req.Assign(spec, picked)
		reason := fmt.Sprintf("in use by container %q", owner)
		if unbindable(eng, req) {
			reason = owner
		}
		fmt.Fprintf(out, "ℹ️  Host port %d is unavailable (%s); using %d instead (%s)\n",
			req.Port, reason, picked, req.Flag)
	}

	return nil
}

// GatewayHostPorts returns the host ports an existing cluster publishes to the
// gateway NodePorts, and false when it was created without host exposure (or
// by a version that mapped the ports elsewhere).
func GatewayHostPorts(ctx context.Context, p Provider, name string) (httpPort, httpsPort int, ok bool) {
	eng := engine.Current()
	node := p.GatewayNode(name)
	h, err := eng.PublishedPort(ctx, node, cluster.GatewayHTTPNodePort)
	if err != nil {
		return 0, 0, false
	}
	hs, err := eng.PublishedPort(ctx, node, cluster.GatewayHTTPSNodePort)
	if err != nil {
		return 0, 0, false
	}
	return h, hs, true
}
