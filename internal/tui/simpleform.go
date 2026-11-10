package tui

import (
	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"
)

// formField is one full-width field of a simpleForm. Its behaviour is given
// as closures over the owning form's state.
type formField struct {
	title string
	// text renders the value; focused is true while the field has focus.
	text func(focused bool) string
	// adjust handles ←/→ (delta -1/+1) and Space (+1). Nil: nothing to change.
	adjust func(delta int)
	// enter handles Enter; it returns true when it did something (focus
	// stays), false to move to the next field. Nil: move on.
	enter func() bool
}

// simpleForm is a heading, a column of fields, the shared formFrame below
// and a checklist overlay. Forms without a special layout are a simpleForm
// plus their own state; see appinstall.go and appuninstall.go.
type simpleForm struct {
	overlay
	formFrame
	heading func() string
	fields  []formField
	boxes   []*widgets.Paragraph
	title   *widgets.Paragraph
	focus   int // 0..len(fields)-1, then confirm, cancel
	err     string
	// submit runs when the confirm button is pressed; a non-empty problem is
	// shown in the status line and keeps the form open.
	submit func() (problem string)
	// summary is the status line while there is no error.
	summary func() string
}

func newSimpleForm(heading func() string, confirmLabel, help string, fields []formField) *simpleForm {
	f := &simpleForm{
		formFrame: newFormFrame(confirmLabel, help),
		heading:   heading,
		fields:    fields,
		title:     widgets.NewParagraph(),
		submit:    func() string { return "" },
		summary:   func() string { return "" },
	}
	f.title.Border = false
	f.title.TextStyle = ui.NewStyle(ui.ColorCyan, ui.ColorClear, ui.ModifierBold)
	for _, fl := range fields {
		p := widgets.NewParagraph()
		p.Title = fl.title
		p.WrapText = false
		f.boxes = append(f.boxes, p)
	}
	return f
}

func (f *simpleForm) confirmIdx() int { return len(f.fields) }
func (f *simpleForm) cancelIdx() int  { return len(f.fields) + 1 }

// run drives the form until it is confirmed or cancelled.
func (f *simpleForm) run() (confirmed bool, err error) {
	err = runLoop(f.layout, f.render, func(id string) (bool, error) {
		done, ok := f.handleKey(id)
		confirmed = ok
		return done, nil
	})
	return confirmed, err
}

// handleKey processes a key. done is true when the form should close;
// confirmed tells whether it closed through the confirm button.
func (f *simpleForm) handleKey(id string) (done, confirmed bool) {
	f.err = ""
	if f.open != nil {
		f.overlayKey(id)
		return false, false
	}
	n := len(f.fields) + 2
	switch id {
	case "<C-c>", "<Escape>":
		return true, false
	case "<Tab>", "<Down>":
		f.focus = (f.focus + 1) % n
	case "<Up>":
		f.focus = (f.focus - 1 + n) % n
	case "<Left>", "<Right>":
		delta := 1
		if id == "<Left>" {
			delta = -1
		}
		switch f.focus {
		case f.confirmIdx():
			f.focus = f.cancelIdx()
		case f.cancelIdx():
			f.focus = f.confirmIdx()
		default:
			if adj := f.fields[f.focus].adjust; adj != nil {
				adj(delta)
			}
		}
	case "<Space>":
		if f.focus < len(f.fields) {
			if adj := f.fields[f.focus].adjust; adj != nil {
				adj(+1)
			}
			return false, false
		}
		return f.activate()
	case "<Enter>":
		return f.activate()
	}
	return false, false
}

func (f *simpleForm) activate() (done, confirmed bool) {
	switch f.focus {
	case f.confirmIdx():
		if problem := f.submit(); problem != "" {
			f.err = problem
			return false, false
		}
		return true, true
	case f.cancelIdx():
		return true, false
	default:
		if enter := f.fields[f.focus].enter; enter == nil || !enter() {
			f.focus++
		}
	}
	return false, false
}

// layout positions every widget for the current terminal size.
func (f *simpleForm) layout() {
	termW, _ := ui.TerminalDimensions()
	x0, x1, mid := formBounds(termW)
	y := formMargin
	f.title.SetRect(x0, y, x1, y+2)
	y += 2
	for _, p := range f.boxes {
		p.SetRect(x0, y, x1, y+fieldH)
		y += fieldH
	}
	f.formFrame.layout(x0, x1, mid, y)
}

// render refreshes every widget's content and draws the screen.
func (f *simpleForm) render() {
	if f.renderOverlay() {
		return
	}
	f.title.Text = f.heading()
	for i, p := range f.boxes {
		p.Text = f.fields[i].text(i == f.focus)
		styleField(p, i == f.focus, true)
	}
	f.renderButtons(f.focus == f.confirmIdx(), f.focus == f.cancelIdx())
	f.setStatus(f.err, f.summary(), ui.ColorGreen)

	items := append(f.items(), f.title)
	for _, p := range f.boxes {
		items = append(items, p)
	}
	ui.Render(items...)
}
