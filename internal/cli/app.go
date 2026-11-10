package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rogerwesterbo/k8slocalcli/internal/apps"
	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/tui"
)

func newAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "List, install and uninstall applications in a cluster",
	}
	cmd.AddCommand(newAppListCmd(), newAppInstallCmd(), newAppUninstallCmd())
	return cmd
}

func newInfoCmd() *cobra.Command {
	var providerFlag string
	cmd := &cobra.Command{
		Use:   "info [name]",
		Short: "Show a cluster's URLs, installed apps and their credentials",
		Long: "Show how to reach a running cluster: its kubectl context, gateway URLs,\n" +
			"and every installed catalog app with its URL and login.\n\n" +
			"Everything is read live from the cluster and Docker, nothing is stored\n" +
			"on disk, so it stays correct after passwords change or apps move.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			p, name, ok, err := pickCluster(ctx, name, providerFlag, out)
			if err != nil || !ok {
				return err
			}
			env := newEnv(localTarget(ctx, p, name, nil), out, newPrompter(out, false))
			fmt.Fprintf(out, "Cluster:  %s (%s) · kubectl context %s\n", name, p.Name(), p.Context(name))
			apps.ShowInfo(ctx, env)
			return nil
		},
	}
	cmd.Flags().StringVar(&providerFlag, "provider", "", "provider of the cluster (auto-detected if omitted)")
	return cmd
}

func newAppListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List installable applications and presets",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
			fmt.Fprintln(tw, "APP\tCATEGORY\tDESCRIPTION\tMETHODS\tDEFAULT HOST")
			for _, a := range apps.Catalog() {
				host := "-"
				if a.Expose != nil {
					host = a.Expose.DefaultHost
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.Category, a.Description, apps.MethodsText(a.Methods), host)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out, "\nPresets (--preset <name>):")
			tw = tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
			for _, p := range apps.Presets() {
				fmt.Fprintf(tw, "  %s\t%s\n", p.Name, strings.Join(p.Apps, ", "))
			}
			return tw.Flush()
		},
	}
}

func newAppInstallCmd() *cobra.Command {
	var (
		target      targetFlags
		presetFlags []string
		profileFlag string
		reinstall   bool
		assumeYes   bool
		valuesFlags []string
	)
	cmd := &cobra.Command{
		Use: "install [<app>[:method][=hostname] ...]",
		// With no apps and no --preset on a terminal, a form opens to choose
		// the profile, a preset and the apps.
		Short: "Install applications into an existing cluster",
		Long: "Install catalog applications into an existing cluster — a k8slocalcli\n" +
			"cluster (--cluster) or any cluster in your kubeconfig (--context).\n\n" +
			"Every app goes through the same steps, each with its own timeout: connection\n" +
			"check, precheck (tools, dependencies, already installed?), install, verify\n" +
			"(all workloads rolled out) and a postcheck that the app actually works.\n" +
			"Dependencies are added automatically (keycloak brings cnpg; <app>:flux brings\n" +
			"flux; <app>:argo brings argocd).\n\n" +
			"Methods: helm, manifest, flux (HelmRelease) or argo (Argo CD Application);\n" +
			"see `k8slocalcli app list` for what each app supports.\n\n" +
			"Profiles: dev (testing: single replicas, dev modes) or production\n" +
			"(near-production: replicas, persistence, generated credentials). Asked\n" +
			"when --profile is not given on a terminal; dev otherwise.\n\n" +
			"With no apps and no --preset, a form opens to choose them (terminal only).\n\n" +
			"Examples:\n" +
			"  k8slocalcli app install                      # form: pick cluster, profile, apps\n" +
			"  k8slocalcli app install argocd --cluster dev\n" +
			"  k8slocalcli app install argocd:manifest=cd.localtest.me --cluster dev\n" +
			"  k8slocalcli app install keycloak nyancat:flux --cluster dev --profile production\n" +
			"  k8slocalcli app install k8sdockside --cluster dev --values k8sdockside=rbac.yaml\n" +
			"  k8slocalcli app install --preset observability --context my-eks-cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			reqs, values, err := apps.ParseSelection(args, presetFlags, valuesFlags)
			if err != nil {
				return err
			}
			ask := newPrompter(out, assumeYes)
			// No apps named: on a terminal, choose them in the TUI.
			interactive := len(reqs) == 0
			if interactive && (assumeYes || !ask.interactive) {
				return fmt.Errorf("name at least one app or --preset (see: k8slocalcli app list), or run without arguments in a terminal to choose them")
			}
			env, ok, err := target.resolve(ctx, out, ask)
			if err != nil || !ok {
				return err
			}
			env.Values = values
			if interactive {
				res, err := tui.RunAppInstall(tui.InstallOptions{
					Target:    installTarget(env),
					Routable:  env.Exposed,
					Installed: apps.InstalledApps(ctx, env),
					Profile:   cluster.Profile(profileFlag),
				})
				if err != nil {
					return err
				}
				if !res.Confirmed {
					fmt.Fprintln(out, "Cancelled.")
					return nil
				}
				env.Profile = res.Profile
				env.Reinstall = reinstall || res.Reinstall
				return apps.InstallAll(ctx, env, res.Apps)
			}
			profile, err := chooseProfile(profileFlag, env.External(), ask)
			if err != nil {
				return err
			}
			env.Profile = profile
			env.Reinstall = reinstall
			return apps.InstallAll(ctx, env, reqs)
		},
	}
	target.register(cmd, "install into")
	cmd.Flags().StringArrayVar(&presetFlags, "preset", nil, "install a preset bundle of apps; repeatable (list: k8slocalcli app list)")
	cmd.Flags().StringVar(&profileFlag, "profile", "", "dev or production (near-production sizing); asked on a terminal when omitted")
	cmd.Flags().StringArrayVar(&valuesFlags, "values", nil, "chart values for an app, merged over k8slocalcli's: <app>=<file.yaml>, e.g. k8sdockside=rbac.yaml; repeatable")
	cmd.Flags().BoolVar(&reinstall, "reinstall", false, "re-apply apps that are already installed instead of asking")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "answer yes to prompts")
	return cmd
}

// installTarget names the target cluster for the install form's title.
func installTarget(env *apps.Env) string {
	if env.External() {
		return fmt.Sprintf("kube context %q", env.KubeContext)
	}
	return fmt.Sprintf("cluster %q (%s) · context %s", env.Name, env.Provider, env.KubeContext)
}

// chooseProfile returns the --profile value, or asks testing vs
// near-production on a terminal. Without a terminal (or with --yes, which
// must not silently mean "production") it is dev.
func chooseProfile(flag string, external bool, ask *prompter) (cluster.Profile, error) {
	if flag != "" {
		p := cluster.Profile(flag)
		if !p.Valid() {
			return "", fmt.Errorf("unknown profile %q (supported: dev, production)", flag)
		}
		return p, nil
	}
	if ask.assumeYes || !ask.interactive {
		return cluster.ProfileDev, nil
	}
	question := "Near-production setup (replicas, persistence, generated credentials)? No = dev/testing (single replicas, dev modes)."
	if external {
		question = "This is not a k8slocalcli cluster. " + question
	}
	prod, err := ask.confirm(question, false)
	if err != nil {
		return "", err
	}
	if prod {
		return cluster.ProfileProduction, nil
	}
	return cluster.ProfileDev, nil
}
