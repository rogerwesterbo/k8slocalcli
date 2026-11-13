package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/engine"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// Talos implements Provider using talosctl with the Docker backend, which runs
// Talos Linux nodes as Docker containers.
type Talos struct{}

// NewTalos returns a talos provider.
func NewTalos() *Talos { return &Talos{} }

// Name implements Provider.
func (t *Talos) Name() cluster.Provider { return cluster.ProviderTalos }

// Context implements Provider. talosctl names the admin context "admin@<name>".
func (t *Talos) Context(name string) string { return "admin@" + name }

// GatewayNode implements Provider: the Docker backend's init node.
func (t *Talos) GatewayNode(name string) string { return name + "-controlplane-1" }

// KubernetesVersions implements Provider, returning the Kubernetes versions
// supported by the installed talosctl (newest first).
func (t *Talos) KubernetesVersions(ctx context.Context) []string {
	return talosK8sVersionsFor(t.majorMinor(ctx))
}

// CheckPrerequisites implements Provider.
func (t *Talos) CheckPrerequisites() error {
	var missing []string
	for _, tool := range []string{"talosctl", engine.Current().Binary} {
		if !runner.LookPath(tool) {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing prerequisites for talos provider: %s\ninstall talosctl: brew install siderolabs/tap/talosctl", strings.Join(missing, ", "))
	}
	return nil
}

// stateDir returns the directory talosctl uses to store cluster state.
func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".k8slocalcli", "clusters")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return dir, nil
}

// Create implements Provider.
func (t *Talos) Create(ctx context.Context, spec cluster.Spec, out io.Writer) error {
	state, err := stateDir()
	if err != nil {
		return fmt.Errorf("preparing talos state directory: %w", err)
	}

	mm := t.majorMinor(ctx)
	version := spec.K8sVersion
	if version == "" {
		version = talosK8sVersionsFor(mm)[0]
	}
	version = strings.TrimPrefix(version, "v")

	// The Docker backend always provisions a single control plane.
	if spec.ControlPlanes > 1 {
		fmt.Fprintf(out, "⚠️  talosctl Docker backend supports only one control plane; creating 1 (requested %d)\n", spec.ControlPlanes)
		fmt.Fprintf(out, "   💡 Use the QEMU backend for multiple control planes.\n")
	}

	fmt.Fprintf(out, "\n⏰ Creating Talos cluster %q (k8s %s, CNI %s, 1 control plane, %d worker(s))\n",
		spec.Name, version, cniLabel(spec.CNI), spec.Workers)

	args := []string{
		"cluster", "create", "docker",
		"--name", spec.Name,
		"--state", state,
		"--workers", fmt.Sprintf("%d", spec.Workers),
		"--kubernetes-version", version,
		"--memory-controlplanes", "2048MB",
		"--memory-workers", "2048MB",
	}
	if spec.Exposure.Enabled {
		args = append(args, "--exposed-ports", talosExposedPorts(spec))
	}

	// Every Talos docker cluster defaults to the same 10.5.0.0/24 and Docker
	// refuses overlapping pools, so a second cluster needs a free one.
	if subnet := freeTalosSubnet(ctx); subnet != "" {
		fmt.Fprintf(out, "ℹ️  %s is already in use by another Docker network; using %s\n",
			talosDefaultSubnet, subnet)
		args = append(args, "--subnet", subnet)
	}

	patches := []string{talosDiscoveryOffPatch(mm)}

	bundle, sources, err := caBundle(spec.CACerts)
	if err != nil {
		return err
	}
	if bundle != "" {
		fmt.Fprintf(out, "🔐 Trusting extra CA certificates from: %s\n", strings.Join(sources, ", "))
		patches = append(patches, talosTrustedRootsPatch(bundle))
	}

	// For a custom CNI, disable Talos's default CNI (and kube-proxy for Cilium)
	// via a machine-config patch whose shape depends on the talosctl version.
	if spec.CNI.Custom() {
		patches = append(patches, talosCNIPatch(spec.CNI, mm))
	}

	for _, patch := range patches {
		file, cleanup, err := writeTempPatch(patch)
		if err != nil {
			return err
		}
		defer cleanup()
		args = append(args, "--config-patch", "@"+file)
	}

	// Contexts left by earlier attempts would make talosctl name this one
	// "<name>-N"; clear them so it gets the plain cluster name.
	t.removeStaleTalosContexts(ctx, spec.Name, out)

	if err := t.runTalosCreate(ctx, spec.CNI.Custom(), spec.Name, args, out); err != nil {
		return fmt.Errorf("could not create talos cluster: %w", err)
	}

	fmt.Fprintf(out, "\n⏰ Merging kubeconfig\n")
	if err := t.MergeKubeconfig(ctx, spec.Name, out); err != nil {
		fmt.Fprintf(out, "⚠️  Could not merge kubeconfig automatically (kubectl may not reach the cluster): %v\n", err)
	}

	// Install the chosen CNI now that the API server is reachable; this is what
	// finally brings the nodes Ready (the default CNI was disabled above).
	if spec.CNI.Custom() {
		if err := installCNI(ctx, t.Name(), spec.CNI, t.Context(spec.Name), out); err != nil {
			return err
		}
	}

	// The Docker backend has a single control plane and usually no workers, so
	// without this there is nowhere to schedule pods.
	if spec.Workers == 0 {
		makeControlPlaneSchedulable(ctx, t.Context(spec.Name), out)
	}

	fmt.Fprintf(out, "\n✅ Talos cluster %q is ready\n", spec.Name)
	return nil
}

