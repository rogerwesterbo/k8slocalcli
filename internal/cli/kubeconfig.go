package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/rogerwesterbo/k8slocalcli/internal/provider"
	"github.com/rogerwesterbo/k8slocalcli/internal/tui"
)

func newKubeconfigCmd() *cobra.Command {
	var (
		providerFlag string
		output       string
		merge        bool
	)

	cmd := &cobra.Command{
		Use:     "kubeconfig [name]",
		Aliases: []string{"kc"},
		Short:   "Print, save or merge a cluster's kubeconfig (interactive picker when no name is given)",
		Long: "Fetch the admin kubeconfig of a local cluster from its provider.\n\n" +
			"kind clusters are read with `kind get kubeconfig`; talos clusters with\n" +
			"`talosctl kubeconfig`, with the API server URL rewritten to the\n" +
			"host-mapped port so it is reachable from the host.\n\n" +
			"By default the kubeconfig is printed to stdout. Use --output to write it\n" +
			"to a file, or --merge to merge it into your default kubeconfig and switch\n" +
			"to the cluster's context.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			var (
				p    provider.Provider
				name string
				err  error
			)
			if len(args) == 1 {
				name = args[0]
				if p, err = resolveProvider(ctx, name, providerFlag); err != nil {
					return err
				}
			} else {
				choices, err := gatherClusters(ctx, providerFlag)
				if err != nil {
					return err
				}
				if len(choices) == 0 {
					fmt.Fprintln(cmd.ErrOrStderr(), "No local clusters found.")
					return nil
				}
				choice, ok, err := tui.SelectCluster(" Select a cluster ", choices)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
					return nil
				}
				name = choice.Name
				if p, err = provider.Get(choice.Provider); err != nil {
					return err
				}
			}

			if merge {
				out := cmd.OutOrStdout()
				if err := p.MergeKubeconfig(ctx, name, out); err != nil {
					return err
				}
				fmt.Fprintf(out, "\n✅ Merged kubeconfig for %s cluster %q; current context is %s\n", p.Name(), name, p.Context(name))
				return nil
			}

			cfg, err := p.Kubeconfig(ctx, name)
			if err != nil {
				return err
			}
			if output == "" {
				_, err := cmd.OutOrStdout().Write(cfg)
				return err
			}
			if err := writeKubeconfig(output, cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "✅ Wrote kubeconfig for %s cluster %q to %s\n", p.Name(), name, output)
			fmt.Fprintf(cmd.ErrOrStderr(), "Try: kubectl --kubeconfig %s get nodes\n", output)
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&providerFlag, "provider", "", "provider of the cluster (auto-detected if omitted)")
	flags.StringVarP(&output, "output", "o", "", "write the kubeconfig to this file instead of stdout")
	flags.BoolVar(&merge, "merge", false, "merge into your default kubeconfig and switch to the cluster's context")
	cmd.MarkFlagsMutuallyExclusive("output", "merge")
	return cmd
}

// writeKubeconfig writes a kubeconfig to path, creating parent directories. The
// file is readable only by the owner since it holds admin credentials.
func writeKubeconfig(path string, cfg []byte) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		return fmt.Errorf("writing kubeconfig: %w", err)
	}
	return nil
}
