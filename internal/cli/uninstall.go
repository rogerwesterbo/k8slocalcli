package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rogerwesterbo/k8slocalcli/internal/apps"
	"github.com/rogerwesterbo/k8slocalcli/internal/tui"
)

func newAppUninstallCmd() *cobra.Command {
	var (
		target    targetFlags
		purge     bool
		force     bool
		assumeYes bool
	)
	cmd := &cobra.Command{
		Use:     "uninstall [<app> ...]",
		Aliases: []string{"remove", "rm"},
		Short:   "Uninstall applications from a cluster",
		Long: "Uninstall catalog applications the way they were installed, worked out\n" +
			"from the cluster: an Argo CD Application or Flux HelmRelease is deleted\n" +
			"(the controller removes what it deployed), a Helm release is uninstalled,\n" +
			"and a manifest install is deleted from the same upstream manifest.\n\n" +
			"Kept unless asked: the app's namespace with its generated passwords and\n" +
			"data volumes (--purge deletes it), and CRDs (deleting one deletes every\n" +
			"resource of that kind in the cluster, so they are never removed).\n\n" +
			"Refused while something still needs the app (keycloak needs cnpg; apps\n" +
			"installed through flux/argo need the controller); --force overrides.\n" +
			"Only installs made by k8slocalcli are removed — kind's own\n" +
			"local-path-provisioner, for example, is left alone.\n\n" +
			"With no apps named, a form opens to choose them (terminal only).\n\n" +
			"Examples:\n" +
			"  k8slocalcli app uninstall                    # form: pick cluster and apps\n" +
			"  k8slocalcli app uninstall nyancat --cluster dev\n" +
			"  k8slocalcli app uninstall keycloak cnpg --cluster dev --purge\n" +
			"  k8slocalcli app rm valkey --context my-cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			ask := newPrompter(out, assumeYes)
			env, ok, err := target.resolve(ctx, out, ask)
			if err != nil || !ok {
				return err
			}
			names, opts := args, apps.UninstallOptions{Purge: purge}
			confirmed := false
			if len(names) == 0 {
				if assumeYes || !ask.interactive {
					return fmt.Errorf("name the apps to uninstall; installed in %s: %s", env.Name, installedList(apps.InstalledApps(ctx, env)))
				}
				res, err := chooseRemovals(ctx, env, force)
				if err != nil || !res.Confirmed {
					if err == nil {
						fmt.Fprintln(out, "Cancelled.")
					}
					return err
				}
				// The form's Uninstall button is the confirmation.
				names, opts.Purge, force, confirmed = res.Apps, res.Purge, res.Force, true
			}

			// Every app is checked before anything is removed.
			plan, notInstalled, err := apps.PlanUninstall(ctx, env, names, force)
			for _, n := range notInstalled {
				fmt.Fprintf(out, "⏭️  %s is not installed in %s\n", n, env.Name)
			}
			if err != nil || len(plan) == 0 {
				return err
			}

			parts := make([]string, len(plan))
			var namespaces []string
			for i, r := range plan {
				parts[i] = fmt.Sprintf("%s (%s)", r.App.Name, r.Installation.Method)
				namespaces = append(namespaces, r.App.PurgeNamespaces()...)
			}
			if !confirmed {
				ok, err := ask.confirm(fmt.Sprintf("Uninstall %s from %s?", strings.Join(parts, ", "), env.Name), false)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(out, "Cancelled.")
					return nil
				}
			}
			if !confirmed && !opts.Purge && len(namespaces) > 0 && ask.interactive && !assumeYes {
				opts.Purge, _ = ask.confirm(fmt.Sprintf("Also delete namespace %s with its generated passwords and data volumes?", strings.Join(namespaces, ", ")), false)
			}

			for _, r := range plan {
				if err := apps.Uninstall(ctx, env, r.App, r.Installation, opts); err != nil {
					return err
				}
			}
			return nil
		},
	}
	target.register(cmd, "uninstall from")
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete the app's namespace with its passwords and data volumes")
	cmd.Flags().BoolVar(&force, "force", false, "uninstall even when other apps still need it")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "answer yes to the confirmation (namespaces are only deleted with --purge)")
	return cmd
}

func installedList(installed map[string]bool) string {
	if len(installed) == 0 {
		return "none"
	}
	names := make([]string, 0, len(installed))
	for n := range installed {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// chooseRemovals shows the uninstall form for what is installed. Pressing
// Uninstall runs the same checks as the command line (PlanUninstall), so a
// blocked selection is explained in the form instead of after it closes.
func chooseRemovals(ctx context.Context, env *apps.Env, force bool) (tui.UninstallResult, error) {
	fmt.Fprintf(env.Out, "🔎 Checking which apps are installed in %s and how…\n", env.Name)
	inventory := apps.Inventory(ctx, env)
	if len(inventory) == 0 {
		return tui.UninstallResult{}, fmt.Errorf("no catalog apps are installed in %s", env.Name)
	}
	items := make([]tui.UninstallItem, len(inventory))
	for i, r := range inventory {
		items[i] = tui.UninstallItem{
			Name:        r.App.Name,
			Method:      string(r.Installation.Method),
			Category:    string(r.App.Category),
			Description: r.App.Description,
		}
		if r.Installation.State == apps.InstalledByOthers {
			items[i].Method = "-"
			items[i].Locked = "not installed by k8slocalcli, so it is left alone"
		}
	}
	return tui.RunAppUninstall(tui.UninstallOptions{
		Target: installTarget(env),
		Items:  items,
		Check: func(names []string, forceNow bool) error {
			_, _, err := apps.PlanUninstall(ctx, env, names, force || forceNow)
			return err
		},
	})
}
