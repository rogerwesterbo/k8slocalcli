package tui

import (
	"fmt"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// InstallOptions describes the cluster the app-install form installs into.
type InstallOptions struct {
	// Target is shown in the title, e.g. `cluster "dev" (kind)`.
	Target string
	// Routable reports whether exposed apps get gateway routes (hostnames are
	// then part of the requests).
	Routable bool
	// Installed marks apps already in the cluster; the checklist shows them.
	Installed map[string]bool
	// Profile pre-selects the profile; empty means dev.
	Profile cluster.Profile
}

// InstallResult is what the app-install form returns.
type InstallResult struct {
	Apps      []cluster.AppRequest
	Profile   cluster.Profile
	Reinstall bool
	Confirmed bool
}

// install-form fields, in order.
const (
	installProfile = iota
	installPreset
	installApps
	installMethod
	installReinstall
)

// installForm is the app-install form: a simpleForm over the shared
// appSelection plus a profile and a re-apply choice.
type installForm struct {
	appSelection
	*simpleForm
	opts      InstallOptions
	profile   cluster.Profile
	reinstall bool
}

// RunAppInstall shows the app-install form and blocks until the user installs
// or cancels.
func RunAppInstall(opts InstallOptions) (InstallResult, error) {
	f := newInstallForm(opts)
	confirmed, err := f.run()
	if err != nil || !confirmed {
		return InstallResult{}, err
	}
	return f.result(), nil
}

func newInstallForm(opts InstallOptions) *installForm {
	f := &installForm{appSelection: newAppSelection(nil, opts.Installed), opts: opts, profile: opts.Profile.OrDefault()}
	openApps := func() bool {
		f.show(f.checklist(func() string { return fmt.Sprintf("Apps — %s profile", f.profile) }))
		return true
	}
	f.simpleForm = newSimpleForm(
		func() string { return "Install apps into " + opts.Target },
		"Install",
		"↑/↓: move · ←/→: change · Enter: open Apps / next · Esc: cancel",
		[]formField{
			installProfile: {
				title:  " App profile — dev: testing · production: replicas, persistence ",
				text:   func(bool) string { return options(cluster.Profiles, f.profile) },
				adjust: func(d int) { f.profile = cycle(cluster.Profiles, f.profile, d) },
			},
			installPreset: {
				title:  " Preset — ←/→ browse, Enter use ",
				text:   f.presetText,
				adjust: f.browsePreset,
				enter:  f.applyPreset,
			},
			installApps: {
				title:  " Apps ",
				text:   f.appsText,
				adjust: func(int) { openApps() },
				enter:  openApps,
			},
			installMethod: {
				title:  " Install method — for every selected app that supports it ",
				text:   f.methodText,
				adjust: f.cycleMethod,
			},
			installReinstall: {
				title: " Already installed apps ",
				text: func(bool) string {
					return options([]string{"skip", "re-apply"}, map[bool]string{true: "re-apply", false: "skip"}[f.reinstall])
				},
				adjust: func(int) { f.reinstall = !f.reinstall },
			},
		},
	)
	f.onClose = func() {
		f.preset = f.matchingPreset()
		f.syncMethod()
	}
	f.submit = func() string {
		if f.selectedCount() == 0 {
			return "select at least one app (Apps → Enter) or a preset"
		}
		return ""
	}
	f.summary = f.statusText
	return f
}

func (f *installForm) result() InstallResult {
	return InstallResult{
		Apps:      f.requests(f.opts.Routable),
		Profile:   f.profile,
		Reinstall: f.reinstall,
		Confirmed: true,
	}
}

// handleKey processes a key; done is true when the form should close.
func (f *installForm) handleKey(id string) (done bool, res InstallResult) {
	done, confirmed := f.simpleForm.handleKey(id)
	if confirmed {
		return true, f.result()
	}
	return done, InstallResult{}
}

// statusText is the status line under the form.
func (f *installForm) statusText() string {
	n := f.selectedCount()
	if n == 0 {
		return "Nothing selected yet: pick a preset, or open Apps with Enter."
	}
	text := fmt.Sprintf("Will install %s with the %s profile; dependencies (e.g. cnpg for keycloak) are added automatically.", plural(n, "app"), f.profile)
	if note := f.methodNote(); note != "" {
		text += "\n" + note
	}
	return text
}

// plural renders "1 app" / "3 apps".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
