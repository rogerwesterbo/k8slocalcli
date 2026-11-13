package apps

import (
	"context"
	"encoding/json"
	"errors"
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
// what it is waiting for instead of nothing. fn gets the step's context.
func (e *Env) step(ctx context.Context, title, namespace string, fn func(ctx context.Context, se *Env) error) error {
	return e.runStep(ctx, title, namespace, false, fn)
}

// rolloutStep is step for waiting on pods to start (install, verify): it
// also stops fn as soon as a pod's image can never be pulled, instead of
// letting helm/Argo/Flux wait out the whole timeout. Only pods created during
// the step count: on a re-install the broken pods of the previous attempt
// stay until the fixed ones are ready. Not for uninstall, where a broken pod
// on its way out must not stop the removal.
func (e *Env) rolloutStep(ctx context.Context, title, namespace string, fn func(ctx context.Context, se *Env) error) error {
	return e.runStep(ctx, title, namespace, true, fn)
}

func (e *Env) runStep(ctx context.Context, title, namespace string, failFast bool, fn func(ctx context.Context, se *Env) error) error {
	rep := e.Progress
	if rep == nil {
		rep = progress.New(e.Out, false, 0)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var status progress.StatusFunc
	if namespace != "" {
		var older map[string]bool
		if failFast {
			older = map[string]bool{}
			for _, name := range e.podStatus(ctx, namespace, nil).pods {
				older[name] = true
			}
		}
		last := "" // status is polled from one goroutine
		status = func(sctx context.Context) string {
			s := e.podStatus(sctx, namespace, older)
			// Twice in a row, so one odd answer from a registry is not
			// enough to give up.
			if failFast && s.unpullable != "" && s.unpullable == last {
				cancel(&unpullableError{s.unpullable})
			}
			last = s.unpullable
			return s.text
		}
	}
	err := rep.Step(ctx, title, status, func(w io.Writer) error {
		se := *e
		se.Out = w
		return fn(ctx, &se)
	})
	var unpullable *unpullableError
	if err != nil && errors.As(context.Cause(ctx), &unpullable) {
		return unpullable
	}
	return err
}

// unpullableError stops a rollout whose pods can never start.
type unpullableError struct{ detail string }

func (e *unpullableError) Error() string { return "stopped waiting: " + e.detail }

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
	Ready bool   `json:"ready"`
	Image string `json:"image"`
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
	} `json:"state"`
}

// podSummary is the status line for a namespace's pods. unpullable is set
// when a pod not in older can never pull its image (see pullCause), e.g.
// `grafana-…: image grafana/grafana:x cannot be pulled: …`. pods lists every
// pod's name.
type podSummary struct {
	text, unpullable string
	pods             []string
}

func (e *Env) podStatus(ctx context.Context, namespace string, older map[string]bool) podSummary {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := runner.New(nil).Capture(ctx, "kubectl", "--context", e.KubeContext,
		"get", "pods", "-n", namespace, "-o", "json")
	if err != nil {
		return podSummary{}
	}
	return summarizePods([]byte(out), older)
}

// summarizePods turns a pod list into e.g.
// "5/7 pods ready · argocd-redis-…: ImagePullBackOff". An image pull failure
// is shown in preference to other problems: pods that depend on the missing
// one often crash-loop and would hide it. Known pull failures get a hint.
func summarizePods(data []byte, older map[string]bool) podSummary {
	var pods podList
	if err := json.Unmarshal(data, &pods); err != nil {
		return podSummary{}
	}
	var sum podSummary
	total, ready := 0, 0
	problem, pullProblem := "", false
	for _, p := range pods.Items {
		sum.pods = append(sum.pods, p.Metadata.Name)
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
		for _, c := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
			w := c.State.Waiting
			if w == nil || w.Reason == "" || w.Reason == "PodInitializing" {
				continue
			}
			if !isPullFailure(w.Reason) {
				if problem == "" {
					problem = p.Metadata.Name + ": " + w.Reason
				}
				continue
			}
			hint, permanent := pullCause(w.Message)
			if !pullProblem {
				problem, pullProblem = p.Metadata.Name+": "+w.Reason, true
				if hint != "" {
					problem += " (" + hint + ")"
				}
			}
			if permanent && sum.unpullable == "" && !older[p.Metadata.Name] {
				sum.unpullable = fmt.Sprintf("%s: image %s cannot be pulled: %s", p.Metadata.Name, c.Image, hint)
			}
			break
		}
	}
	if total == 0 {
		sum.text = "waiting for pods"
		return sum
	}
	sum.text = fmt.Sprintf("%d/%d pods ready", ready, total)
	if problem != "" {
		sum.text += " · " + problem
	}
	return sum
}

func isPullFailure(reason string) bool {
	return reason == "ErrImagePull" || reason == "ImagePullBackOff"
}

// pullCause explains an image pull failure message: hint names a known cause
// ("" if unknown), and permanent marks failures that kubelet's retries cannot
// fix (unlike rate limits or network blips), so a rollout can stop early.
func pullCause(msg string) (hint string, permanent bool) {
	switch {
	case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate signed by unknown authority"):
		return "x509 — behind a TLS-intercepting proxy? put its root CA in ~/.k8slocalcli/ca-certificates and re-create the cluster", true
	// containerd reports this as NotFound too, so it goes first.
	case strings.Contains(msg, "no match for platform in manifest"):
		return "the image has no build for this node's CPU architecture — a newer k8slocalcli may pin a fixed version (k8slocalcli upgrade), or set another image tag with --values", true
	case strings.Contains(msg, "code = NotFound") || strings.Contains(msg, "manifest unknown"):
		return "the image or tag does not exist", true
	}
	return "", false
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
