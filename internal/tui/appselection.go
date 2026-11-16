package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/rogerwesterbo/k8slocalcli/internal/apps"
	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// appSelection is the app-choosing state shared by the create form and the
// app-install form: a preset and one choice per catalog app, edited through
// a checklist (see checklist.go).
type appSelection struct {
	catalog []apps.App
	// appChoice[i] is 0 for "off", or 1+index into catalog[i].Methods.
	appChoice []int
	// appHost[i] is the hostname for catalog[i]: its default, or one given
	// with --app (the picker has no hostname editor; flags do that).
	appHost []string
	// preset is the preset being browsed in the Preset field: 0 for "none",
	// or 1+index into apps.Presets(). Browsing changes nothing; applying
	// (Enter) replaces the selection with it.
	preset int
	// installed marks apps already in the target cluster (app install only).
	installed map[string]bool
	// method is the Method field: one install method for every selected app
	// that supports it, or "" for each app's own (chosen in the checklist).
	method cluster.InstallMethod
	// provided returns the apps the cluster setup installs by itself (create
	// only: cert-manager for verified HTTPS). They show as ticked and locked
	// and are left out of the requests; a tick the user gave one is kept, so
	// it comes back when the setting that provides it is turned off.
	provided func() []apps.ExposureApp
}

// methodChoices are the Method field's values: each app's own, then every
// install method.
var methodChoices = append([]cluster.InstallMethod{""}, cluster.InstallMethods...)

// newAppSelection starts from the catalog defaults, pre-filled with initial
// (e.g. from --app flags).
func newAppSelection(initial []cluster.AppRequest, installed map[string]bool) appSelection {
	s := appSelection{catalog: apps.Catalog(), installed: installed}
	s.appChoice = make([]int, len(s.catalog))
	s.appHost = make([]string, len(s.catalog))
	for i, a := range s.catalog {
		if a.Expose != nil {
			s.appHost[i] = a.Expose.DefaultHost
		}
		for _, req := range initial {
			if req.Name != a.Name {
				continue
			}
			s.appChoice[i] = 1
			for j, m := range a.Methods {
				if m == req.Method {
					s.appChoice[i] = j + 1
				}
			}
			if req.Host != "" {
				s.appHost[i] = req.Host
			}
		}
	}
	s.preset = s.matchingPreset()
	return s
}

// providedApp reports whether the cluster setup installs app by itself.
func (s *appSelection) providedApp(name string) (apps.ExposureApp, bool) {
	if s.provided == nil {
		return apps.ExposureApp{}, false
	}
	for _, p := range s.provided() {
		if p.Name == name {
			return p, true
		}
	}
	return apps.ExposureApp{}, false
}

// chosen reports whether catalog[i] is selected by the user and not
// installed by the cluster setup anyway.
func (s *appSelection) chosen(i int) bool {
	if s.appChoice[i] == 0 {
		return false
	}
	_, provided := s.providedApp(s.catalog[i].Name)
	return !provided
}

// requests returns the selected apps; hostnames only when routes are possible.
func (s *appSelection) requests(withHosts bool) []cluster.AppRequest {
	var out []cluster.AppRequest
	for i, a := range s.catalog {
		if !s.chosen(i) {
			continue
		}
		req := cluster.AppRequest{Name: a.Name, Method: a.Methods[s.appChoice[i]-1]}
		if a.Expose != nil && withHosts {
			req.Host = s.appHost[i]
		}
		out = append(out, req)
	}
	return out
}

// presetName is the browsed preset, or "none".
func (s *appSelection) presetName() string {
	if s.preset == 0 {
		return "none"
	}
	return apps.Presets()[s.preset-1].Name
}

// browsePreset moves to the next/previous preset without touching the
// selection, so the presets can be looked through freely.
func (s *appSelection) browsePreset(delta int) {
	n := len(apps.Presets()) + 1
	s.preset = ((s.preset+delta)%n + n) % n
}

// presetApplied reports whether the selection is exactly the browsed preset
// ("none": nothing selected).
func (s *appSelection) presetApplied() bool {
	if s.preset == 0 {
		return s.selectedCount() == 0
	}
	return s.matchingPreset() == s.preset
}

