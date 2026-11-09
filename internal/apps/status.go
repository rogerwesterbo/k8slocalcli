package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/progress"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// step runs fn as one progress step. While it runs, the pods in namespace
// (when given) are summarised on the live line, so a long helm --wait shows
// what it is waiting for instead of nothing.
func (e *Env) step(ctx context.Context, title, namespace string, fn func(se *Env) error) error {
	rep := e.Progress
	if rep == nil {
		rep = progress.New(e.Out, false, 0)
	}
	var status progress.StatusFunc
	if namespace != "" {
		status = func(ctx context.Context) string { return e.podStatus(ctx, namespace) }
	}
	return rep.Step(ctx, title, status, func(w io.Writer) error {
		se := *e
		se.Out = w
		return fn(&se)
	})
}

// podList is the part of `kubectl get pods -o json` the status line needs.
type podList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Phase                 string            `json:"phase"`
			InitContainerStatuses []containerStatus `json:"initContainerStatuses"`
			ContainerStatuses     []containerStatus `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

type containerStatus struct {
	Ready bool `json:"ready"`
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
	} `json:"state"`
}

func (e *Env) podStatus(ctx context.Context, namespace string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := runner.New(nil).Capture(ctx, "kubectl", "--context", e.KubeContext,
		"get", "pods", "-n", namespace, "-o", "json")
	if err != nil {
		return ""
	}
	return summarizePods([]byte(out))
}

// summarizePods turns a pod list into e.g.
// "5/7 pods ready · argocd-redis-…: ImagePullBackOff". An image pull that
// fails on certificate verification gets the corporate-proxy hint, since that
// is by far the most common cause on a laptop.
func summarizePods(data []byte) string {
	var pods podList
	if err := json.Unmarshal(data, &pods); err != nil {
		return ""
	}
	total, ready := 0, 0
	problem := ""
	for _, p := range pods.Items {
		if p.Status.Phase == "Succeeded" {
			continue // completed jobs
		}
		total++
		allReady := p.Status.Phase == "Running" && len(p.Status.ContainerStatuses) > 0
		for _, c := range p.Status.ContainerStatuses {
			allReady = allReady && c.Ready
		}
		if allReady {
			ready++
			continue
		}
		if problem != "" {
			continue
		}
		for _, c := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
			if w := c.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" {
				problem = p.Metadata.Name + ": " + w.Reason
				if isPullFailure(w.Reason) && isCertError(w.Message) {
					problem += " (x509 — behind a TLS-intercepting proxy? put its root CA in ~/.k8slocalcli/ca-certificates and re-create the cluster)"
				}
				break
			}
		}
	}
	if total == 0 {
		return "waiting for pods"
	}
	s := fmt.Sprintf("%d/%d pods ready", ready, total)
	if problem != "" {
		s += " · " + problem
	}
	return s
}

func isPullFailure(reason string) bool {
	return reason == "ErrImagePull" || reason == "ImagePullBackOff"
}

func isCertError(msg string) bool {
	return strings.Contains(msg, "x509") || strings.Contains(msg, "certificate signed by unknown authority")
}

// checkHostResolves warns when host does not resolve to this machine. Some
// routers and corporate DNS servers drop public answers that point at
// 127.0.0.1 (DNS rebinding protection), which breaks *.localtest.me.
func checkHostResolves(ctx context.Context, env *Env, host string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err == nil && anyLoopback(addrs) {
		return
	}
	why := "does not resolve to 127.0.0.1 on this machine"
	if cluster.IsLocalDomain(host) {
		why += " (your router or DNS server may block localtest.me as DNS rebinding protection)"
	}
	fmt.Fprintf(env.Out, "⚠️  %s %s. Add it to /etc/hosts:\n   echo '127.0.0.1 %s' | sudo tee -a /etc/hosts\n", host, why, host)
}

func anyLoopback(addrs []net.IPAddr) bool {
	for _, a := range addrs {
		if a.IP.IsLoopback() {
			return true
		}
	}
	return false
}

// RequiredTools returns the CLIs a create with spec will run, so missing ones
// are reported before a cluster is built rather than halfway through.
func RequiredTools(spec cluster.Spec) []string {
	tools := []string{"kubectl"}
	helm := spec.CNI.Custom()
	if exp := spec.Exposure; exp.Enabled {
		helm = helm || exp.ControllerMethod == cluster.MethodHelm ||
			(exp.TLS == cluster.TLSVerified && exp.CertManagerMethod == cluster.MethodHelm)
	}
	for _, a := range spec.Apps {
		helm = helm || a.Method == cluster.MethodHelm
	}
	if helm {
		tools = append(tools, "helm")
	}
	return tools
}

// MissingTools returns the RequiredTools not found on PATH, with install
// hints.
func MissingTools(spec cluster.Spec) error {
	hints := map[string]string{
		"kubectl": "kubectl (brew install kubectl, or https://kubernetes.io/docs/tasks/tools/)",
		"helm":    "helm (brew install helm, or https://helm.sh/docs/intro/install/) — or choose the manifest install methods",
	}
	var missing []string
	for _, t := range RequiredTools(spec) {
		if !runner.LookPath(t) {
			missing = append(missing, "  - "+hints[t])
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing tools needed for this cluster:\n%s", strings.Join(missing, "\n"))
}