// talosExposedPorts publishes the gateway host ports to the fixed gateway
// NodePorts on the init node (talosctl's <hostPort>:<containerPort>/<proto>).
func talosExposedPorts(spec cluster.Spec) string {
	return fmt.Sprintf("%d:%d/tcp,%d:%d/tcp",
		spec.HTTPPort, cluster.GatewayHTTPNodePort,
		spec.HTTPSPort, cluster.GatewayHTTPSNodePort)
}

// writeTempPatch writes a machine-config patch to a temporary file for
// `talosctl --config-patch @file` and returns a func that removes it.
func writeTempPatch(patch string) (string, func(), error) {
	f, err := os.CreateTemp("", "talos-patch-*.yaml")
	if err != nil {
		return "", nil, fmt.Errorf("creating talos config patch: %w", err)
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err := f.WriteString(patch); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("writing talos config patch: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("closing talos config patch: %w", err)
	}
	return f.Name(), cleanup, nil
}

// runTalosCreate runs `talosctl cluster create`. With a custom CNI the default
// CNI is disabled, so talosctl's health checks (which require nodes to be Ready)
// never pass and the command would block until its own timeout. In that case we
// run it in the background and return as soon as the API server is reachable,
// letting the subsequent CNI install bring the nodes Ready.
func (t *Talos) runTalosCreate(ctx context.Context, customCNI bool, name string, args []string, out io.Writer) error {
	if !customCNI {
		return runner.New(out).Run(ctx, "talosctl", args...)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		runErr = runner.New(out).Run(runCtx, "talosctl", args...)
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Minute)
	defer timeout.Stop()

	for {
		select {
		case <-done:
			// talosctl returned on its own (typically an early failure).
			return runErr
		case <-timeout.C:
			cancel()
			<-done
			if t.apiReachable(ctx, name) {
				return nil
			}
			return fmt.Errorf("talos control plane did not become reachable within 5m")
		case <-ticker.C:
			if t.apiReachable(ctx, name) {
				fmt.Fprintf(out, "\nℹ️  Control plane API is up; installing CNI to finish bringing nodes Ready\n")
				cancel()
				<-done // wait for the killed process to be reaped
				return nil
			}
		}
	}
}

// apiReachable reports whether the cluster's Kubernetes API is responding, by
// asking talosctl to fetch a throwaway kubeconfig.
func (t *Talos) apiReachable(ctx context.Context, name string) bool {
	tmp, err := os.CreateTemp("", "talos-probe-*.kubeconfig")
	if err != nil {
		return false
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(path) }()
	return runner.New(nil).Run(ctx, "talosctl", t.kubeconfigArgs(ctx, name, path, "--force")...) == nil
}

// Kubeconfig implements Provider. It downloads the admin kubeconfig into a
// temporary file and rewrites its server URL to the host-mapped port (see
// fixKubeconfigServer) so the result works from the host.
func (t *Talos) Kubeconfig(ctx context.Context, name string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "talos-*.kubeconfig")
	if err != nil {
		return nil, fmt.Errorf("creating temporary kubeconfig: %w", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(path) }()

	r := runner.New(nil)
	if err := r.Run(ctx, "talosctl", t.kubeconfigArgs(ctx, name, path, "--merge=false", "--force")...); err != nil {
		return nil, fmt.Errorf("could not get kubeconfig for talos cluster %q: %w", name, err)
	}
	if err := fixKubeconfigServer(ctx, r, name, path, io.Discard); err != nil {
		return nil, fmt.Errorf("could not point kubeconfig at the host port: %w", err)
	}
	cfg, err := os.ReadFile(path) // #nosec G304 -- path is our own temp file from os.CreateTemp
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig: %w", err)
	}
	return cfg, nil
}

