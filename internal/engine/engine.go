// Package engine is the seam between k8slocalcli and the container engine that
// runs the cluster nodes. Only Docker is implemented; everything that talks to
// the engine directly (host-port lookups, networks, container cleanup) goes
// through Current() so Podman can be added as a second Engine without touching
// the providers.
//
// What a Podman Engine would need to fill in, so the gaps are known up front:
//   - Binary "podman" (its CLI is Docker-compatible for every call made here).
//   - PrivilegedPorts=false when rootless: ports below 1024 need
//     net.ipv4.ip_unprivileged_port_start lowered, so the 80/443 defaults must
//     be skipped (ResolveHostPorts honours this already).
//   - kind supports it via KIND_EXPERIMENTAL_PROVIDER=podman; talosctl's
//     Docker backend only speaks the Docker API, so it needs the Podman
//     socket exported as DOCKER_HOST (the counterpart of EnsureDockerHost).
package engine

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// Engine describes a container engine CLI.
type Engine struct {
	// Name is the human-readable engine name used in messages.
	Name string
	// Binary is the CLI executable invoked for every engine command.
	Binary string
	// PrivilegedPorts reports whether host ports below 1024 can be published
	// without extra host configuration. True for Docker (Docker Desktop and a
	// root daemon); false for rootless engines.
	PrivilegedPorts bool
}

// Docker is the Docker engine (Docker Desktop, Colima, or a Linux daemon).
var Docker = Engine{Name: "Docker", Binary: "docker", PrivilegedPorts: true}

// Current returns the engine in use. Fixed to Docker until engine selection
// exists; callers must not hardcode "docker" so that switch stays one place.
func Current() Engine { return Docker }

// Available reports whether the engine CLI is on PATH.
func (e Engine) Available() bool { return runner.LookPath(e.Binary) }

// PortOwner returns the name of a running container already publishing
// hostPort, or "" when the port is free. The engine is the allocator here, so
// asking it is authoritative; binding a probe socket would not work, since ports
// below 1024 need root on macOS and would look taken.
func (e Engine) PortOwner(ctx context.Context, hostPort int) (string, error) {
	out, err := runner.New(nil).Capture(ctx, e.Binary, "ps",
		"--filter", fmt.Sprintf("publish=%d", hostPort),
		"--format", "{{.Names}}")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			return name, nil
		}
	}
	return "", nil
}

// PublishedPort returns the host port published for containerPort/tcp on the
// named container.
func (e Engine) PublishedPort(ctx context.Context, container string, containerPort int) (int, error) {
	out, err := runner.New(nil).Capture(ctx, e.Binary, "port", container, fmt.Sprintf("%d/tcp", containerPort))
	if err != nil {
		return 0, err
	}
	port := ParsePortOutput(out)
	if port == 0 {
		return 0, fmt.Errorf("no host port mapped to %d/tcp on %s", containerPort, container)
	}
	return port, nil
}

var hostPortRe = regexp.MustCompile(`:(\d+)\s*$`)

// ParsePortOutput extracts the host port from `docker port` output, handling
// both the "0.0.0.0:64659" and "[::]:64659" forms (taking the first mapping).
// It returns 0 when nothing parses.
func ParsePortOutput(out string) int {
	for _, line := range strings.Split(out, "\n") {
		if m := hostPortRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			if p, err := strconv.Atoi(m[1]); err == nil {
				return p
			}
		}
	}
	return 0
}

// ContainerSubnets returns the subnets of every network the container is
// attached to.
func (e Engine) ContainerSubnets(ctx context.Context, container string) ([]string, error) {
	r := runner.New(nil)
	nets, err := r.Capture(ctx, e.Binary, "inspect", container,
		"--format", "{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}")
	if err != nil {
		return nil, err
	}
	names := strings.Fields(nets)
	if len(names) == 0 {
		return nil, nil
	}
	args := append([]string{"network", "inspect"}, names...)
	args = append(args, "--format", "{{range .IPAM.Config}}{{.Subnet}} {{end}}")
	out, err := r.Capture(ctx, e.Binary, args...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}
