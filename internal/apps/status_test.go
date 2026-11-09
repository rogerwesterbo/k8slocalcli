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
	got := summarizePods([]byte(pods))
	if !strings.HasPrefix(got, "1/2 pods ready · argocd-redis-1: ImagePullBackOff") {
		t.Errorf("summary = %q", got)
	}
	if !strings.Contains(got, "ca-certificates") {
		t.Errorf("an x509 pull failure should point at the CA directory: %q", got)
	}

	plain := `{"items":[{"metadata":{"name":"a"},"status":{"phase":"Pending","containerStatuses":[{"ready":false,"state":{"waiting":{"reason":"ContainerCreating"}}}]}}]}`
	if got := summarizePods([]byte(plain)); got != "0/1 pods ready · a: ContainerCreating" {
		t.Errorf("summary = %q", got)
	}
	if got := summarizePods([]byte(`{"items":[]}`)); got != "waiting for pods" {
		t.Errorf("empty summary = %q", got)
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
