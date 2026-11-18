package cli

import (
	"strings"
	"testing"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// create refuses an app that cannot work on this machine before building
// anything, and lets everything else through.
func TestRefuseUnavailableApps(t *testing.T) {
	unavailable := map[string]string{"kubevirt": "no node can run VMs"}
	err := refuseUnavailableApps([]cluster.AppRequest{{Name: "nyancat"}, {Name: "kubevirt"}}, unavailable)
	if err == nil || !strings.Contains(err.Error(), "kubevirt cannot work on this machine") || !strings.Contains(err.Error(), "no node can run VMs") {
		t.Errorf("err = %v, want kubevirt refused with its reason", err)
	}
	if err := refuseUnavailableApps([]cluster.AppRequest{{Name: "cdi"}}, unavailable); err != nil {
		t.Errorf("cdi works without kubevirt: %v", err)
	}
	if err := refuseUnavailableApps([]cluster.AppRequest{{Name: "kubevirt"}}, nil); err != nil {
		t.Errorf("unknown machine: %v", err)
	}
}
