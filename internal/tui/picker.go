package tui

import (
	"fmt"

	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// ClusterChoice is one selectable cluster in the delete picker.
type ClusterChoice struct {
	Provider cluster.Provider
	Name     string
}

func (c ClusterChoice) label() string {
	return fmt.Sprintf("%-7s  %s", c.Provider, c.Name)
}

// SelectClusterToDelete shows an interactive list of clusters and requires an
// explicit confirmation before returning the chosen one. ok is false when the
// user cancels (or there is nothing to choose from).
func SelectClusterToDelete(choices []ClusterChoice) (choice ClusterChoice, ok bool, err error) {
	if len(choices) == 0 {
		return ClusterChoice{}, false, nil
	}
	if err := ui.Init(); err != nil {
		return ClusterChoice{}, false, fmt.Errorf("could not start the terminal UI (are you on an interactive terminal?): %w", err)
	}
	defer ui.Close()

	list := widgets.NewList()
	list.Title = " Select a cluster to delete "
	list.Rows = make([]string, len(choices))
	for i, c := range choices {
		list.Rows[i] = c.label()
	}
	list.SelectedRowStyle = ui.NewStyle(ui.ColorBlack, ui.ColorRed)
	list.BorderStyle = ui.NewStyle(ui.ColorYellow)

	help := widgets.NewParagraph()
	help.Border = false
	help.Text = "↑/↓: move   Enter: select   Esc: cancel"

	confirm := widgets.NewParagraph()
	confirm.Title = " Confirm "
	confirm.BorderStyle = ui.NewStyle(ui.ColorRed)
	confirm.TextStyle = ui.NewStyle(ui.ColorRed)

	confirming := false

	layout := func() {
		w, _ := ui.TerminalDimensions()
		if w <= 0 || w > 80 {
			w = 80
		}
		listH := len(choices) + 2
		if listH > 16 {
			listH = 16
		}
		list.SetRect(1, 1, w-1, 1+listH)
		help.SetRect(1, 1+listH, w-1, 1+listH+2)
		confirm.SetRect(1, 1+listH+2, w-1, 1+listH+5)
	}
	render := func() {
		items := []ui.Drawable{list, help}
		if confirming {
			c := choices[list.SelectedRow]
			confirm.Text = fmt.Sprintf("Delete %s/%s? Press y to confirm, any other key to cancel.", c.Provider, c.Name)
			items = append(items, confirm)
		}
		ui.Render(items...)
	}

	layout()
	render()

	for e := range ui.PollEvents() {
		if e.Type == ui.ResizeEvent {
			layout()
			render()
			continue
		}
		if e.Type != ui.KeyboardEvent {
			continue
		}

		if confirming {
			switch e.ID {
			case "y", "Y":
				return choices[list.SelectedRow], true, nil
			default: // n/N/Esc/anything else aborts the confirmation
				confirming = false
			}
			render()
			continue
		}

		switch e.ID {
		case "<C-c>", "<Escape>", "q":
			return ClusterChoice{}, false, nil
		case "<Down>", "j":
			list.ScrollDown()
		case "<Up>", "k":
			list.ScrollUp()
		case "<Enter>":
			confirming = true
		}
		render()
	}
	return ClusterChoice{}, false, nil
}
