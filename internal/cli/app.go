package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rogerwesterbo/k8slocalcli/internal/apps"
	"github.com/rogerwesterbo/k8slocalcli/internal/provider"
	"github.com/rogerwesterbo/k8slocalcli/internal/tui"
)

func newAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "List and install applications into a cluster",
	}
	cmd.AddCommand(newAppListCmd(), newAppInstallCmd())
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
			env := targetEnv(ctx, p, name, out, newPrompter(out, false))
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
		Short:   "List installable applications",
		RunE: func(cmd *cobra.Command, _ []string) error {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
			fmt.Fprintln(tw, "APP\tDESCRIPTION\tMETHODS\tDEFAULT HOST")
			for _, a := range apps.Catalog() {
				host := "-"
				if a.Expose != nil {
					host = a.Expose.DefaultHost
				}
				methods := make([]string, len(a.Methods))
				for i, m := range a.Methods {
					methods[i] = string(m)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Name, a.Description, strings.Join(methods, ", "), host)
			}
			return tw.Flush()
		},
	}
}

func newAppInstallCmd() *cobra.Command {
	var (
		clusterName  string
		providerFlag string
		assumeYes    bool
	)
	cmd := &cobra.Command{
		Use:   "install <app>[:method][=hostname]",
		Short: "Install an application into an existing cluster",
		Long: "Install a catalog application into an existing cluster and, when the\n" +
			"cluster was created with host exposure, route a hostname to it through\n" +
			"the cluster's Gateway.\n\n" +
			"Examples:\n" +
			"  k8slocalcli app install argocd --cluster dev\n" +
			"  k8slocalcli app install argocd:manifest=cd.localtest.me --cluster dev",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			req, err := apps.ParseRequest(args[0])
			if err != nil {
				return err
			}

			p, name, ok, err := pickCluster(ctx, clusterName, providerFlag, out)
			if err != nil || !ok {
				return err
			}

			env := targetEnv(ctx, p, name, out, newPrompter(out, assumeYes))
			return apps.Install(ctx, env, req)
		},
	}
	cmd.Flags().StringVarP(&clusterName, "cluster", "c", "", "cluster to install into (picker when omitted and several exist)")
	cmd.Flags().StringVar(&providerFlag, "provider", "", "provider of the cluster (auto-detected if omitted)")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "answer yes to prompts")
	return cmd
}

// pickCluster resolves the cluster to act on: the named one, the only one, or
// one chosen in a picker. ok is false when the user cancelled.
func pickCluster(ctx context.Context, name, providerFlag string, out io.Writer) (provider.Provider, string, bool, error) {
	if name != "" {
		p, err := resolveProvider(ctx, name, providerFlag)
		return p, name, err == nil, err
	}
	choices, err := gatherClusters(ctx, providerFlag)
	if err != nil {
		return nil, "", false, err
	}
	var choice tui.ClusterChoice
	switch len(choices) {
	case 0:
		return nil, "", false, fmt.Errorf("no local clusters found; create one with `k8slocalcli create`")
	case 1:
		choice = choices[0]
	default:
		var picked bool
		choice, picked, err = tui.SelectCluster(" Select a cluster ", choices)
		if err != nil {
			return nil, "", false, err
		}
		if !picked {
			fmt.Fprintln(out, "Cancelled.")
			return nil, "", false, nil
		}
	}
	p, err := provider.Get(choice.Provider)
	return p, choice.Name, err == nil, err
}

// targetEnv describes an existing cluster for the apps package, reading its
// published gateway ports back from the container engine.
func targetEnv(ctx context.Context, p provider.Provider, name string, out io.Writer, ask *prompter) *apps.Env {
	httpPort, httpsPort, exposed := provider.GatewayHostPorts(ctx, p, name)
	return &apps.Env{
		Target: apps.Target{
			Name:        name,
			KubeContext: p.Context(name),
			Exposed:     exposed,
			GatewayNode: p.GatewayNode(name),
			HTTPPort:    httpPort,
			HTTPSPort:   httpsPort,
		},
		Out:      out,
		Confirm:  ask.confirm,
		Progress: newReporter(out),
	}
}
