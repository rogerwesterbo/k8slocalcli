package tui

import (
	"testing"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// focusOn moves focus to the first field with id (test helper).
func focusOn(t *testing.T, f *form, id fieldID) {
	t.Helper()
	for i, fl := range f.fields {
		if fl.id == id {
			f.focus = i
			return
		}
	}
	t.Fatalf("no field %d", id)
}

// The form opens on the cluster name, and the provider picker comes right after.
func TestFormFieldOrder(t *testing.T) {
	f := newForm(cluster.Spec{}, nil)
	if f.focused().id != fieldName {
		t.Fatalf("initial focus = %d, want fieldName", f.focused().id)
	}
	_, _, _ = f.handleKey("<Tab>")
	if f.focused().id != fieldProvider {
		t.Fatalf("focus after Tab = %d, want fieldProvider", f.focused().id)
	}
	before := f.spec.Provider
	_, _, _ = f.handleKey("<Right>")
	if f.spec.Provider == before {
		t.Errorf("→ on provider field did not change provider (still %s)", before)
	}
}

// Exposure defaults to on; turning it off skips every gateway question.
func TestExposeOffSkipsGatewayFields(t *testing.T) {
	f := newForm(cluster.Spec{}, nil)
	if !f.spec.Exposure.Enabled {
		t.Fatal("exposure should default to on")
	}
	focusOn(t, f, fieldExpose)
	_, _, _ = f.handleKey("<Right>")
	if f.spec.Exposure.Enabled {
		t.Fatal("→ should turn exposure off")
	}
	_, _, _ = f.handleKey("<Tab>")
	if got := f.focused(); got == nil || got.id != fieldApp {
		t.Fatalf("after expose=no, Tab should skip to the first app, got %+v", got)
	}
	if len(f.buildSpec().Warnings()) == 0 {
		t.Error("expose=no must produce an up-front warning")
	}
}

// cert-manager is only asked about for verified certificates.
func TestCertManagerOnlyForVerifiedTLS(t *testing.T) {
	f := newForm(cluster.Spec{}, nil)
	focusOn(t, f, fieldCertManagerMethod)
	if f.enabled(f.focused()) {
		t.Fatal("cert-manager method should be disabled for self-signed")
	}
	focusOn(t, f, fieldTLS)
	_, _, _ = f.handleKey("<Right>")
	if f.spec.Exposure.TLS != cluster.TLSVerified {
		t.Fatalf("TLS = %s, want verified", f.spec.Exposure.TLS)
	}
	focusOn(t, f, fieldCertManagerMethod)
	if !f.enabled(f.focused()) {
		t.Error("cert-manager method should be enabled for verified")
	}
}

// Selecting an app cycles off → helm → manifest, and its hostname is editable
// and carried into the spec only while exposure is on.
func TestAppSelectionAndHost(t *testing.T) {
	f := newForm(cluster.Spec{Name: "dev"}, nil)
	focusOn(t, f, fieldApp)
	if len(f.buildSpec().Apps) != 0 {
		t.Fatal("apps should default to off")
	}
	_, _, _ = f.handleKey("<Right>")
	apps := f.buildSpec().Apps
	if len(apps) != 1 || apps[0].Name != "argocd" || apps[0].Method != cluster.MethodHelm || apps[0].Host != "argocd.localtest.me" {
		t.Fatalf("unexpected apps: %+v", apps)
	}
	_, _, _ = f.handleKey("<Right>")
	if m := f.buildSpec().Apps[0].Method; m != cluster.MethodManifest {
		t.Errorf("second → should select manifest, got %s", m)
	}

	focusOn(t, f, fieldAppHost)
	for range len(".localtest.me") {
		_, _, _ = f.handleKey("<Backspace>")
	}
	for _, r := range "CD.localtest.me" {
		_, _, _ = f.handleKey(string(r))
	}
	if h := f.buildSpec().Apps[0].Host; h != "argocdcd.localtest.me" {
		t.Errorf("host = %q, want argocdcd.localtest.me", h)
	}

	f.spec.Exposure.Enabled = false
	if h := f.buildSpec().Apps[0].Host; h != "" {
		t.Errorf("host should be dropped without exposure, got %q", h)
	}
}

// --app flags pre-fill the form.
func TestFormPrefillFromSpec(t *testing.T) {
	spec := cluster.DefaultSpec()
	spec.Apps = []cluster.AppRequest{{Name: "argocd", Method: cluster.MethodManifest, Host: "cd.localtest.me"}}
	f := newForm(spec, nil)
	got := f.buildSpec().Apps
	if len(got) != 1 || got[0] != spec.Apps[0] {
		t.Errorf("prefill lost: %+v", got)
	}
}