// MergeKubeconfig implements Provider. talosctl merges the admin kubeconfig into
// the default kubeconfig; the server URL is then rewritten to the host-mapped
// port and the cluster's context made current.
func (t *Talos) MergeKubeconfig(ctx context.Context, name string, out io.Writer) error {
	r := runner.New(out)
	if err := r.Run(ctx, "talosctl", t.kubeconfigArgs(ctx, name, "", "--force")...); err != nil {
		return fmt.Errorf("could not merge kubeconfig for talos cluster %q: %w", name, err)
	}
	if err := fixKubeconfigServer(ctx, r, name, "", out); err != nil {
		return fmt.Errorf("could not point kubeconfig at the host port: %w", err)
	}
	if err := r.Run(ctx, "kubectl", "config", "use-context", t.Context(name)); err != nil {
		return fmt.Errorf("could not switch kubectl context: %w", err)
	}
	return nil
}

// kubeconfigArgs builds a `talosctl kubeconfig` invocation for the cluster,
// writing to localPath (empty = merge into the default kubeconfig). The
// talosconfig context is chosen by talosContextFor; when none matches,
// talosctl's current context is used (the most recently created cluster).
func (t *Talos) kubeconfigArgs(ctx context.Context, name, localPath string, extra ...string) []string {
	args := []string{"kubeconfig"}
	if localPath != "" {
		args = append(args, localPath)
	}
	args = append(args, "--cluster", name, "--nodes", "127.0.0.1")
	if c := t.talosContextFor(ctx, name); c != "" {
		args = append(args, "--context", c)
	}
	return append(args, extra...)
}

// talosContextFor returns the talosconfig context that reaches cluster name.
// The name alone is not reliable: talosctl renames a new context to
// "<name>-1", "<name>-2", ... when a context called <name> is already there
// (e.g. left over from a failed attempt), and that stale one points at a dead
// port. So the context whose endpoint is the cluster's live apid port wins.
func (t *Talos) talosContextFor(ctx context.Context, name string) string {
	out, err := runner.New(nil).Capture(ctx, "talosctl", "config", "contexts")
	if err != nil {
		return ""
	}
	port, _ := nodeHostPort(ctx, name+"-controlplane-1", talosAPIPort)
	return pickTalosContext(parseTalosContexts(out), name, port)
}

// talosAPIPort is the apid port inside a Talos node container.
const talosAPIPort = "50000"

// pickTalosContext prefers the context whose endpoint uses apidPort (the
// host port Docker published for this cluster), then one named exactly name.
func pickTalosContext(contexts []talosContext, name, apidPort string) string {
	if apidPort != "" {
		for _, c := range contexts {
			if c.hasPort(apidPort) {
				return c.Name
			}
		}
	}
	for _, c := range contexts {
		if c.Name == name {
			return c.Name
		}
	}
	return ""
}

// removeStaleTalosContexts drops talosconfig contexts that talosctl created
// for earlier clusters called name ("<name>" and "<name>-N") and that no longer
// reach anything, so the next create gets the plain name again and
// talosContextFor is not confused. Kept: the context of the live cluster (by
// apid port) and "<name>-N" when a Talos cluster is literally named that.
// talosctl itself refuses to remove the current context. Best effort.
func (t *Talos) removeStaleTalosContexts(ctx context.Context, name string, out io.Writer) {
	list, err := runner.New(nil).Capture(ctx, "talosctl", "config", "contexts")
	if err != nil {
		return
	}
	livePort, _ := nodeHostPort(ctx, name+"-controlplane-1", talosAPIPort)
	clusters, _ := t.List(ctx)
	for _, c := range staleTalosContexts(parseTalosContexts(list), name, livePort, clusters) {
		_ = runner.New(out).Run(ctx, "talosctl", "config", "remove", c, "--noconfirm")
	}
}

