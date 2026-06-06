// Package tui implements the interactive terminal UI used to collect cluster
// parameters (provider, name, control planes, workers, Kubernetes version, CNI)
// before creation. It is built on github.com/gizak/termui/v3.
package tui

import (
	"fmt"
	"strings"

	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// Focusable items, in tab order. The first numFields are stacked input fields;
// the last two are the Create / Cancel buttons.
const (
	fieldProvider = iota
	fieldName
	fieldControlPlanes
	fieldWorkers
	fieldK8sVersion
	fieldCNI
	fieldCreate
	fieldCancel
	focusCount
)

// numFields is the count of stacked input fields ([0..fieldCNI]).
const numFields = fieldCNI + 1

const (
	// maxControlPlanes is deliberately small: each control plane adds an etcd
	// member, and many of them on a single Docker host make kubeadm joins time
	// out (kind also adds a load balancer for >1). Odd counts are best.
	maxControlPlanes = 5
	maxWorkers       = 9  // sane upper bound for a local Docker host
	formWidth        = 72 // characters
	formMargin       = 1  // left/top margin
)

// banner is the ASCII logo shown at the top of the form.
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

// form holds the mutable UI state while the event loop runs.
type form struct {
	spec  cluster.Spec
	focus int
	err   string

	// versions maps each provider to its selectable Kubernetes versions,
	// newest first (index 0 is "latest"). versionIdx points into the current
	// provider's slice.
	versions   map[cluster.Provider][]string
	versionIdx int

	title     *widgets.Paragraph
	fields    [numFields]*widgets.Paragraph
	createBtn *widgets.Paragraph
	cancelBtn *widgets.Paragraph
	help      *widgets.Paragraph
	status    *widgets.Paragraph
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

	f := &form{spec: initial, focus: fieldName, versions: versions}

	// Resolve the initial version selection: honour a pre-set version, else
	// default to the latest (index 0).
	f.versionIdx = 0
	if initial.K8sVersion != "" {
		for i, v := range f.currentVersions() {
			if versionsEqual(v, initial.K8sVersion) {
				f.versionIdx = i
				break
			}
		}
	}
	f.syncVersion()

	f.title = widgets.NewParagraph()
	f.title.Border = false
	f.title.Text = strings.Join(banner, "\n")
	f.title.TextStyle = ui.NewStyle(ui.ColorCyan)

	for i := range f.fields {
		f.fields[i] = widgets.NewParagraph()
	}
	f.fields[fieldProvider].Title = " Provider "
	f.fields[fieldName].Title = " Cluster name "
	f.fields[fieldControlPlanes].Title = " Control planes "
	f.fields[fieldWorkers].Title = " Workers "
	f.fields[fieldK8sVersion].Title = " Kubernetes version "
	f.fields[fieldCNI].Title = " CNI (network plugin) "

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

// layout positions every widget for the current terminal size.
func (f *form) layout() {
	termW, _ := ui.TerminalDimensions()
	w := formWidth
	if termW > 0 && termW-2 < w {
		w = termW - 2
	}
	x0 := formMargin
	x1 := x0 + w

	y := formMargin
	bannerH := len(banner) + 1
	f.title.SetRect(x0, y, x1, y+bannerH)
	y += bannerH

	const fieldH = 3
	for i := range f.fields {
		f.fields[i].SetRect(x0, y, x1, y+fieldH)
		y += fieldH
	}

	// Button row: Create on the left, Cancel on the right.
	mid := x0 + w/2
	f.createBtn.SetRect(x0, y, mid, y+3)
	f.cancelBtn.SetRect(mid, y, x1, y+3)
	y += 3

	f.help.SetRect(x0, y, x1, y+3)
	y += 3
	f.status.SetRect(x0, y, x1, y+2)
}

// render refreshes every widget's content and draws the screen.
func (f *form) render() {
	f.fields[fieldProvider].Text = providerText(f.spec.Provider)

	// Name field with a cursor when focused.
	name := f.spec.Name
	if f.focus == fieldName {
		name += "▏"
	}
	if f.spec.Name == "" && f.focus != fieldName {
		name = dim("(required)")
	}
	f.fields[fieldName].Text = name

	f.fields[fieldControlPlanes].Text = numberText(f.spec.ControlPlanes)
	f.fields[fieldWorkers].Text = numberText(f.spec.Workers)
	f.fields[fieldK8sVersion].Text = f.versionText()
	f.fields[fieldCNI].Text = cniText(f.spec.CNI)

	// Highlight the focused field.
	for i, p := range f.fields {
		if i == f.focus {
			p.BorderStyle = ui.NewStyle(ui.ColorYellow)
			p.TitleStyle = ui.NewStyle(ui.ColorYellow, ui.ColorClear, ui.ModifierBold)
		} else {
			p.BorderStyle = ui.NewStyle(ui.ColorWhite)
			p.TitleStyle = ui.NewStyle(ui.ColorWhite)
		}
	}

	// Buttons.
	f.createBtn.Text = fillButton("Create", f.createBtn)
	f.cancelBtn.Text = fillButton("Cancel", f.cancelBtn)
	styleButton(f.createBtn, f.focus == fieldCreate, ui.ColorGreen)
	styleButton(f.cancelBtn, f.focus == fieldCancel, ui.ColorRed)

	if f.err != "" {
		f.status.Text = "🛑 " + f.err
		f.status.TextStyle = ui.NewStyle(ui.ColorRed)
	} else {
		f.status.Text = summary(f.spec)
		f.status.TextStyle = ui.NewStyle(ui.ColorGreen)
	}

	ui.Render(f.title, f.createBtn, f.cancelBtn, f.help, f.status)
	for _, p := range f.fields {
		ui.Render(p)
	}
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
		f.focus = (f.focus + 1) % focusCount

	case "<Up>":
		f.focus = (f.focus - 1 + focusCount) % focusCount

	case "<Left>":
		f.moveOrAdjust(-1)

	case "<Right>":
		f.moveOrAdjust(+1)

	case "<Backspace>", "<C-8>":
		f.backspace()

	case "<Space>":
		switch f.focus {
		case fieldProvider, fieldCNI:
			f.adjust(+1)
		case fieldCreate, fieldCancel:
			return f.activate()
		}

	default:
		f.typeRune(id)
	}
	return false, Result{}, nil
}

// activate handles Enter/Space on the focused item: submit on Create, cancel on
// Cancel, and advance to the next item on any input field (so Enter walks the
// form down to the Create button rather than submitting prematurely).
func (f *form) activate() (bool, Result, error) {
	switch f.focus {
	case fieldCreate:
		if verr := f.spec.Validate(); verr != nil {
			f.err = verr.Error()
			return false, Result{}, nil
		}
		return true, Result{Spec: f.spec, Confirmed: true}, nil
	case fieldCancel:
		return true, Result{Confirmed: false}, nil
	default:
		f.focus = (f.focus + 1) % focusCount
		return false, Result{}, nil
	}
}

// moveOrAdjust handles ←/→: it changes the value of an input field, or toggles
// between the two buttons when one is focused.
func (f *form) moveOrAdjust(delta int) {
	switch f.focus {
	case fieldCreate:
		f.focus = fieldCancel
	case fieldCancel:
		f.focus = fieldCreate
	default:
		f.adjust(delta)
	}
}

// adjust changes the focused input field's value by delta.
func (f *form) adjust(delta int) {
	switch f.focus {
	case fieldProvider:
		f.spec.Provider = otherProvider(f.spec.Provider)
		// Different providers support different versions; reset to latest.
		f.versionIdx = 0
		f.syncVersion()
	case fieldControlPlanes:
		f.spec.ControlPlanes = clamp(f.spec.ControlPlanes+delta, 1, maxControlPlanes)
	case fieldWorkers:
		f.spec.Workers = clamp(f.spec.Workers+delta, 0, maxWorkers)
	case fieldK8sVersion:
		if n := len(f.currentVersions()); n > 0 {
			f.versionIdx = ((f.versionIdx+delta)%n + n) % n // wrap around
			f.syncVersion()
		}
	case fieldCNI:
		f.spec.CNI = cycleCNI(f.spec.CNI, delta)
	}
}

// backspace deletes the last edited character of the focused field.
func (f *form) backspace() {
	switch f.focus {
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

	switch f.focus {
	case fieldName:
		switch {
		case r >= 'A' && r <= 'Z':
			r += 'a' - 'A' // lowercase
			fallthrough
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			if len(f.spec.Name) < 63 {
				f.spec.Name += string(r)
			}
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

func providerText(p cluster.Provider) string {
	var parts []string
	for _, opt := range cluster.Providers {
		if opt == p {
			parts = append(parts, "‹ "+string(opt)+" ›")
		} else {
			parts = append(parts, dim(string(opt)))
		}
	}
	return strings.Join(parts, "   ")
}

func cniText(c cluster.CNI) string {
	var parts []string
	for _, opt := range cluster.CNIs {
		if opt == c {
			parts = append(parts, "‹ "+string(opt)+" ›")
		} else {
			parts = append(parts, dim(string(opt)))
		}
	}
	return strings.Join(parts, "   ")
}

// cycleCNI returns the next/previous CNI in cluster.CNIs, wrapping around.
func cycleCNI(c cluster.CNI, delta int) cluster.CNI {
	n := len(cluster.CNIs)
	idx := 0
	for i, opt := range cluster.CNIs {
		if opt == c {
			idx = i
			break
		}
	}
	return cluster.CNIs[((idx+delta)%n+n)%n]
}

func numberText(n int) string {
	return fmt.Sprintf("%d", n)
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
	return fmt.Sprintf("Will create: %s/%s · k8s %s · %s CNI · %d control plane(s) · %d worker(s)",
		s.Provider, name, ver, cni, s.ControlPlanes, s.Workers)
}

func otherProvider(p cluster.Provider) cluster.Provider {
	for i, opt := range cluster.Providers {
		if opt == p {
			return cluster.Providers[(i+1)%len(cluster.Providers)]
		}
	}
	return cluster.Providers[0]
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
