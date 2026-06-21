// Package cli wires the cobra command tree for k8slocalcli.
package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/provider"
	"github.com/rogerwesterbo/k8slocalcli/internal/tui"
)

// version is set via -ldflags at build time.
var version = "dev"

// Execute builds and runs the root command.
func Execute(ctx context.Context) error {
	root := newRootCmd()
	// Before handing off to cobra, see if this invocation targets an external
	// k8slocalcli-* plugin on PATH; if so, exec it instead.
	if handled, err := maybeDispatchPlugin(root); handled {
		return err
	}
	return root.ExecuteContext(ctx)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "k8slocalcli",
		Short: "Create local Kubernetes clusters in Docker with kind or talos",
		Long: "k8slocalcli spins up local Kubernetes clusters whose nodes run as Docker\n" +
			"containers, using either kind or Talos Linux as the provider.\n\n" +
			"Run `k8slocalcli create` with no flags for an interactive TUI.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.AddCommand(newCreateCmd(), newListCmd(), newDeleteCmd(), newPluginCmd())
	return root
}

func newCreateCmd() *cobra.Command {
	spec := cluster.DefaultSpec()

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a cluster (interactive TUI when no --name is given)",
		Long: "Create a local Kubernetes cluster.\n\n" +
			"With no --name flag an interactive TUI asks for the provider, name,\n" +
			"and node counts. Pass --name to run non-interactively from flags.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			interactive := spec.Name == ""
			if interactive {
				ctx := cmd.Context()
				versions := map[cluster.Provider][]string{}
				for _, p := range provider.All() {
					versions[p.Name()] = p.KubernetesVersions(ctx)
				}
				res, err := tui.Run(spec, versions)
				if err != nil {
					return err
				}
				if !res.Confirmed {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
				spec = res.Spec
			}
			return runCreate(cmd.Context(), spec, cmd.OutOrStdout())
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&spec.Name, "name", "", "cluster name (omit for interactive TUI)")
	providerFlag := flags.String("provider", string(spec.Provider), "provider: kind or talos")
	flags.IntVar(&spec.ControlPlanes, "control-planes", spec.ControlPlanes, "number of control plane nodes")
	flags.IntVar(&spec.Workers, "workers", spec.Workers, "number of worker nodes")
	flags.StringVar(&spec.K8sVersion, "k8s-version", "", "Kubernetes version (default: provider's newest)")
	cniFlag := flags.String("cni", string(spec.CNI), "CNI: default, cilium or calico")
	flags.IntVar(&spec.HTTPPort, "http-port", spec.HTTPPort, "host port mapped to ingress :80")
	flags.IntVar(&spec.HTTPSPort, "https-port", spec.HTTPSPort, "host port mapped to ingress :443")

	cmd.PreRunE = func(_ *cobra.Command, _ []string) error {
		spec.Provider = cluster.Provider(*providerFlag)
		spec.CNI = cluster.CNI(*cniFlag)
		return nil
	}

	return cmd
}

func runCreate(ctx context.Context, spec cluster.Spec, out io.Writer) error {
	if err := spec.Validate(); err != nil {
		return err
	}

	p, err := provider.Get(spec.Provider)
	if err != nil {
		return err
	}
	if err := p.CheckPrerequisites(); err != nil {
		return err
	}

	exists, err := p.Exists(ctx, spec.Name)
	if err != nil {
		return fmt.Errorf("checking for existing cluster: %w", err)
	}
	if exists {
		return fmt.Errorf("a %s cluster named %q already exists; delete it first or choose another name", spec.Provider, spec.Name)
	}

	if err := p.Create(ctx, spec, out); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nkubectl context: %s\n", p.Context(spec.Name))
	fmt.Fprintf(out, "Try: kubectl --context %s get nodes\n", p.Context(spec.Name))
	return nil
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List local clusters from all providers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			type row struct{ provider, name, context string }
			var rows []row
			for _, p := range provider.All() {
				clusters, err := p.List(ctx)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: listing %s clusters: %v\n", p.Name(), err)
					continue
				}
				for _, c := range clusters {
					rows = append(rows, row{string(p.Name()), c, p.Context(c)})
				}
			}

			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No local clusters found.")
				return nil
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
			fmt.Fprintln(tw, "PROVIDER\tCLUSTER\tCONTEXT")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", r.provider, r.name, r.context)
			}
			return tw.Flush()
		},
	}
}

func newDeleteCmd() *cobra.Command {
	var providerFlag string

	cmd := &cobra.Command{
		Use:     "delete [name]",
		Aliases: []string{"d", "rm"},
		Short:   "Delete a local cluster (interactive picker when no name is given)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			// Name supplied: delete it directly.
			if len(args) == 1 {
				name := args[0]
				p, err := resolveProvider(ctx, name, providerFlag)
				if err != nil {
					return err
				}
				return p.Delete(ctx, name, out)
			}

			// No name: present an interactive picker of existing clusters.
			choices, err := gatherClusters(ctx, providerFlag)
			if err != nil {
				return err
			}
			if len(choices) == 0 {
				fmt.Fprintln(out, "No local clusters found.")
				return nil
			}

			choice, ok, err := tui.SelectClusterToDelete(choices)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Fprintln(out, "Cancelled.")
				return nil
			}

			p, err := provider.Get(choice.Provider)
			if err != nil {
				return err
			}
			return p.Delete(ctx, choice.Name, out)
		},
	}
	cmd.Flags().StringVar(&providerFlag, "provider", "", "provider of the cluster (auto-detected if omitted)")
	return cmd
}

// gatherClusters lists every cluster across providers (or a single provider when
// explicit is set) as selectable picker choices.
func gatherClusters(ctx context.Context, explicit string) ([]tui.ClusterChoice, error) {
	providers := provider.All()
	if explicit != "" {
		pv := cluster.Provider(explicit)
		if !pv.Valid() {
			return nil, fmt.Errorf("unknown provider %q", explicit)
		}
		p, err := provider.Get(pv)
		if err != nil {
			return nil, err
		}
		providers = []provider.Provider{p}
	}

	var choices []tui.ClusterChoice
	for _, p := range providers {
		names, err := p.List(ctx)
		if err != nil {
			continue
		}
		for _, n := range names {
			choices = append(choices, tui.ClusterChoice{Provider: p.Name(), Name: n})
		}
	}
	return choices, nil
}

// resolveProvider finds which provider owns a cluster, honouring an explicit
// --provider flag when given.
func resolveProvider(ctx context.Context, name, explicit string) (provider.Provider, error) {
	if explicit != "" {
		pv := cluster.Provider(explicit)
		if !pv.Valid() {
			return nil, fmt.Errorf("unknown provider %q", explicit)
		}
		return provider.Get(pv)
	}
	for _, p := range provider.All() {
		exists, err := p.Exists(ctx, name)
		if err != nil {
			continue
		}
		if exists {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no cluster named %q found in any provider (use --provider to force)", name)
}