// applyPreset replaces the selection with the browsed preset's apps (default
// method); "none" clears it. It returns false when there was nothing to
// change.
func (s *appSelection) applyPreset() bool {
	if s.presetApplied() {
		return false
	}
	want := map[string]bool{}
	if s.preset > 0 {
		for _, a := range apps.Presets()[s.preset-1].Apps {
			want[a] = true
		}
	}
	for i, a := range s.catalog {
		s.appChoice[i] = 0
		if want[a.Name] {
			s.appChoice[i] = 1
		}
	}
	s.applyMethod()
	return true
}

// cycleMethod moves the Method field and applies it to the selected apps.
func (s *appSelection) cycleMethod(delta int) {
	s.method = cycle(methodChoices, s.method, delta)
	s.applyMethod()
}

// applyMethod sets the Method field's method on every selected app that
// supports it; the others keep theirs.
func (s *appSelection) applyMethod() {
	if s.method == "" {
		return
	}
	for i, a := range s.catalog {
		if !s.chosen(i) {
			continue
		}
		if j := slices.Index(a.Methods, s.method); j >= 0 {
			s.appChoice[i] = j + 1
		}
	}
}

// syncMethod makes the Method field match the selection after per-app
// changes in the checklist: the shared method when every selected app uses
// the same one, "each app's own" otherwise.
func (s *appSelection) syncMethod() {
	s.method = s.commonMethod()
}

// commonMethod returns the method all selected apps use, or "".
func (s *appSelection) commonMethod() cluster.InstallMethod {
	var common cluster.InstallMethod
	for i, a := range s.catalog {
		if !s.chosen(i) {
			continue
		}
		m := a.Methods[s.appChoice[i]-1]
		if common != "" && m != common {
			return ""
		}
		common = m
	}
	return common
}

// methodText renders the Method field, naming selected apps that cannot use
// the chosen method (they keep their own).
func (s *appSelection) methodText(bool) string {
	labels := make([]string, len(methodChoices))
	selected := ""
	for i, m := range methodChoices {
		labels[i] = string(m)
		if m == "" {
			labels[i] = "each app's own"
		}
		if m == s.method {
			selected = labels[i]
		}
	}
	return options(labels, selected)
}

// methodNote names the selected apps that cannot use the Method field's
// method and keep their own, e.g. "no manifest for crossplane (keeps helm)";
// "" when there are none. Forms show it in their status line.
func (s *appSelection) methodNote() string {
	if s.method == "" {
		return ""
	}
	var keep []string
	for i, a := range s.catalog {
		if s.chosen(i) && !slices.Contains(a.Methods, s.method) {
			keep = append(keep, fmt.Sprintf("%s (keeps %s)", a.Name, a.Methods[s.appChoice[i]-1]))
		}
	}
	if len(keep) == 0 {
		return ""
	}
	return "no " + string(s.method) + " for " + strings.Join(keep, ", ")
}

// presetText renders the Preset field: the browsed preset, what it holds, and
// whether Enter would change anything.
func (s *appSelection) presetText(focused bool) string {
	text := "‹ " + s.presetName() + " ›"
	if s.preset > 0 {
		text += "  " + strings.Join(apps.Presets()[s.preset-1].Apps, ", ")
	}
	switch {
	case s.presetApplied():
		if s.preset > 0 {
			text += "  ✓"
		}
	case !focused:
	case s.preset == 0:
		text += fmt.Sprintf("  (Enter: clear %d selected)", s.selectedCount())
	default:
		text += "  (Enter: use)"
	}
	return text
}

func (s *appSelection) selectedCount() int {
	n := 0
	for i := range s.catalog {
		if s.chosen(i) {
			n++
		}
	}
	return n
}

// matchingPreset returns the preset index whose apps are exactly the current
// selection, or 0 ("custom"). Apps the cluster setup provides count as
// selected when the preset has them, and are not missing when it does not.
func (s *appSelection) matchingPreset() int {
	chosen := map[string]bool{}
	for i, a := range s.catalog {
		if s.chosen(i) {
			chosen[a.Name] = true
		}
	}
	if len(chosen) == 0 {
		return 0
	}
	for i, p := range apps.Presets() {
		inPreset := map[string]bool{}
		all := true
		for _, a := range p.Apps {
			inPreset[a] = true
			_, provided := s.providedApp(a)
			all = all && (chosen[a] || provided)
		}
		for a := range chosen {
			all = all && inPreset[a]
		}
		if all {
			return i + 1
		}
	}
	return 0
}

