package runner

import (
	"context"
	"os"
	"strings"
	"time"
)

// EnsureDockerHost exports DOCKER_HOST from the active Docker CLI context when
// it is not already set.
//
// The docker CLI resolves its endpoint through contexts (Docker Desktop on
// macOS uses ~/.docker/run/docker.sock via the "desktop-linux" context), but
// tools built on the Docker Go SDK — notably talosctl — ignore contexts and
// only honour DOCKER_HOST, falling back to /var/run/docker.sock. When that
// default socket is absent they fail even though `docker ps` works. Setting
// DOCKER_HOST in this process makes every child command use the same daemon
// as the docker CLI. Best effort: any failure leaves the environment as-is.
func EnsureDockerHost(ctx context.Context) {
	if os.Getenv("DOCKER_HOST") != "" || !LookPath("docker") {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	host, err := New(nil).Capture(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		return
	}
	if isLocalDockerEndpoint(host) {
		_ = os.Setenv("DOCKER_HOST", host)
	}
}

// isLocalDockerEndpoint reports whether host is a local socket endpoint that
// is fully described by DOCKER_HOST alone: a Unix socket (macOS, Linux) or a
// Windows named pipe. Remote endpoints are rejected on purpose: tcp:// contexts
// usually carry TLS material that DOCKER_HOST cannot express, ssh:// is not
// understood by Docker SDK clients such as talosctl, and setting DOCKER_HOST
// would also make the docker CLI ignore the (working) context.
func isLocalDockerEndpoint(host string) bool {
	return strings.HasPrefix(host, "unix://") || strings.HasPrefix(host, "npipe://")
}
