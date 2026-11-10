// Package tui implements the interactive terminal UI used to collect cluster
// parameters (name, provider, nodes, Kubernetes version, CNI, host exposure and
// apps) before creation. It is built on github.com/gizak/termui/v3.
package tui

import (
	"fmt"
	"strings"

	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// fieldID identifies what a form field edits.
type fieldID int

const (
	fieldName fieldID = iota
	fieldProvider
	fieldCNI
	fieldControlPlanes
	fieldWorkers
	fieldK8sVersion
	fieldExpose
	fieldTLS
	fieldChannel
	fieldGatewayMethod
	fieldCertManagerMethod
	fieldProfile // dev / production
	fieldPreset  // browse presets; Enter applies one (or "none" to clear)
	fieldApps    // opens the app picker
)

const (
	// maxControlPlanes is deliberately small: each control plane adds an etcd
	// member, and many of them on a single Docker host make kubeadm joins time
	// out (kind also adds a load balancer for >1). Odd counts are best.
	maxControlPlanes = 5
	maxWorkers       = 9  // sane upper bound for a local Docker host
	formWidth        = 76 // characters
	formMargin       = 1  // left/top margin
	fieldH           = 3
	statusH          = 4
)

// banner is the ASCII logo shown at the top of the form when there is room.
var banner = []string{
	"  _    ___      _                 _      _ _ ",
	" | |__( _ )___ | |___  __ __ _ | | __| (_) ",
	" | / /| _ (_-< | / _ \\/ _/ _` || |/ _| | | ",
	" |_\\_\\\\___/__/ |_\\___/\\__\\__,_||_|\\__|_|_| ",
	"        local kubernetes clusters · kind + talos",
}

// Result is what the form returns when the user confirms.
type Result struct {
	Spec      cluster.Spec
	Confirmed bool
}

// field is one input on the form.
type field struct {
	id    fieldID
	half  bool // shares a row with the next half-width field
	title string
	w     *widgets.Paragraph
}

// form holds the mutable UI state while the event loop runs.
type form struct {
	spec  cluster.Spec
	focus int // index into fields, or len(fields) (Create) / len(fields)+1 (Cancel)
	err   string

	// versions maps each provider to its selectable Kubernetes versions,
	// newest first (index 0 is "latest"). versionIdx points into the current
	// provider's slice.
	versions   map[cluster.Provider][]string
	versionIdx int

	// appSelection holds the preset and app choices, shared with the
	// app-install form; overlay shows their checklist.
	appSelection
	overlay

	fields     []*field
	title      *widgets.Paragraph
	showBanner bool
	formFrame
}

// Run launches the interactive form, pre-filled from initial, and returns the
// collected Spec. versions provides the selectable Kubernetes versions per
// provider (newest first). It blocks until the user confirms or cancels.
func Run(initial cluster.Spec, versions map[cluster.Provider][]string) (Result, error) {
	f := newForm(initial, versions)
	var res Result
	err := runLoop(f.layout, f.render, func(id string) (bool, error) {
		done, r, err := f.handleKey(id)
		res = r
		return done, err
	})
	return res, err
}

func newForm(initial cluster.Spec, versions map[cluster.Provider][]string) *form {
	if !initial.Provider.Valid() {
		initial.Provider = cluster.ProviderKind
	}
	if initial.ControlPlanes < 1 {
		initial.ControlPlanes = cluster.DefaultControlPlanes
	}
	if !initial.CNI.Valid() {
		initial.CNI = cluster.CNIDefault
	}
	if initial.HTTPPort == 0 {
		initial.HTTPPort = cluster.DefaultHTTPPort
	}
	if initial.HTTPSPort == 0 {
		initial.HTTPSPort = cluster.DefaultHTTPSPort
	}
	initial.Exposure = withExposureDefaults(initial.Exposure)

	f := &form{spec: initial, versions: versions, appSelection: newAppSelection(initial.Apps, nil)}
	f.onClose = func() { f.preset = f.matchingPreset() }

	// Resolve the initial version selection: honour a pre-set version, else
	// default to the latest (index 0).
	if initial.K8sVersion != "" {
		for i, v := range f.currentVersions() {
			if versionsEqual(v, initial.K8sVersion) {
				f.versionIdx = i
				break
			}
		}
	}
	f.syncVersion()

	add := func(id fieldID, title string, half bool) {
		f.fields = append(f.fields, &field{id: id, title: title, half: half})
	}
	add(fieldName, " Cluster name ", false)
	add(fieldProvider, " Provider ", true)
	add(fieldCNI, " CNI (network plugin) ", true)
	add(fieldControlPlanes, " Control planes ", true)
	add(fieldWorkers, " Workers ", true)
	add(fieldK8sVersion, " Kubernetes version ", false)
	add(fieldExpose, " Expose services to this machine? ", true)
	add(fieldTLS, " HTTPS certificates ", true)
	add(fieldChannel, " Gateway API channel ", true)
	add(fieldGatewayMethod, " Envoy Gateway via ", true)
	add(fieldCertManagerMethod, " cert-manager via ", false)
	add(fieldProfile, " App profile — dev: testing · production: replicas, persistence ", false)
	add(fieldPreset, " Preset — ←/→ browse, Enter use ", false)
	add(fieldApps, " Apps ", false)
	if !f.spec.Profile.Valid() {
		f.spec.Profile = cluster.ProfileDev
	}
	for _, fl := range f.fields {
		fl.w = widgets.NewParagraph()
		fl.w.Title = fl.title
		fl.w.WrapText = false
	}

	f.title = widgets.NewParagraph()
	f.title.Border = false
	f.title.Text = strings.Join(banner, "\n")
	f.title.TextStyle = ui.NewStyle(ui.ColorCyan)

	f.formFrame = newFormFrame("Create", "↑/↓/Tab: move · ←/→: change · type: edit · Enter: next (Create to build) · Esc: cancel")

	return f
}

// withExposureDefaults fills unset exposure fields. A zero Exposure means the
// caller built a Spec by hand; the form then starts from the defaults.
func withExposureDefaults(e cluster.Exposure) cluster.Exposure {
	if e == (cluster.Exposure{}) {
		return cluster.DefaultExposure()
	}
	d := cluster.DefaultExposure()
	if !e.Controller.Valid() {
		e.Controller = d.Controller
	}
	if !e.ControllerMethod.Valid() {
		e.ControllerMethod = d.ControllerMethod
	}
	if !e.Channel.Valid() {
		e.Channel = d.Channel
	}
	if !e.TLS.Valid() {
		e.TLS = d.TLS
	}
	if !e.CertManagerMethod.Valid() {
		e.CertManagerMethod = d.CertManagerMethod
	}
	return e
}

// focusCount is the number of focusable items: every field plus two buttons.
func (f *form) focusCount() int { return len(f.fields) + 2 }
func (f *form) createIdx() int  { return len(f.fields) }
func (f *form) cancelIdx() int  { return len(f.fields) + 1 }

// focused returns the focused field, or nil when a button is focused.
func (f *form) focused() *field {
	if f.focus < len(f.fields) {
		return f.fields[f.focus]
	}
	return nil
}

// enabled reports whether a field applies given the other answers; disabled
// fields are dimmed and skipped by navigation.
func (f *form) enabled(fl *field) bool {
	exp := f.spec.Exposure
	switch fl.id {
	case fieldTLS, fieldChannel, fieldGatewayMethod:
		return exp.Enabled
	case fieldCertManagerMethod:
		return exp.Enabled && exp.TLS == cluster.TLSVerified
	default:
		return true
	}
}

// layout positions every widget for the current terminal size.
func (f *form) layout() {
	termW, termH := ui.TerminalDimensions()
	x0, x1, mid := formBounds(termW)

	rows := 0
	for i := 0; i < len(f.fields); i++ {
		if f.fields[i].half && i+1 < len(f.fields) && f.fields[i+1].half {
			i++
		}
		rows++
	}
	bannerH := len(banner) + 1
	needed := formMargin + rows*fieldH + frameHeight
	f.showBanner = termH == 0 || termH >= needed+bannerH

	y := formMargin
	if f.showBanner {
		f.title.SetRect(x0, y, x1, y+bannerH)
		y += bannerH
	}

	for i := 0; i < len(f.fields); i++ {
		fl := f.fields[i]
		if fl.half && i+1 < len(f.fields) && f.fields[i+1].half {
			fl.w.SetRect(x0, y, mid, y+fieldH)
			f.fields[i+1].w.SetRect(mid, y, x1, y+fieldH)
			i++
		} else {
			fl.w.SetRect(x0, y, x1, y+fieldH)
		}
		y += fieldH
	}

	f.formFrame.layout(x0, x1, mid, y)
}

// render refreshes every widget's content and draws the screen.
func (f *form) render() {
	if f.renderOverlay() {
		return
	}
	for i, fl := range f.fields {
		fl.w.Text = f.fieldText(fl, i == f.focus)
		styleField(fl.w, i == f.focus, f.enabled(fl))
	}
	f.renderButtons(f.focus == f.createIdx(), f.focus == f.cancelIdx())

	spec := f.buildSpec()
	switch warnings := spec.Warnings(); {
	case len(warnings) > 0 && !spec.Exposure.Enabled:
		f.setStatus(f.err, "⚠️  "+warnings[0], ui.ColorYellow)
	default:
		text := summary(spec)
		if len(warnings) > 0 {
			text += "\n⚠️  " + warnings[0]
		}
		f.setStatus(f.err, text, ui.ColorGreen)
	}

	items := f.items()
	if f.showBanner {
		items = append(items, f.title)
	}
	for _, fl := range f.fields {
		items = append(items, fl.w)
	}
	ui.Render(items...)
}

// fieldText renders a field's current value.
func (f *form) fieldText(fl *field, focused bool) string {
	exp := f.spec.Exposure
	if !f.enabled(fl) {
		switch {
		case !exp.Enabled:
			return dim("(needs host exposure)")
		default:
			return dim("(only for verified certificates)")
		}
	}
	switch fl.id {
	case fieldName:
		name := f.spec.Name
		if focused {
			return name + "▏"
		}
		if name == "" {
			return dim("(required)")
		}
		return name
	case fieldProvider:
		return options(cluster.Providers, f.spec.Provider)
	case fieldCNI:
		return options(cluster.CNIs, f.spec.CNI)
	case fieldControlPlanes:
		return fmt.Sprintf("%d", f.spec.ControlPlanes)
	case fieldWorkers:
		return fmt.Sprintf("%d", f.spec.Workers)
	case fieldK8sVersion:
		return f.versionText()
	case fieldExpose:
		return options([]string{"yes", "no"}, yesNo(exp.Enabled))
	case fieldTLS:
		return options(cluster.TLSModes, exp.TLS)
	case fieldChannel:
		return options(cluster.GatewayChannels, exp.Channel)
	case fieldGatewayMethod:
		return options(cluster.DirectMethods, exp.ControllerMethod)
	case fieldCertManagerMethod:
		return options(cluster.DirectMethods, exp.CertManagerMethod)
	case fieldProfile:
		return options(cluster.Profiles, f.spec.Profile)
	case fieldPreset:
		return f.presetText(focused)
	case fieldApps:
		return f.appsText(focused)
	}
	return ""
}

// buildSpec returns the spec with the app selections applied.
func (f *form) buildSpec() cluster.Spec {
	s := f.spec
	s.Apps = f.requests(s.Exposure.Enabled)
	return s
}

// handleKey processes a key press. It returns done=true with a Result when the
// loop should stop.
func (f *form) handleKey(id string) (done bool, res Result, err error) {
	f.err = ""

	if f.open != nil {
		f.overlayKey(id)
		return false, Result{}, nil
	}

	switch id {
	case "<C-c>", "<Escape>":
		return true, Result{Confirmed: false}, nil

	case "<Enter>":
		return f.activate()

	case "<Tab>", "<Down>":
		f.move(+1)

	case "<Up>":
		f.move(-1)

	case "<Left>":
		f.moveOrAdjust(-1)

	case "<Right>":
		f.moveOrAdjust(+1)

	case "<Backspace>", "<C-8>":
		f.backspace()

	case "<Space>":
		if fl := f.focused(); fl != nil {
			switch fl.id {
			case fieldName:
			case fieldApps:
				f.openApps()
			case fieldPreset:
				f.applyPreset()
			default:
				f.adjust(+1)
			}
		} else {
			return f.activate()
		}

	default:
		f.typeRune(id)
	}
	return false, Result{}, nil
}

// move advances focus by delta, skipping disabled fields.
func (f *form) move(delta int) {
	n := f.focusCount()
	for range n {
		f.focus = ((f.focus+delta)%n + n) % n
		if fl := f.focused(); fl == nil || f.enabled(fl) {
			return
		}
	}
}

// activate handles Enter/Space on the focused item: submit on Create, cancel on
// Cancel, and advance to the next item on any input field (so Enter walks the
// form down to the Create button rather than submitting prematurely).
func (f *form) activate() (bool, Result, error) {
	switch f.focus {
	case f.createIdx():
		spec := f.buildSpec()
		if verr := spec.Validate(); verr != nil {
			f.err = verr.Error()
			return false, Result{}, nil
		}
		return true, Result{Spec: spec, Confirmed: true}, nil
	case f.cancelIdx():
		return true, Result{Confirmed: false}, nil
	default:
		if fl := f.focused(); fl != nil && fl.id == fieldApps {
			f.openApps()
			return false, Result{}, nil
		}
		// Enter on a preset that is not in use applies it; otherwise it
		// moves on like any field.
		if fl := f.focused(); fl != nil && fl.id == fieldPreset && f.applyPreset() {
			return false, Result{}, nil
		}
		f.move(+1)
		return false, Result{}, nil
	}
}

// moveOrAdjust handles ←/→: it changes the value of an input field, or toggles
// between the two buttons when one is focused.
func (f *form) moveOrAdjust(delta int) {
	switch f.focus {
	case f.createIdx():
		f.focus = f.cancelIdx()
	case f.cancelIdx():
		f.focus = f.createIdx()
	default:
		f.adjust(delta)
	}
}

// adjust changes the focused input field's value by delta.
func (f *form) adjust(delta int) {
	fl := f.focused()
	if fl == nil || !f.enabled(fl) {
		return
	}
	exp := &f.spec.Exposure
	switch fl.id {
	case fieldProvider:
		f.spec.Provider = cycle(cluster.Providers, f.spec.Provider, delta)
		// Different providers support different versions; reset to latest.
		f.versionIdx = 0
		f.syncVersion()
	case fieldCNI:
		f.spec.CNI = cycle(cluster.CNIs, f.spec.CNI, delta)
	case fieldControlPlanes:
		f.spec.ControlPlanes = clamp(f.spec.ControlPlanes+delta, 1, maxControlPlanes)
	case fieldWorkers:
		f.spec.Workers = clamp(f.spec.Workers+delta, 0, maxWorkers)
	case fieldK8sVersion:
		if n := len(f.currentVersions()); n > 0 {
			f.versionIdx = ((f.versionIdx+delta)%n + n) % n // wrap around
			f.syncVersion()
		}
	case fieldExpose:
		exp.Enabled = !exp.Enabled
	case fieldTLS:
		exp.TLS = cycle(cluster.TLSModes, exp.TLS, delta)
	case fieldChannel:
		exp.Channel = cycle(cluster.GatewayChannels, exp.Channel, delta)
	case fieldGatewayMethod:
		exp.ControllerMethod = cycle(cluster.DirectMethods, exp.ControllerMethod, delta)
	case fieldCertManagerMethod:
		exp.CertManagerMethod = cycle(cluster.DirectMethods, exp.CertManagerMethod, delta)
	case fieldProfile:
		f.spec.Profile = cycle(cluster.Profiles, f.spec.Profile, delta)
	case fieldPreset:
		f.browsePreset(delta)
	case fieldApps:
		f.openApps()
	}
}

// backspace deletes the last edited character of the focused field.
func (f *form) backspace() {
	fl := f.focused()
	if fl == nil {
		return
	}
	switch fl.id {
	case fieldName:
		if n := len(f.spec.Name); n > 0 {
			f.spec.Name = f.spec.Name[:n-1]
		}
	case fieldControlPlanes:
		f.spec.ControlPlanes = clamp(f.spec.ControlPlanes/10, 1, maxControlPlanes)
	case fieldWorkers:
		f.spec.Workers = f.spec.Workers / 10
	}
}

// typeRune handles a printable key for the focused field.
func (f *form) typeRune(id string) {
	if len([]rune(id)) != 1 {
		return // not a single printable rune (some control key)
	}
	r := []rune(id)[0]
	if r >= 'A' && r <= 'Z' {
		r += 'a' - 'A' // names and hostnames are lowercase
	}
	isNameRune := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'

	fl := f.focused()
	if fl == nil {
		return
	}
	switch fl.id {
	case fieldName:
		if isNameRune && len(f.spec.Name) < 63 {
			f.spec.Name += string(r)
		}
	case fieldControlPlanes:
		// Maxima are single digits, so a typed digit replaces the value
		// (typing "3" sets 3, rather than appending to the current value).
		if r >= '0' && r <= '9' {
			f.spec.ControlPlanes = clamp(int(r-'0'), 1, maxControlPlanes)
		}
	case fieldWorkers:
		if r >= '0' && r <= '9' {
			f.spec.Workers = clamp(int(r-'0'), 0, maxWorkers)
		}
	}
}

// --- version helpers ---

// currentVersions returns the selectable versions for the focused provider.
func (f *form) currentVersions() []string {
	if f.versions == nil {
		return nil
	}
	return f.versions[f.spec.Provider]
}

// syncVersion writes the currently selected version into the spec.
func (f *form) syncVersion() {
	vs := f.currentVersions()
	if len(vs) == 0 {
		f.spec.K8sVersion = ""
		return
	}
	if f.versionIdx < 0 || f.versionIdx >= len(vs) {
		f.versionIdx = 0
	}
	f.spec.K8sVersion = vs[f.versionIdx]
}

func (f *form) versionText() string {
	vs := f.currentVersions()
	if len(vs) == 0 {
		return dim("(provider default)")
	}
	v := vs[f.versionIdx]
	if f.versionIdx == 0 {
		return v + "   " + dim("(latest)")
	}
	return v
}

// versionsEqual compares two version strings ignoring a leading "v".
func versionsEqual(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// --- small rendering helpers ---

// options renders every option with the selected one bracketed.
func options[T ~string](opts []T, selected T) string {
	parts := make([]string, len(opts))
	for i, opt := range opts {
		if opt == selected {
			parts[i] = "‹ " + string(opt) + " ›"
		} else {
			parts[i] = dim(string(opt))
		}
	}
	return strings.Join(parts, "  ")
}

// cycle returns the next/previous option after cur, wrapping around.
func cycle[T comparable](opts []T, cur T, delta int) T {
	n := len(opts)
	idx := 0
	for i, opt := range opts {
		if opt == cur {
			idx = i
			break
		}
	}
	return opts[((idx+delta)%n+n)%n]
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func summary(s cluster.Spec) string {
	name := s.Name
	if name == "" {
		name = "<name>"
	}
	ver := s.K8sVersion
	if ver == "" {
		ver = "latest"
	}
	cni := s.CNI
	if cni == "" {
		cni = cluster.CNIDefault
	}
	text := fmt.Sprintf("Will create: %s/%s · k8s %s · %s CNI · %d cp · %d worker(s)",
		s.Provider, name, ver, cni, s.ControlPlanes, s.Workers)
	if s.Exposure.Enabled {
		text += fmt.Sprintf(" · %s gateway (%s, %s TLS)", s.Exposure.Controller, s.Exposure.Channel, s.Exposure.TLS)
	}
	if len(s.Apps) > 0 {
		text += fmt.Sprintf(" · %s apps:", s.Profile)
		for _, a := range s.Apps {
			text += " " + a.Name
		}
	}
	return text
}

// styleButton colours a button paragraph; the focused button is filled with the
// accent colour so it visibly looks pressed/selected.
func styleButton(p *widgets.Paragraph, focused bool, accent ui.Color) {
	if focused {
		p.BorderStyle = ui.NewStyle(accent, ui.ColorClear, ui.ModifierBold)
		p.TextStyle = ui.NewStyle(ui.ColorBlack, accent, ui.ModifierBold)
	} else {
		p.BorderStyle = ui.NewStyle(ui.ColorWhite)
		p.TextStyle = ui.NewStyle(accent)
	}
}

// fillButton centres a label across the button's inner width so the focused
// background colour spans the whole button.
func fillButton(label string, p *widgets.Paragraph) string {
	w := p.Inner.Dx()
	if w <= len(label) {
		return label
	}
	total := w - len(label)
	left := total / 2
	return strings.Repeat(" ", left) + label + strings.Repeat(" ", total-left)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// dim wraps text in termui's dim style markup.
func dim(s string) string {
	return fmt.Sprintf("[%s](fg:white)", s)
}

// openApps shows the app checklist.
func (f *form) openApps() {
	f.show(f.checklist(func() string { return fmt.Sprintf("Apps — %s profile", f.spec.Profile) }))
}
