package tui

import (
	"errors"
	"strings"
	"testing"
)

func uninstallItems() []UninstallItem {
	return []UninstallItem{
		{Name: "argocd", Method: "helm", Category: "gitops"},
		{Name: "local-path", Method: "-", Category: "storage", Locked: "not installed by k8slocalcli"},
		{Name: "nyancat", Method: "flux", Category: "demo"},
	}
}

// Locked rows cannot be ticked; Check failures keep the form open with the
// reason; a passing selection returns the apps with purge and force.
func TestUninstallForm(t *testing.T) {
	var checked []string
	f := newUninstallForm(UninstallOptions{
		Target: "cluster dev",
		Items:  uninstallItems(),
		Check: func(names []string, force bool) error {
			checked = names
			if !force {
				return errors.New("argocd is still needed")
			}
			return nil
		},
	})

	f.focus = f.confirmIdx()
	if done, _ := f.handleKey("<Enter>"); done || !strings.Contains(f.err, "select at least one") {
		t.Fatalf("empty selection must not close the form (err %q)", f.err)
	}

	f.focus = uninstallApps
	f.handleKey("<Enter>")
	if f.open == nil {
		t.Fatal("Enter on Apps should open the checklist")
	}
	f.handleKey("<Space>") // argocd
	f.handleKey("<Down>")
	f.handleKey("<Space>") // local-path: locked
	if !strings.HasPrefix(f.open.rows()[1], "[-]") {
		t.Errorf("locked row should show [-]: %q", f.open.rows()[1])
	}
	f.handleKey("<Escape>")
	if got := f.selected(); len(got) != 1 || got[0] != "argocd" {
		t.Fatalf("selected = %v, want [argocd] (locked rows cannot be ticked)", got)
	}

	f.focus = f.confirmIdx()
	if done, _ := f.handleKey("<Enter>"); done || !strings.Contains(f.err, "still needed") {
		t.Fatalf("a failing Check must keep the form open with its reason (err %q)", f.err)
	}
	f.focus = uninstallPurge
	f.handleKey("<Right>")
	f.focus = uninstallForce
	f.handleKey("<Right>")
	f.focus = f.confirmIdx()
	done, confirmed := f.handleKey("<Enter>")
	if !done || !confirmed {
		t.Fatal("a passing Check should close the form confirmed")
	}
	res := f.result()
	if len(checked) != 1 || !res.Purge || !res.Force || res.Apps[0] != "argocd" {
		t.Errorf("result = %+v (checked %v)", res, checked)
	}
}

// Rows align their columns; the option column only appears when some row
// has a choice of options.
func TestChecklistRows(t *testing.T) {
	c := &checklist{
		title:  func() string { return "x" },
		items:  []checkItem{{columns: []string{"a", "one"}, options: []string{"on"}}, {columns: []string{"longer", "two"}, options: []string{"on"}}},
		choice: []int{1, 0},
	}
	rows := c.rows()
	if rows[0] != "[x] a      one" || rows[1] != "[ ] longer two" {
		t.Errorf("rows = %q", rows)
	}
	c.items[0].options = []string{"helm", "flux"}
	if rows := c.rows(); !strings.HasPrefix(rows[0], "[x] helm") {
		t.Errorf("with options the chosen one is shown: %q", rows[0])
	}
}
