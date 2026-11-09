// Package tui implements the interactive terminal UI used to collect cluster
// parameters (name, provider, nodes, Kubernetes version, CNI, host exposure and
// apps) before creation. It is built on github.com/gizak/termui/v3.
package tui

import (
	"fmt"
	"strings"

	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"

	"github.com/rogerwesterbo/k8slocalcli/internal/apps"
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
	fieldApp     // one per catalog app: off / install method
	fieldAppHost // one per exposable catalog app: hostname
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
	app   int  // catalog index for fieldApp/fieldAppHost
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

	catalog []apps.App
	// appChoice[i] is 0 for "off", or 1+index into catalog[i].Methods.
	appChoice []int
	appHost   []string

	fields     []*field
	title      *widgets.Paragraph
	showBanner bool
	createBtn  *widgets.Paragraph
	cancelBtn  *widgets.Paragraph
	help       *widgets.Paragraph
	status     *widgets.Paragraph
}

// Run launches the interactive form, pre-filled from initial, and returns the
// collected Spec. versions provides the selectable Kubernetes versions per
// provider (newest first). It blocks until the user confirms or cancels.
func Run(initial cluster.Spec, versions map[cluster.Provider][]string) (Result, error) {
	if err := ui.Init(); err != nil {
		return Result{}, fmt.Errorf("could not start the terminal UI (are you on an interactive terminal?): %w", err)
	}
	defer ui.Close()

	f := newForm(initial, versions)
	f.layout()
	f.render()

	for e := range ui.PollEvents() {
		switch e.Type {
		case ui.ResizeEvent:
			ui.Clear()
			f.layout()
			f.render()
			continue
		case ui.KeyboardEvent:
			done, res, err := f.handleKey(e.ID)
			if err != nil {
				return Result{}, err
			}
			if done {
				return res, nil
			}
			f.render()
		}
	}
	return Result{}, nil
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

	f := &form{spec: initial, versions: versions, catalog: apps.Catalog()}

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

	// App selections, pre-filled from any --app flags.
	f.appChoice = make([]int, len(f.catalog))
	f.appHost = make([]string, len(f.catalog))
	for i, a := range f.catalog {
		if a.Expose != nil {
			f.appHost[i] = a.Expose.DefaultHost
		}
		for _, req := range initial.Apps {
			if req.Name != a.Name {
				continue
			}
			f.appChoice[i] = 1
			for j, m := range a.Methods {
				if m == req.Method {
					f.appChoice[i] = j + 1
				}
			}
			if req.Host != "" {
				f.appHost[i] = req.Host
			}
		}
	}

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
	for i, a := range f.catalog {
		f.fields = append(f.fields, &field{id: fieldApp, app: i, half: a.Expose != nil, title: " " + a.Description + " "})
		if a.Expose != nil {
			f.fields = append(f.fields, &field{id: fieldAppHost, app: i, half: true, title: " " + a.Name + " hostname "})
		}
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

	f.createBtn = widgets.NewParagraph()
	f.createBtn.WrapText = false
	f.cancelBtn = widgets.NewParagraph()
	f.cancelBtn.WrapText = false

	f.help = widgets.NewParagraph()
	f.help.Title = " Keys "
	f.help.Text = "↑/↓/Tab: move · ←/→: change · type: edit · Enter: next (Create to build) · Esc: cancel"
	f.help.TextStyle = ui.NewStyle(ui.ColorWhite)

	f.status = widgets.NewParagraph()
	f.status.Border = false

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
	case fieldAppHost:
		return exp.Enabled && f.appChoice[fl.app] > 0
	default:
		return true
	}
}

// layout positions every widget for the current terminal size.
func (f *form) layout() {
	termW, termH := ui.TerminalDimensions()
	w := formWidth
	if termW > 0 && termW-2 < w {
		w = termW - 2
	}
	x0 := formMargin
	x1 := x0 + w
	mid := x0 + w/2

	rows := 0
	for i := 0; i < len(f.fields); i++ {
		if f.fields[i].half && i+1 < len(f.fields) && f.fields[i+1].half {
			i++
		}
		rows++
	}
	bannerH := len(banner) + 1
	needed := formMargin + rows*fieldH + 3 + 3 + statusH
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

	// Button row: Create on the left, Cancel on the right.
	f.createBtn.SetRect(x0, y, mid, y+3)
	f.cancelBtn.SetRect(mid, y, x1, y+3)
	y += 3

	f.help.SetRect(x0, y, x1, y+3)
	y += 3
	f.status.SetRect(x0, y, x1, y+statusH)
}

