package tui

import (
	"strings"
	"testing"

	"github.com/rogerwesterbo/k8slocalcli/internal/apps"
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
	if got := f.focused(); got == nil || got.id != fieldProfile {
		t.Fatalf("after expose=no, Tab should skip to the app profile, got %+v", got)
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

// The picker toggles apps (default method) and cycles their methods, and the
// selection reaches the spec with the app's default host.
func TestAppPicker(t *testing.T) {
	f := newForm(cluster.Spec{Name: "dev"}, nil)
	if len(f.buildSpec().Apps) != 0 {
		t.Fatal("apps should default to none")
	}
	focusOn(t, f, fieldApps)
	_, _, _ = f.handleKey("<Enter>")
	if f.open == nil {
		t.Fatal("Enter on Apps should open the picker")
	}
	idx := -1
	for i, a := range f.catalog {
		if a.Name == "argocd" {
			idx = i
		}
	}
	for range idx {
		_, _, _ = f.handleKey("<Down>")
	}
	_, _, _ = f.handleKey("<Space>")
	got := f.buildSpec().Apps
	if len(got) != 1 || got[0].Name != "argocd" || got[0].Method != cluster.MethodHelm || got[0].Host != "argocd.localtest.me" {
		t.Fatalf("unexpected apps: %+v", got)
	}
	_, _, _ = f.handleKey("<Right>")
	if m := f.buildSpec().Apps[0].Method; m != cluster.MethodManifest {
		t.Errorf("→ should select manifest, got %s", m)
	}
	_, _, _ = f.handleKey("<Escape>")
	if f.open != nil {
		t.Fatal("Esc should close the picker, not the form")
	}

	f.spec.Exposure.Enabled = false
	if h := f.buildSpec().Apps[0].Host; h != "" {
		t.Errorf("host should be dropped without exposure, got %q", h)
	}
}

// Browsing presets changes nothing; Enter applies the shown one and "none"
// clears it again. The profile is part of the spec.
func TestPresetAndProfile(t *testing.T) {
	f := newForm(cluster.Spec{Name: "dev"}, nil)
	if f.spec.Profile != cluster.ProfileDev {
		t.Fatalf("profile = %q, want dev", f.spec.Profile)
	}
	focusOn(t, f, fieldProfile)
	_, _, _ = f.handleKey("<Right>")
	if f.buildSpec().Profile != cluster.ProfileProduction {
		t.Errorf("→ on profile should select production")
	}
	focusOn(t, f, fieldPreset)
	_, _, _ = f.handleKey("<Right>")
	_, _, _ = f.handleKey("<Right>")
	_, _, _ = f.handleKey("<Left>")
	if n := len(f.buildSpec().Apps); n != 0 {
		t.Fatalf("browsing presets must not select apps, got %d", n)
	}
	_, _, _ = f.handleKey("<Enter>")
	if got := f.focused(); got == nil || got.id != fieldPreset {
		t.Fatal("Enter that applies a preset should stay on the field")
	}
	want := apps.Presets()[0].Apps
	got := f.buildSpec().Apps
	if len(got) != len(want) {
		t.Fatalf("preset %s: got %+v, want %v", apps.Presets()[0].Name, got, want)
	}
	if f.matchingPreset() != 1 {
		t.Errorf("selection should match preset 1")
	}
	_, _, _ = f.handleKey("<Left>") // back to "none"
	if !strings.Contains(f.presetText(true), "clear") {
		t.Errorf("none should offer to clear: %q", f.presetText(true))
	}
	_, _, _ = f.handleKey("<Enter>")
	if n := len(f.buildSpec().Apps); n != 0 {
		t.Errorf("applying none should clear the selection, %d left", n)
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
