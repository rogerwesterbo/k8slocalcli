package apps

import (
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

func TestSummarizePods(t *testing.T) {
	pods := `{"items":[
	 {"metadata":{"name":"argocd-server-1"},"status":{"phase":"Running","containerStatuses":[{"ready":true,"state":{}}]}},
	 {"metadata":{"name":"argocd-redis-1"},"status":{"phase":"Pending","containerStatuses":[{"ready":false,"state":{"waiting":{"reason":"ImagePullBackOff","message":"tls: failed to verify certificate: x509: certificate signed by unknown authority"}}}]}},
	 {"metadata":{"name":"secret-init-1"},"status":{"phase":"Succeeded"}}
	]}`
	got := summarizePods([]byte(pods), nil)
	if !strings.HasPrefix(got.text, "1/2 pods ready · argocd-redis-1: ImagePullBackOff") {
		t.Errorf("summary = %q", got.text)
	}
	if !strings.Contains(got.text, "ca-certificates") {
		t.Errorf("an x509 pull failure should point at the CA directory: %q", got.text)
	}

	// A crash-looping pod listed first must not hide the pull failure that
	// blocks the install (kube-prometheus-stack on arm64, Nov 2026), and the
	// failure is permanent, so a rollout can stop.
	platform := `{"items":[
	 {"metadata":{"name":"kps-kube-state-metrics-1"},"status":{"phase":"Running","containerStatuses":[{"ready":false,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}},
	 {"metadata":{"name":"kps-operator-1"},"status":{"phase":"Pending","containerStatuses":[{"ready":false,"image":"quay.io/prometheus-operator/prometheus-operator:v0.95.1","state":{"waiting":{"reason":"ImagePullBackOff","message":"rpc error: code = NotFound desc = failed to pull and unpack image: no match for platform in manifest: not found"}}}]}}
	]}`
	got = summarizePods([]byte(platform), nil)
	if !strings.HasPrefix(got.text, "0/2 pods ready · kps-operator-1: ImagePullBackOff (the image has no build") {
		t.Errorf("a pull failure should win over a crash loop and explain the platform: %q", got.text)
	}
	if want := "kps-operator-1: image quay.io/prometheus-operator/prometheus-operator:v0.95.1 cannot be pulled: the image has no build"; !strings.HasPrefix(got.unpullable, want) {
		t.Errorf("unpullable = %q, want prefix %q", got.unpullable, want)
	}

	// Re-install: the old broken pod is still there while the fixed one
	// starts, so it must not stop the rollout; a new broken pod still does.
	got = summarizePods([]byte(platform), map[string]bool{"kps-operator-1": true})
	if got.unpullable != "" || !strings.Contains(got.text, "kps-operator-1: ImagePullBackOff") {
		t.Errorf("a pod from before the step should show but not count as unpullable: %+v", got)
	}
	if !slices.Contains(got.pods, "kps-operator-1") || len(got.pods) != 2 {
		t.Errorf("pods = %v", got.pods)
	}

	plain := `{"items":[{"metadata":{"name":"a"},"status":{"phase":"Pending","containerStatuses":[{"ready":false,"state":{"waiting":{"reason":"ContainerCreating"}}}]}}]}`
	if got := summarizePods([]byte(plain), nil); got.text != "0/1 pods ready · a: ContainerCreating" || got.unpullable != "" {
		t.Errorf("summary = %+v", got)
	}
	if got := summarizePods([]byte(`{"items":[]}`), nil); got.text != "waiting for pods" {
		t.Errorf("empty summary = %q", got.text)
	}
}

func TestPullCause(t *testing.T) {
	for _, tc := range []struct {
		msg       string
		hint      string // prefix
		permanent bool
	}{
		{`rpc error: code = NotFound desc = failed to pull and unpack image "docker.io/library/busybox:nope": failed to resolve reference "docker.io/library/busybox:nope": docker.io/library/busybox:nope: not found`, "the image or tag does not exist", true},
		{`no match for platform in manifest: not found`, "the image has no build", true},
		{`tls: failed to verify certificate: x509: certificate signed by unknown authority`, "x509", true},
		// Retries can fix these: never give up on them.
		{`429 Too Many Requests - Server message: toomanyrequests: You have reached your pull rate limit`, "", false},
		{`dial tcp: lookup quay.io: i/o timeout`, "", false},
	} {
		hint, permanent := pullCause(tc.msg)
		if !strings.HasPrefix(hint, tc.hint) || (tc.hint == "" && hint != "") || permanent != tc.permanent {
			t.Errorf("pullCause(%q) = %q, %v; want %q…, %v", tc.msg, hint, permanent, tc.hint, tc.permanent)
		}
	}
}

func TestRequiredTools(t *testing.T) {
	spec := cluster.DefaultSpec()
	if got := RequiredTools(spec); !slices.Contains(got, "helm") || !slices.Contains(got, "kubectl") {
		t.Errorf("default spec installs Envoy via helm: %v", got)
	}
	spec.Exposure.ControllerMethod = cluster.MethodManifest
	if got := RequiredTools(spec); slices.Contains(got, "helm") {
		t.Errorf("all-manifest spec should not need helm: %v", got)
	}
	spec.Apps = []cluster.AppRequest{{Name: "argocd", Method: cluster.MethodHelm}}
	if got := RequiredTools(spec); !slices.Contains(got, "helm") {
		t.Errorf("a helm app needs helm: %v", got)
	}
	none := cluster.DefaultSpec()
	none.Exposure.Enabled = false
	if got := RequiredTools(none); slices.Contains(got, "helm") {
		t.Errorf("no exposure, no apps, default CNI should not need helm: %v", got)
	}
}

func TestAnyLoopback(t *testing.T) {
	if !anyLoopback([]net.IPAddr{{IP: net.ParseIP("::1")}, {IP: net.ParseIP("127.0.0.1")}}) {
		t.Error("loopback answers should pass")
	}
	if anyLoopback([]net.IPAddr{{IP: net.ParseIP("192.168.1.10")}}) {
		t.Error("a LAN answer is not this machine")
	}
}

// The operator of the Nov 2026 failure started at 09:16:37, the prometheuses
// CRD was established at 09:17:33.
func TestStartedBefore(t *testing.T) {
	if !startedBefore([]string{"2026-11-13T09:16:37Z"}, "2026-11-13T09:17:33Z") {
		t.Error("an operator started before its CRD is stale")
	}
	if startedBefore([]string{"2026-11-13T09:18:00Z"}, "2026-11-13T09:17:33Z") || startedBefore(nil, "2026-11-13T09:17:33Z") {
		t.Error("an operator started after its CRD, or none at all, is fine")
	}
	if startedBefore([]string{"2026-11-13T09:16:37Z"}, "") {
		t.Error("no Established time (CRD missing) must not restart anything")
	}
}