// render refreshes every widget's content and draws the screen.
func (f *form) render() {
	for i, fl := range f.fields {
		fl.w.Text = f.fieldText(fl, i == f.focus)
		switch {
		case i == f.focus:
			fl.w.BorderStyle = ui.NewStyle(ui.ColorYellow)
			fl.w.TitleStyle = ui.NewStyle(ui.ColorYellow, ui.ColorClear, ui.ModifierBold)
		case !f.enabled(fl):
			fl.w.BorderStyle = ui.NewStyle(ui.ColorBlack)
			fl.w.TitleStyle = ui.NewStyle(ui.ColorWhite)
		default:
			fl.w.BorderStyle = ui.NewStyle(ui.ColorWhite)
			fl.w.TitleStyle = ui.NewStyle(ui.ColorWhite)
		}
	}

	f.createBtn.Text = fillButton("Create", f.createBtn)
	f.cancelBtn.Text = fillButton("Cancel", f.cancelBtn)
	styleButton(f.createBtn, f.focus == f.createIdx(), ui.ColorGreen)
	styleButton(f.cancelBtn, f.focus == f.cancelIdx(), ui.ColorRed)

	spec := f.buildSpec()
	switch warnings := spec.Warnings(); {
	case f.err != "":
		f.status.Text = "🛑 " + f.err
		f.status.TextStyle = ui.NewStyle(ui.ColorRed)
	case len(warnings) > 0 && !spec.Exposure.Enabled:
		f.status.Text = "⚠️  " + warnings[0]
		f.status.TextStyle = ui.NewStyle(ui.ColorYellow)
	default:
		text := summary(spec)
		if len(warnings) > 0 {
			text += "\n⚠️  " + warnings[0]
		}
		f.status.Text = text
		f.status.TextStyle = ui.NewStyle(ui.ColorGreen)
	}

	items := []ui.Drawable{f.createBtn, f.cancelBtn, f.help, f.status}
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
		case fl.id == fieldCertManagerMethod:
			return dim("(only for verified certificates)")
		default:
			return dim("(app not selected)")
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
		return options(cluster.InstallMethods, exp.ControllerMethod)
	case fieldCertManagerMethod:
		return options(cluster.InstallMethods, exp.CertManagerMethod)
	case fieldApp:
		return options(f.appOptions(fl.app), f.appOptions(fl.app)[f.appChoice[fl.app]])
	case fieldAppHost:
		host := f.appHost[fl.app]
		if focused {
			return host + "▏"
		}
		return host
	}
	return ""
}

// appOptions is "off" followed by the app's install methods.
func (f *form) appOptions(i int) []string {
	out := []string{"off"}
	for _, m := range f.catalog[i].Methods {
		out = append(out, string(m))
	}
	return out
}

// buildSpec returns the spec with the app selections applied.
func (f *form) buildSpec() cluster.Spec {
	s := f.spec
	s.Apps = nil
	for i, a := range f.catalog {
		if f.appChoice[i] == 0 {
			continue
		}
		req := cluster.AppRequest{Name: a.Name, Method: a.Methods[f.appChoice[i]-1]}
		if a.Expose != nil && s.Exposure.Enabled {
			req.Host = f.appHost[i]
		}
		s.Apps = append(s.Apps, req)
	}
	return s
}

// handleKey processes a key press. It returns done=true with a Result when the
// loop should stop.
func (f *form) handleKey(id string) (done bool, res Result, err error) {
	f.err = ""

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
			if fl.id != fieldName && fl.id != fieldAppHost {
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
		exp.ControllerMethod = cycle(cluster.InstallMethods, exp.ControllerMethod, delta)
	case fieldCertManagerMethod:
		exp.CertManagerMethod = cycle(cluster.InstallMethods, exp.CertManagerMethod, delta)
	case fieldApp:
		n := len(f.catalog[fl.app].Methods) + 1
		f.appChoice[fl.app] = ((f.appChoice[fl.app]+delta)%n + n) % n
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
	case fieldAppHost:
		if n := len(f.appHost[fl.app]); n > 0 {
			f.appHost[fl.app] = f.appHost[fl.app][:n-1]
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
	case fieldAppHost:
		if (isNameRune || r == '.') && len(f.appHost[fl.app]) < 253 {
			f.appHost[fl.app] += string(r)
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
	for _, a := range s.Apps {
		text += " · " + a.Name
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