var talosContextSuffixRe = regexp.MustCompile(`^-\d+$`)

// staleTalosContexts is the pure selection behind removeStaleTalosContexts.
func staleTalosContexts(contexts []talosContext, name, livePort string, clusters []string) []string {
	var stale []string
	for _, c := range contexts {
		if c.Name != name {
			rest, ok := strings.CutPrefix(c.Name, name)
			if !ok || !talosContextSuffixRe.MatchString(rest) || slices.Contains(clusters, c.Name) {
				continue
			}
		}
		if livePort != "" && c.hasPort(livePort) {
			continue
		}
		stale = append(stale, c.Name)
	}
	return stale
}

// talosContext is one row of `talosctl config contexts`.
type talosContext struct {
	Name      string
	Endpoints []string // e.g. ["127.0.0.1:64946"]
}

func (c talosContext) hasPort(port string) bool {
	for _, e := range c.Endpoints {
		if strings.HasSuffix(e, ":"+port) {
			return true
		}
	}
	return false
}

// parseTalosContexts parses `talosctl config contexts` output, a table with a
// CURRENT column ("*" on the active row), then NAME, ENDPOINTS, NODES.
func parseTalosContexts(out string) []talosContext {
	var contexts []talosContext
	for i, line := range nonEmptyLines(out) {
		if i == 0 && strings.HasPrefix(line, "CURRENT") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "*" {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		c := talosContext{Name: fields[0]}
		if len(fields) > 1 {
			c.Endpoints = strings.Split(fields[1], ",")
		}
		contexts = append(contexts, c)
	}
	return contexts
}

// fixKubeconfigServer rewrites the cluster's API server URL in a kubeconfig to
// the host-mapped port. talosctl records the control-plane's internal Docker IP
// (e.g. https://10.5.0.2:6443), which the host cannot reach on macOS/Windows;
// the Docker backend forwards the API server (6443) to a localhost port
// instead. kubeconfigPath selects the file to edit (empty = the default).
func fixKubeconfigServer(ctx context.Context, r *runner.Runner, name, kubeconfigPath string, out io.Writer) error {
	port, err := nodeHostPort(ctx, name+"-controlplane-1", "6443")
	if err != nil {
		return err
	}
	server := fmt.Sprintf("https://127.0.0.1:%s", port)
	fmt.Fprintf(out, "🔧 Pointing kubeconfig cluster %q at %s\n", name, server)

	var args []string
	if kubeconfigPath != "" {
		args = append(args, "--kubeconfig", kubeconfigPath)
	}
	args = append(args, "config", "set-cluster", name, "--server="+server)
	return r.Run(ctx, "kubectl", args...)
}

// nodeHostPort returns the host port the container engine mapped to
// containerPort/tcp on the named node container (e.g. "64659").
func nodeHostPort(ctx context.Context, container, containerPort string) (string, error) {
	cp, err := strconv.Atoi(containerPort)
	if err != nil {
		return "", fmt.Errorf("invalid container port %q", containerPort)
	}
	hp, err := engine.Current().PublishedPort(ctx, container, cp)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(hp), nil
}

// Delete implements Provider.
func (t *Talos) Delete(ctx context.Context, name string, out io.Writer) error {
	r := runner.New(out)

	state, err := stateDir()
	if err != nil {
		return fmt.Errorf("locating talos state directory: %w", err)
	}

	fmt.Fprintf(out, "\n⏰ Deleting Talos cluster %q\n", name)
	if iso, cleanup, err := isolateState(state, name); err != nil {
		fmt.Fprintf(out, "⚠️  skipping talosctl destroy, attempting manual cleanup: %v\n", err)
	} else {
		if err := r.Run(ctx, "talosctl", "cluster", "destroy", "--name", name, "--state", iso); err != nil {
			fmt.Fprintf(out, "⚠️  talosctl destroy failed, attempting manual cleanup: %v\n", err)
		}
		cleanup()
	}

	// Best-effort cleanup of any leftover containers/network for this cluster.
	for _, c := range talosContainers(ctx, name) {
		_ = r.Run(ctx, engine.Current().Binary, "rm", "-f", c)
	}
	_ = r.Run(ctx, engine.Current().Binary, "network", "rm", name)

	// Remove the cluster state directory.
	_ = os.RemoveAll(filepath.Join(state, name))
	t.removeStaleTalosContexts(ctx, name, out)

	fmt.Fprintf(out, "\n✅ Talos cluster %q deleted\n", name)
	return nil
}

// isolateState moves cluster name's state directory into a fresh directory
// under state and returns that directory, for `talosctl cluster destroy
// --state`. talosctl 1.14's Docker Destroy removes the whole --state directory
// (Reflect takes it as the cluster's own path), which would wipe every other
// cluster's state. cleanup removes the isolated directory. A missing state
// directory is not an error: destroy then fails on its own and Delete falls
// back to manual cleanup.
func isolateState(state, name string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp(state, ".destroy-"+name+"-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	if err := os.Rename(filepath.Join(state, name), filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// Leftovers implements LeftoverChecker. It reports what a previous (usually
// failed) attempt left behind for name: talosctl refuses to create a cluster
// while its state directory exists, even when it is empty.
func (t *Talos) Leftovers(ctx context.Context, name string) []string {
	var found []string
	if state, err := stateDir(); err == nil {
		dir := filepath.Join(state, name)
		if _, err := os.Stat(dir); err == nil {
			found = append(found, "state directory "+dir)
		}
	}
	for _, c := range talosContainers(ctx, name) {
		found = append(found, "container "+c)
	}
	if engine.Current().Available() {
		label, err := runner.New(nil).Capture(ctx, engine.Current().Binary, "network", "inspect", name,
			"--format", `{{index .Labels "talos.cluster.name"}}`)
		if err == nil && label == name {
			found = append(found, "docker network "+name)
		}
	}
	return found
}

// talosContainers returns the node containers of cluster name
// ("<name>-controlplane-N", "<name>-worker-N"). Docker's name filter is a
// substring match, so results are narrowed to an exact prefix: otherwise
// cluster "test" would also match "mytest-controlplane-1".
func talosContainers(ctx context.Context, name string) []string {
	if !engine.Current().Available() {
		return nil
	}
	names, err := dockerNames(ctx, name+"-")
	if err != nil {
		return nil
	}
	return filterTalosContainers(names, name)
}

func filterTalosContainers(names []string, cluster string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, cluster+"-controlplane-") || strings.HasPrefix(n, cluster+"-worker-") {
			out = append(out, n)
		}
	}
	return out
}

// List implements Provider. Talos Docker clusters are discovered by their
// "<name>-controlplane-1" container.
func (t *Talos) List(ctx context.Context) ([]string, error) {
	if !engine.Current().Available() {
		return nil, nil
	}
	names, err := dockerNames(ctx, "controlplane-1")
	if err != nil {
		return nil, err
	}
	var clusters []string
	for _, n := range names {
		if strings.HasSuffix(n, "-controlplane-1") {
			clusters = append(clusters, strings.TrimSuffix(n, "-controlplane-1"))
		}
	}
	return clusters, nil
}

// Exists implements Provider.
func (t *Talos) Exists(ctx context.Context, name string) (bool, error) {
	clusters, err := t.List(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range clusters {
		if c == name {
			return true, nil
		}
	}
	return false, nil
}

// majorMinor returns the installed talosctl version as "major.minor" (e.g.
// "1.13"), or "" if it cannot be determined.
func (t *Talos) majorMinor(ctx context.Context) string {
	r := runner.New(nil)
	out, err := r.Capture(ctx, "talosctl", "version", "--client")
	if err != nil {
		return ""
	}
	return parseTalosMajorMinor(out)
}

var talosTagRe = regexp.MustCompile(`Tag:\s*v?(\d+)\.(\d+)`)

func parseTalosMajorMinor(versionOutput string) string {
	m := talosTagRe.FindStringSubmatch(versionOutput)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}

// dockerNames returns the names of docker containers whose name matches the
// given substring filter.
func dockerNames(ctx context.Context, nameFilter string) ([]string, error) {
	r := runner.New(nil)
	out, err := r.Capture(ctx, engine.Current().Binary, "ps", "-a", "--filter", "name="+nameFilter, "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(out), nil
}