// appsText summarises the selection on the Apps field with each app's
// method: "4 via helm: a, b, …" when they share one, "a (helm), b (flux)"
// otherwise.
//
// Apps installed without being selected follow: "+ cert-manager (verified
// HTTPS), argocd (for nyancat)", so the field shows everything that will be
// installed.
func (s *appSelection) appsText(focused bool) string {
	var names []string
	for i, a := range s.catalog {
		if s.chosen(i) {
			names = append(names, a.Name)
		}
	}
	common := s.commonMethod()
	if common == "" {
		for k, n := range names {
			for i, a := range s.catalog {
				if a.Name == n {
					names[k] = fmt.Sprintf("%s (%s)", n, a.Methods[s.appChoice[i]-1])
				}
			}
		}
	}
	text := selectionText(names, false)
	if common != "" {
		text = strings.Replace(text, ": ", " via "+string(common)+": ", 1)
	}
	if extra := s.alsoInstalled(); len(extra) > 0 {
		text += " + " + strings.Join(extra, ", ")
	}
	if focused {
		text += chooseHint
	}
	return text
}

// alsoInstalled lists, in catalog order, the apps installed without being
// selected, with why: "cert-manager (verified HTTPS)", "argocd (for
// nyancat)". Dependencies already in the cluster are left alone, so they are
// not listed.
func (s *appSelection) alsoInstalled() []string {
	deps := s.dependencies()
	var out []string
	for _, a := range s.catalog {
		if p, ok := s.providedApp(a.Name); ok {
			out = append(out, fmt.Sprintf("%s (%s)", a.Name, p.Reason))
		} else if d, ok := deps[a.Name]; ok && !s.installed[a.Name] {
			out = append(out, fmt.Sprintf("%s (for %s)", a.Name, d.By))
		}
	}
	return out
}

// dependencies are the apps the planner adds for the selection (the GitOps
// controller of an argo/flux method, cnpg for keycloak), by name.
func (s *appSelection) dependencies() map[string]apps.Dependency {
	_, added, err := apps.Plan(s.requests(false))
	if err != nil {
		return nil
	}
	out := make(map[string]apps.Dependency, len(added))
	for _, d := range added {
		out[d.Name] = d
	}
	return out
}

// checklist builds the checklist that edits the selection: one row per
// catalog app, its install methods as options. The state column says why an
// app is there without being ticked: installed already, included by the
// cluster setup (locked), or added for a selected app.
func (s *appSelection) checklist(title func() string) *checklist {
	items := make([]checkItem, len(s.catalog))
	for i, a := range s.catalog {
		options := make([]string, len(a.Methods))
		for j, m := range a.Methods {
			options[j] = string(m)
		}
		items[i] = checkItem{options: options}
	}
	c := &checklist{title: title, items: items, choice: s.appChoice}
	c.refresh = func() { s.refreshItems(c.items) }
	c.refresh()
	return c
}

// refreshItems sets each row's state, lock and hint from the selection.
func (s *appSelection) refreshItems(items []checkItem) {
	deps := s.dependencies()
	for i, a := range s.catalog {
		it := &items[i]
		state, hint := "", dependencyHint(a)
		it.locked, it.fixed = "", ""
		if p, ok := s.providedApp(a.Name); ok {
			state = "included"
			it.locked = fmt.Sprintf("%s installs it via %s", p.Reason, p.Method)
			it.fixed = string(p.Method)
		} else if s.installed[a.Name] {
			state = "installed"
		} else if d, ok := deps[a.Name]; ok {
			state = "added"
			hint = joinHints("added automatically: "+d.By+" needs it", hint)
		}
		it.columns = []string{a.Name, string(a.Category), state, a.Description}
		it.hint = hint
	}
}

func joinHints(hints ...string) string {
	var out []string
	for _, h := range hints {
		if h != "" {
			out = append(out, h)
		}
	}
	return strings.Join(out, " · ")
}

// dependencyHint tells what selecting an app also brings in.
func dependencyHint(a apps.App) string {
	var parts []string
	if len(a.Requires) > 0 {
		parts = append(parts, a.Name+" needs "+strings.Join(a.Requires, ", ")+" (added automatically)")
	}
	if slices.Contains(a.Methods, cluster.MethodFlux) {
		parts = append(parts, "flux → adds Flux", "argo → adds Argo CD")
	}
	return joinHints(parts...)
}
