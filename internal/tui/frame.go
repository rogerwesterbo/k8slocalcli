package tui

import (
	"fmt"

	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"
)

// Every form in this package follows the same shape:
//
//   - a state struct with handleKey (pure logic, unit-tested without a
//     terminal), layout (positions, on start and resize) and render (content);
//   - runLoop drives it;
//   - formFrame draws the shared bottom: confirm/cancel buttons, a key-help box
//     and a status line;
//   - app choosing is the embedded appSelection with its checklist overlay.

// runLoop starts termui and feeds key presses to handle until it reports
// done. layout runs at start and on every resize; render after every event.
func runLoop(layout, render func(), handle func(id string) (done bool, err error)) error {
	if err := ui.Init(); err != nil {
		return fmt.Errorf("could not start the terminal UI (are you on an interactive terminal?): %w", err)
	}
	defer ui.Close()

	layout()
	render()
	for e := range ui.PollEvents() {
		switch e.Type {
		case ui.ResizeEvent:
			ui.Clear()
			layout()
		case ui.KeyboardEvent:
			done, err := handle(e.ID)
			if err != nil || done {
				return err
			}
		}
		render()
	}
	return nil
}

// formBounds returns a form's left and right edge and its middle column for
// a terminal termW wide (0 when unknown, e.g. in tests).
func formBounds(termW int) (x0, x1, mid int) {
	w := formWidth
	if termW > 0 && termW-2 < w {
		w = termW - 2
	}
	return formMargin, formMargin + w, formMargin + w/2
}

// frameHeight is the height formFrame needs below a form's fields.
const frameHeight = 3 + 3 + statusH

// formFrame is the bottom of every form: a confirm and a cancel button side
// by side, a key-help box, and a status line.
type formFrame struct {
	confirmLabel string
	confirm      *widgets.Paragraph
	cancel       *widgets.Paragraph
	help         *widgets.Paragraph
	status       *widgets.Paragraph
}

func newFormFrame(confirmLabel, helpText string) formFrame {
	fr := formFrame{
		confirmLabel: confirmLabel,
		confirm:      widgets.NewParagraph(),
		cancel:       widgets.NewParagraph(),
		help:         widgets.NewParagraph(),
		status:       widgets.NewParagraph(),
	}
	fr.confirm.WrapText = false
	fr.cancel.WrapText = false
	fr.help.Title = " Keys "
	fr.help.Text = helpText
	fr.help.TextStyle = ui.NewStyle(ui.ColorWhite)
	fr.status.Border = false
	return fr
}

// layout places the frame from row y, between x0 and x1 split at mid.
func (fr *formFrame) layout(x0, x1, mid, y int) {
	fr.confirm.SetRect(x0, y, mid, y+3)
	fr.cancel.SetRect(mid, y, x1, y+3)
	y += 3
	fr.help.SetRect(x0, y, x1, y+3)
	y += 3
	fr.status.SetRect(x0, y, x1, y+statusH)
}

// renderButtons fills and styles the buttons for the current focus.
func (fr *formFrame) renderButtons(confirmFocused, cancelFocused bool) {
	fr.confirm.Text = fillButton(fr.confirmLabel, fr.confirm)
	fr.cancel.Text = fillButton("Cancel", fr.cancel)
	styleButton(fr.confirm, confirmFocused, ui.ColorGreen)
	styleButton(fr.cancel, cancelFocused, ui.ColorRed)
}

// setStatus shows text in the status line; an error (when set) wins.
func (fr *formFrame) setStatus(errText, text string, color ui.Color) {
	if errText != "" {
		fr.status.Text = "🛑 " + errText
		fr.status.TextStyle = ui.NewStyle(ui.ColorRed)
		return
	}
	fr.status.Text = text
	fr.status.TextStyle = ui.NewStyle(color)
}

func (fr *formFrame) items() []ui.Drawable {
	return []ui.Drawable{fr.confirm, fr.cancel, fr.help, fr.status}
}

// styleField colours a field's border and title for its focus state.
func styleField(p *widgets.Paragraph, focused, enabled bool) {
	switch {
	case focused:
		p.BorderStyle = ui.NewStyle(ui.ColorYellow)
		p.TitleStyle = ui.NewStyle(ui.ColorYellow, ui.ColorClear, ui.ModifierBold)
	case !enabled:
		p.BorderStyle = ui.NewStyle(ui.ColorBlack)
		p.TitleStyle = ui.NewStyle(ui.ColorWhite)
	default:
		p.BorderStyle = ui.NewStyle(ui.ColorWhite)
		p.TitleStyle = ui.NewStyle(ui.ColorWhite)
	}
}
