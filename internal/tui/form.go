// Package tui implements the interactive terminal UI used to collect cluster
// parameters (provider, name, control planes, workers, Kubernetes version)
// before creation. It is built on github.com/gizak/termui/v3.
package tui

import (
	"fmt"
	"strings"

	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// field indices, in tab order.
const (
	fieldProvider = iota
	fieldName
	fieldControlPlanes
	fieldWorkers
	fieldK8sVersion
	fieldCount
)

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

	title  *widgets.Paragraph
	fields [fieldCount]*widgets.Paragraph
	help   *widgets.Paragraph
	status *widgets.Paragraph
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

	f.help = widgets.NewParagraph()
	f.help.Title = " Keys "
	f.help.Text = "↑/↓ or Tab: move   ←/→: change value   type: edit   Enter: create   Esc: cancel"
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

	f.help.SetRect(x0, y, x1, y+3)
	y += 3
	f.status.SetRect(x0, y, x1, y+2)
}

// render refreshes every widget's content and draws the screen.
func (f *form) render() {
	// Provider field: show both options, mark the selected one.
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

	if f.err != "" {
		f.status.Text = "🛑 " + f.err
		f.status.TextStyle = ui.NewStyle(ui.ColorRed)
	} else {
		f.status.Text = summary(f.spec)
		f.status.TextStyle = ui.NewStyle(ui.ColorGreen)
	}

	drawables := []ui.Drawable{f.title, f.help, f.status}
	for _, p := range f.fields {
		drawables = append(drawables, p)
	}
	ui.Render(drawables...)
}

// handleKey processes a key press. It returns done=true with a Result when the
// loop should stop.
func (f *form) handleKey(id string) (done bool, res Result, err error) {
	f.err = ""

	switch id {
	case "<C-c>", "<Escape>":
		return true, Result{Confirmed: false}, nil

	case "<Enter>":
		if verr := f.spec.Validate(); verr != nil {
			f.err = verr.Error()
			return false, Result{}, nil
		}
		return true, Result{Spec: f.spec, Confirmed: true}, nil

	case "<Tab>", "<Down>":
		f.focus = (f.focus + 1) % fieldCount
		return false, Result{}, nil

	case "<Up>":
		f.focus = (f.focus - 1 + fieldCount) % fieldCount
		return false, Result{}, nil

	case "<Left>":
		f.adjust(-1)
		return false, Result{}, nil

	case "<Right>":
		f.adjust(+1)
		return false, Result{}, nil

	case "<Backspace>", "<C-8>":
		f.backspace()
		return false, Result{}, nil

	case "<Space>":
		// Space toggles the provider; ignored elsewhere (names have no spaces).
		if f.focus == fieldProvider {
			f.adjust(+1)
		}
		return false, Result{}, nil

	default:
		f.typeRune(id)
		return false, Result{}, nil
	}
}

// adjust changes the focused field's value by delta (used by ←/→).
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
	return fmt.Sprintf("Will create: %s/%s · k8s %s · %d control plane(s) · %d worker(s)",
		s.Provider, name, ver, s.ControlPlanes, s.Workers)
}

func otherProvider(p cluster.Provider) cluster.Provider {
	for i, opt := range cluster.Providers {
		if opt == p {
			return cluster.Providers[(i+1)%len(cluster.Providers)]
		}
	}
	return cluster.Providers[0]
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
