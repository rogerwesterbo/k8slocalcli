package tui

import (
	"fmt"
	"strings"

	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"
)

// checkItem is one row of a checklist.
type checkItem struct {
	// columns are shown after the box, aligned across rows (e.g. name,
	// category, description).
	columns []string
	// options are what a ticked row can be set to with ←/→ (an app's install
	// methods). A single option makes the row a plain on/off toggle, and the
	// option is then not shown.
	options []string
	// locked, when set, means the row cannot be changed; it is shown as the
	// reason in the help box. The row is unticked, or ticked with fixed as
	// its option when fixed is set (an app the cluster setup installs anyway).
	locked string
	fixed  string
	// hint is shown in the help box while the cursor is on the row.
	hint string
}

// checklist is a scrolling list of rows that can be ticked, drawn over the
// whole screen while it is open. It edits choice in place (0 = off, 1+ =
// index into the row's options), so the owner sees the selection as it is
// made. Forms open one from a field (Enter) when there are too many choices
// for one field each.
type checklist struct {
	title  func() string
	items  []checkItem
	choice []int
	cursor int
	// refresh, when set, updates the items after every change, for rows
	// whose state depends on the rest of the selection.
	refresh func()
}

// handleKey processes a key; it returns true when the checklist should close.
func (c *checklist) handleKey(id string) (closed bool) {
	n := len(c.items)
	switch id {
	case "<Enter>", "<Escape>", "<C-c>", "q":
		return true
	case "<Down>", "<Tab>", "j":
		c.cursor = (c.cursor + 1) % n
	case "<Up>", "k":
		c.cursor = (c.cursor - 1 + n) % n
	case "<Space>", "x":
		if c.items[c.cursor].locked != "" {
			return false
		}
		if c.choice[c.cursor] == 0 {
			c.choice[c.cursor] = 1
		} else {
			c.choice[c.cursor] = 0
		}
		c.changed()
	case "<Right>", "l":
		c.cycle(+1)
	case "<Left>", "h":
		c.cycle(-1)
	}
	return false
}

func (c *checklist) changed() {
	if c.refresh != nil {
		c.refresh()
	}
}

// cycle moves the row under the cursor through off → each option.
func (c *checklist) cycle(delta int) {
	it := c.items[c.cursor]
	if it.locked != "" {
		return
	}
	m := len(it.options) + 1
	c.choice[c.cursor] = ((c.choice[c.cursor]+delta)%m + m) % m
	c.changed()
}

// hasOptions reports whether any row offers more than one option, i.e.
// whether ←/→ and the option column mean anything.
func (c *checklist) hasOptions() bool {
	for _, it := range c.items {
		if len(it.options) > 1 {
			return true
		}
	}
	return false
}

// rows renders one aligned line per item.
func (c *checklist) rows() []string {
	widths := map[int]int{}
	for _, it := range c.items {
		for i, col := range it.columns {
			widths[i] = max(widths[i], len([]rune(col)))
		}
	}
	options := c.hasOptions()
	rows := make([]string, len(c.items))
	for r, it := range c.items {
		box, option := "[ ]", ""
		switch {
		case it.locked != "" && it.fixed != "":
			box, option = "[x]", it.fixed
		case it.locked != "":
			box = "[-]"
		case c.choice[r] > 0:
			box = "[x]"
			option = it.options[c.choice[r]-1]
		}
		var b strings.Builder
		b.WriteString(box)
		if options {
			fmt.Fprintf(&b, " %-9s", option)
		}
		for i, col := range it.columns {
			fmt.Fprintf(&b, " %-*s", widths[i], col)
		}
		rows[r] = strings.TrimRight(b.String(), " ")
	}
	return rows
}

// selectedCount is the number of rows ticked by the user (locked rows are
// not a choice).
func (c *checklist) selectedCount() int {
	n := 0
	for i, ch := range c.choice {
		if ch > 0 && c.items[i].locked == "" {
			n++
		}
	}
	return n
}

// render lays the checklist out over the whole terminal.
func (c *checklist) render(termW, termH int) []ui.Drawable {
	w := min(termW-2, 130)
	listH := max(termH-2-helpH, 5)

	list := widgets.NewList()
	list.Title = fmt.Sprintf(" %s · %d selected ", c.title(), c.selectedCount())
	list.Rows = c.rows()
	list.SelectedRow = c.cursor
	list.SelectedRowStyle = ui.NewStyle(ui.ColorBlack, ui.ColorCyan)
	list.BorderStyle = ui.NewStyle(ui.ColorYellow)
	list.WrapText = false
	list.SetRect(formMargin, formMargin, formMargin+w, formMargin+listH)

	keys := "↑/↓: move · Space: on/off"
	if c.hasOptions() {
		keys += " · ←/→: method"
	}
	keys += " · Enter/Esc: done"

	help := widgets.NewParagraph()
	help.Title = " Keys "
	help.Text = keys + "\n" + c.detail()
	help.WrapText = true
	help.SetRect(formMargin, formMargin+listH, formMargin+w, formMargin+listH+helpH)
	return []ui.Drawable{list, help}
}

// helpH is the height of the help box under the list: its border, the keys
// and up to three wrapped lines about the row under the cursor (an app's
// hint, or why it cannot be selected).
const helpH = 6

// detail says what the row under the cursor is about: why it is locked, or
// its hint.
func (c *checklist) detail() string {
	it := c.items[c.cursor]
	switch {
	case it.locked != "" && it.fixed != "":
		return "cannot be changed: " + it.locked
	case it.locked != "":
		return "cannot be selected: " + it.locked
	}
	return it.hint
}

// chooseHint follows a focused field that opens a checklist.
var chooseHint = "  " + dim("(Enter: choose)")

// selectionText summarises a checklist selection on the field that opens it:
// "3: a, b, c", or "none".
func selectionText(names []string, focused bool) string {
	hint := ""
	if focused {
		hint = chooseHint
	}
	if len(names) == 0 {
		return dim("none") + hint
	}
	return fmt.Sprintf("%d: %s", len(names), strings.Join(names, ", ")) + hint
}

// overlay owns an open checklist for a form: it routes keys to it and draws
// it instead of the form while open. Forms embed it.
type overlay struct {
	open *checklist
	// clearScreen asks the next render to wipe a just-closed checklist.
	clearScreen bool
	// onClose runs when the checklist closes (e.g. to re-match a preset).
	onClose func()
}

// show opens c over the form.
func (o *overlay) show(c *checklist) { o.open = c }

// overlayKey forwards a key to the open checklist.
func (o *overlay) overlayKey(id string) {
	if !o.open.handleKey(id) {
		return
	}
	o.open = nil
	o.clearScreen = true
	if o.onClose != nil {
		o.onClose()
	}
}

// renderOverlay draws the open checklist and reports whether it did (the
// form then draws nothing else). It also clears a just-closed one.
func (o *overlay) renderOverlay() bool {
	if o.clearScreen {
		ui.Clear()
		o.clearScreen = false
	}
	if o.open == nil {
		return false
	}
	termW, termH := ui.TerminalDimensions()
	ui.Clear()
	ui.Render(o.open.render(termW, termH)...)
	return true
}
