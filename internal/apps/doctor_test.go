package apps

import (
	"slices"
	"strings"
	"testing"
)

// FailedCreate events with a Pod Security rejection become one finding per
// namespace; other events are ignored, and only privileged catalog apps are
// "known" (safe to relax without asking).
func TestParsePodSecurityEvents(t *testing.T) {
	events := `{"items":[
	 {"message":"Error creating: pods \"x-1\" is forbidden: violates PodSecurity \"baseline:latest\": host namespaces (hostNetwork=true)",
	  "involvedObject":{"kind":"ReplicaSet","name":"x-abc","namespace":"team-a"}},
	 {"message":"Error creating: pods \"x-2\" is forbidden: violates PodSecurity \"baseline:latest\": host namespaces (hostNetwork=true)",
	  "involvedObject":{"kind":"ReplicaSet","name":"x-abc","namespace":"team-a"}},
	 {"message":"Error creating: pods \"s-0\" is forbidden: violates PodSecurity \"baseline:latest\": hostPath volumes (volume \"proc\")",
	  "involvedObject":{"kind":"DaemonSet","name":"speaker","namespace":"metallb-system"}},
	 {"message":"Error creating: pods \"q\" is forbidden: exceeded quota",
	  "involvedObject":{"kind":"ReplicaSet","name":"q","namespace":"team-b"}}
	]}`
	got := parsePodSecurityEvents([]byte(events))
	if len(got) != 2 {
		t.Fatalf("findings = %+v, want metallb-system and team-a", got)
	}
	if got[0].Namespace != "metallb-system" || !got[0].Known() || got[0].App != "metallb" {
		t.Errorf("metallb-system should be the known metallb app: %+v", got[0])
	}
	if got[1].Namespace != "team-a" || got[1].Known() {
		t.Errorf("team-a is not a catalog app: %+v", got[1])
	}
	if got[1].Problem != "replicaset/x-abc: host namespaces (hostNetwork=true)" {
		t.Errorf("duplicate events should collapse: %q", got[1].Problem)
	}
	if parsePodSecurityEvents([]byte("not json")) != nil {
		t.Error("garbage should give no findings")
	}
}

// These apps run pods that Pod Security "baseline" rejects (found by
// dry-running every chart and manifest against a baseline namespace, plus
// the pods local-path and Trivy create at runtime). Changing the list means
// re-running that audit.
func TestPrivilegedApps(t *testing.T) {
	want := []string{"kube-prometheus-stack", "local-path", "longhorn", "metallb", "nfs", "trivy-operator"}
	var got []string
	for _, a := range Catalog() {
		if a.Privileged {
			got = append(got, a.Name)
		}
	}
	slices.Sort(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("privileged apps = %v, want %v", got, want)
	}
}
