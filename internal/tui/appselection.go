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

// requests returns the selected apps; hostnames only when routes are possible.
func (s *appSelection) requests(withHosts bool) []cluster.AppRequest {
	var out []cluster.AppRequest
	for i, a := range s.catalog {
		if s.appChoice[i] == 0 {
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
		if s.appChoice[i] == 0 {
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
		if s.appChoice[i] == 0 {
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
		if s.appChoice[i] > 0 && !slices.Contains(a.Methods, s.method) {
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
	for _, c := range s.appChoice {
		if c > 0 {
			n++
		}
	}
	return n
}

// matchingPreset returns the preset index whose apps are exactly the current
// selection, or 0 ("custom").
func (s *appSelection) matchingPreset() int {
	selected := map[string]bool{}
	for i, a := range s.catalog {
		if s.appChoice[i] > 0 {
			selected[a.Name] = true
		}
	}
	if len(selected) == 0 {
		return 0
	}
	for i, p := range apps.Presets() {
		if len(p.Apps) != len(selected) {
			continue
		}
		all := true
		for _, a := range p.Apps {
			all = all && selected[a]
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
func (s *appSelection) appsText(focused bool) string {
	var names []string
	for i, a := range s.catalog {
		if s.appChoice[i] > 0 {
			names = append(names, a.Name)
		}
	}
	if m := s.commonMethod(); m != "" {
		return strings.Replace(selectionText(names, focused), ": ", " via "+string(m)+": ", 1)
	}
	for k, n := range names {
		for i, a := range s.catalog {
			if a.Name == n {
				names[k] = fmt.Sprintf("%s (%s)", n, a.Methods[s.appChoice[i]-1])
			}
		}
	}
	return selectionText(names, focused)
}

// checklist builds the checklist that edits the selection: one row per
// catalog app, its install methods as options.
func (s *appSelection) checklist(title func() string) *checklist {
	items := make([]checkItem, len(s.catalog))
	for i, a := range s.catalog {
		state := ""
		if s.installed[a.Name] {
			state = "installed"
		}
		options := make([]string, len(a.Methods))
		for j, m := range a.Methods {
			options[j] = string(m)
		}
		items[i] = checkItem{
			columns: []string{a.Name, string(a.Category), state, a.Description},
			options: options,
			hint:    dependencyHint(a),
		}
	}
	return &checklist{title: title, items: items, choice: s.appChoice}
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
	return strings.Join(parts, " · ")
}
