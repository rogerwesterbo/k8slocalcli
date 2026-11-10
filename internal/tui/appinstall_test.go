package tui

import (
	"strings"
	"testing"

	"github.com/rogerwesterbo/k8slocalcli/internal/apps"
	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// Install refuses an empty selection, and a preset fills it.
func TestInstallFormPresetAndInstall(t *testing.T) {
	f := newInstallForm(InstallOptions{Target: "cluster dev", Routable: true})
	f.focus = f.confirmIdx()
	if done, _ := f.handleKey("<Enter>"); done || f.err == "" {
		t.Fatal("Install with nothing selected must show an error, not close")
	}

	f.focus = installPreset
	f.handleKey("<Right>")
	if f.selectedCount() != 0 {
		t.Fatal("browsing a preset must not select its apps")
	}
	f.handleKey("<Enter>")
	f.focus = f.confirmIdx()
	done, res := f.handleKey("<Enter>")
	if !done || !res.Confirmed {
		t.Fatal("Install should close the form with a confirmed result")
	}
	want := apps.Presets()[0].Apps
	if len(res.Apps) != len(want) || res.Profile != cluster.ProfileDev || res.Reinstall {
		t.Errorf("result = %+v, want preset %v on dev without reinstall", res, want)
	}
}

// Profile, re-apply and the apps picker; hosts only when routable.
func TestInstallFormFields(t *testing.T) {
	f := newInstallForm(InstallOptions{Target: "kube context x", Installed: map[string]bool{"nyancat": true}})
	f.focus = installProfile
	f.handleKey("<Right>")
	f.focus = installReinstall
	f.handleKey("<Space>")

	f.focus = installApps
	f.handleKey("<Enter>")
	if f.open == nil {
		t.Fatal("Enter on Apps should open the picker")
	}
	idx := -1
	for i, a := range f.catalog {
		if a.Name == "nyancat" {
			idx = i
		}
	}
	if !strings.Contains(f.open.rows()[idx], "installed") {
		t.Errorf("installed apps should be marked: %q", f.open.rows()[idx])
	}
	for range idx {
		f.handleKey("<Down>")
	}
	f.handleKey("<Space>")
	f.handleKey("<Enter>") // closes the picker, not the form
	if f.open != nil {
		t.Fatal("Enter should close the picker")
	}

	res := f.result()
	if res.Profile != cluster.ProfileProduction || !res.Reinstall {
		t.Errorf("result = %+v, want production with reinstall", res)
	}
	if len(res.Apps) != 1 || res.Apps[0].Name != "nyancat" || res.Apps[0].Host != "" {
		t.Errorf("apps = %+v, want nyancat without a host (not routable)", res.Apps)
	}

	if done, res := f.handleKey("<Escape>"); !done || res.Confirmed {
		t.Error("Esc should cancel the form")
	}
}

// The Method field sets one method on every selected app that supports it
// (others keep theirs and are named), shows in the Apps summary, and falls
// back to "each app's own" when the checklist makes them differ.
func TestInstallFormMethod(t *testing.T) {
	f := newInstallForm(InstallOptions{Target: "cluster dev"})
	idx := map[string]int{}
	for i, a := range f.catalog {
		idx[a.Name] = i
	}
	f.appChoice[idx["nyancat"]] = 1    // helm
	f.appChoice[idx["crossplane"]] = 1 // helm; has no manifest

	if got := f.appsText(false); !strings.Contains(got, "via helm") {
		t.Errorf("apps text should show the shared method: %q", got)
	}
	f.focus = installMethod
	for f.method != "manifest" {
		f.handleKey("<Right>")
	}
	if m := f.catalog[idx["crossplane"]].Methods[f.appChoice[idx["crossplane"]]-1]; m != "helm" {
		t.Errorf("crossplane has no manifest and must keep helm, got %s", m)
	}
	if note := f.methodNote(); !strings.Contains(note, "crossplane (keeps helm)") {
		t.Errorf("note = %q", note)
	}
	// nyancat has no manifest either, so both keep helm: still one method.
	if got := f.appsText(false); !strings.Contains(got, "via helm") {
		t.Errorf("apps text = %q", got)
	}

	f.focus = installMethod
	for f.method != "flux" {
		f.handleKey("<Right>")
	}
	if got := f.appsText(false); !strings.Contains(got, "via flux") {
		t.Errorf("both support flux, want shared flux: %q", got)
	}
	// Change one app in the checklist: the field becomes "each app's own".
	f.focus = installApps
	f.handleKey("<Enter>")
	f.open.cursor = idx["nyancat"]
	f.handleKey("<Left>") // flux → the method before it (helm)
	f.handleKey("<Escape>")
	if f.method != "" {
		t.Errorf("after a per-app change the Method field should be each app's own, got %q", f.method)
	}
	if got := f.appsText(false); !strings.Contains(got, "(") {
		t.Errorf("mixed methods should be listed per app: %q", got)
	}
}
