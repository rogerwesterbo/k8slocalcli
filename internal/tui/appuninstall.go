package tui

import "fmt"

// UninstallItem is one app installed in the target cluster.
type UninstallItem struct {
	Name        string
	Method      string // how it was installed: helm, manifest, flux, argo
	Category    string
	Description string
	// Locked, when set, says why it cannot be uninstalled (e.g. it was not
	// installed by k8slocalcli); the row is shown but cannot be ticked.
	Locked string
}

// UninstallOptions describes what the app-uninstall form offers.
type UninstallOptions struct {
	// Target is shown in the title.
	Target string
	// Items are the installed apps.
	Items []UninstallItem
	// Check validates a selection before the form closes (dependants still
	// installed, ...); a non-nil error is shown and keeps the form open.
	Check func(names []string, force bool) error
}

// UninstallResult is what the app-uninstall form returns.
type UninstallResult struct {
	Apps      []string
	Purge     bool
	Force     bool
	Confirmed bool
}

// uninstall-form fields, in order.
const (
	uninstallApps = iota
	uninstallPurge
	uninstallForce
)

// uninstallForm is the app-uninstall form: a simpleForm over a checklist of
// installed apps plus the --purge and --force choices.
type uninstallForm struct {
	*simpleForm
	opts   UninstallOptions
	choice []int
	purge  bool
	force  bool
}

// RunAppUninstall shows the app-uninstall form and blocks until the user
// confirms (after opts.Check passed) or cancels.
func RunAppUninstall(opts UninstallOptions) (UninstallResult, error) {
	f := newUninstallForm(opts)
	confirmed, err := f.run()
	if err != nil || !confirmed {
		return UninstallResult{}, err
	}
	return f.result(), nil
}

func newUninstallForm(opts UninstallOptions) *uninstallForm {
	f := &uninstallForm{opts: opts, choice: make([]int, len(opts.Items))}
	openApps := func() bool {
		f.show(f.checklist())
		return true
	}
	f.simpleForm = newSimpleForm(
		func() string { return "Uninstall apps from " + opts.Target },
		"Uninstall",
		"↑/↓: move · ←/→: change · Enter: open Apps / next · Esc: cancel",
		[]formField{
			uninstallApps: {
				title:  " Apps to uninstall ",
				text:   f.appsText,
				adjust: func(int) { openApps() },
				enter:  openApps,
			},
			uninstallPurge: {
				title: " Namespaces — delete also removes generated passwords and data volumes ",
				text: func(bool) string {
					return options([]string{"keep", "delete"}, map[bool]string{true: "delete", false: "keep"}[f.purge])
				},
				adjust: func(int) { f.purge = !f.purge },
			},
			uninstallForce: {
				title: " Apps other apps still need ",
				text: func(bool) string {
					return options([]string{"refuse", "remove anyway"}, map[bool]string{true: "remove anyway", false: "refuse"}[f.force])
				},
				adjust: func(int) { f.force = !f.force },
			},
		},
	)
	f.submit = func() string {
		names := f.selected()
		if len(names) == 0 {
			return "select at least one app (Apps → Enter)"
		}
		if opts.Check != nil {
			if err := opts.Check(names, f.force); err != nil {
				return err.Error()
			}
		}
		return ""
	}
	f.summary = f.statusText
	return f
}

// checklist lists the installed apps; ones k8slocalcli did not install are
// locked.
func (f *uninstallForm) checklist() *checklist {
	items := make([]checkItem, len(f.opts.Items))
	for i, it := range f.opts.Items {
		items[i] = checkItem{
			columns: []string{it.Name, it.Method, it.Category, it.Description},
			options: []string{"remove"},
			locked:  it.Locked,
		}
	}
	return &checklist{title: func() string { return "Installed apps" }, items: items, choice: f.choice}
}

func (f *uninstallForm) selected() []string {
	var out []string
	for i, c := range f.choice {
		if c > 0 {
			out = append(out, f.opts.Items[i].Name)
		}
	}
	return out
}

func (f *uninstallForm) result() UninstallResult {
	return UninstallResult{Apps: f.selected(), Purge: f.purge, Force: f.force, Confirmed: true}
}

func (f *uninstallForm) appsText(focused bool) string {
	return selectionText(f.selected(), focused)
}

func (f *uninstallForm) statusText() string {
	n := len(f.selected())
	if n == 0 {
		return fmt.Sprintf("%s installed. Open Apps with Enter to choose what to remove.", plural(len(f.opts.Items), "app"))
	}
	text := fmt.Sprintf("Will uninstall %s", plural(n, "app"))
	if f.purge {
		return text + " and delete their namespaces. CRDs are always kept."
	}
	return text + "; their namespaces are kept. CRDs are always kept."
}
