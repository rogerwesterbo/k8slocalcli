package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PodSecurityFinding is a namespace whose pods Pod Security admission rejects,
// or will reject.
type PodSecurityFinding struct {
	Namespace string
	// App is the catalog app the namespace belongs to, or "" for a namespace
	// k8slocalcli knows nothing about.
	App string
	// Problem says what was found, e.g. `replicaset/x: host namespaces
	// (hostNetwork=true)` or "missing the privileged labels it needs".
	Problem string
}

// Known reports whether the finding is a catalog app k8slocalcli installed:
// its namespace needs the privileged level by design, so fixing it is safe.
// Other namespaces lower their security when relaxed, so they need a human
// yes.
func (f PodSecurityFinding) Known() bool { return f.App != "" }

// PodSecurityFindings looks for Pod Security problems in the cluster:
//   - privileged catalog apps k8slocalcli installed whose namespace lacks the
//     labels (installed by an older k8slocalcli, or labels removed);
//   - any namespace where a controller failed to create pods because they
//     violate the enforced Pod Security level (FailedCreate events).
//
// Talos enforces "baseline" on every namespace but kube-system, so this is
// where its "my app never starts" problems surface; other hardened clusters
// do the same.
func PodSecurityFindings(ctx context.Context, env *Env) []PodSecurityFinding {
	var out []PodSecurityFinding
	seen := map[string]bool{}
	for _, a := range catalogPodSecurityGaps(ctx, env) {
		out = append(out, PodSecurityFinding{Namespace: a.Namespace, App: a.Name, Problem: "missing the privileged Pod Security labels it needs"})
		seen[a.Namespace] = true
	}
	for _, f := range rejectedByPodSecurity(ctx, env) {
		if seen[f.Namespace] || env.namespacePrivileged(ctx, f.Namespace) {
			continue // fixed above, or already relaxed (the event is old)
		}
		out = append(out, f)
	}
	return out
}

// FixPodSecurity labels the finding's namespace for privileged pods.
func FixPodSecurity(ctx context.Context, env *Env, f PodSecurityFinding) error {
	return env.ensurePrivilegedNamespace(ctx, f.Namespace)
}

// catalogPodSecurityGaps returns the privileged catalog apps k8slocalcli
// installed whose namespace does not enforce the privileged level.
func catalogPodSecurityGaps(ctx context.Context, env *Env) []App {
	return scanCatalog(ctx, func(ctx context.Context, a App) bool {
		if !a.Privileged || env.namespacePrivileged(ctx, a.Namespace) {
			return false
		}
		return DetectInstallation(ctx, env, a).State == InstalledByUs
	})
}

// repairPodSecurity fixes catalogPodSecurityGaps before installing, so a new
// app does not fail far from the cause (on Talos, a new app's PVC stays
// Pending because local-path's helper pod in local-path-storage is rejected).
func repairPodSecurity(ctx context.Context, env *Env) {
	for _, a := range catalogPodSecurityGaps(ctx, env) {
		if err := env.ensurePrivilegedNamespace(ctx, a.Namespace); err == nil {
			fmt.Fprintf(env.Out, "🔧 Labelled namespace %s (%s) for privileged pods — it was installed without the Pod Security labels it needs\n", a.Namespace, a.Name)
		}
	}
}

// rejectedByPodSecurity reads FailedCreate events whose message is a Pod
// Security rejection, one finding per namespace (listing each controller).
func rejectedByPodSecurity(ctx context.Context, env *Env) []PodSecurityFinding {
	out, err := env.kubectlQuiet(ctx, "get", "events", "-A", "--field-selector", "reason=FailedCreate", "-o", "json")
	if err != nil {
		return nil
	}
	return parsePodSecurityEvents([]byte(out))
}

// parsePodSecurityEvents turns `kubectl get events -o json` into findings.
func parsePodSecurityEvents(data []byte) []PodSecurityFinding {
	var events struct {
		Items []struct {
			Message        string `json:"message"`
			InvolvedObject struct {
				Kind      string `json:"kind"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"involvedObject"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &events); err != nil {
		return nil
	}
	problems := map[string]map[string]bool{} // namespace -> "kind/name: rules"
	for _, e := range events.Items {
		_, rules, ok := strings.Cut(e.Message, "violates PodSecurity ")
		if !ok {
			continue
		}
		// `"baseline:latest": host namespaces (hostNetwork=true)` → the rules.
		if _, after, found := strings.Cut(rules, ": "); found {
			rules = after
		}
		obj := e.InvolvedObject
		key := strings.ToLower(obj.Kind) + "/" + obj.Name + ": " + rules
		if problems[obj.Namespace] == nil {
			problems[obj.Namespace] = map[string]bool{}
		}
		problems[obj.Namespace][key] = true
	}
	var out []PodSecurityFinding
	for ns, set := range problems {
		var lines []string
		for l := range set {
			lines = append(lines, l)
		}
		sort.Strings(lines)
		out = append(out, PodSecurityFinding{Namespace: ns, App: catalogAppIn(ns), Problem: strings.Join(lines, "; ")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace < out[j].Namespace })
	return out
}

// catalogAppIn returns the privileged catalog app that owns ns, or "".
// Only privileged apps count: for any other app a rejection is unexpected and
// must not be "fixed" silently.
func catalogAppIn(ns string) string {
	for _, a := range catalog {
		if a.Privileged && a.Namespace == ns {
			return a.Name
		}
	}
	return ""
}
