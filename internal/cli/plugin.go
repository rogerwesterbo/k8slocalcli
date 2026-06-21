package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rogerwesterbo/k8slocalcli/internal/plugin"
	"github.com/rogerwesterbo/k8slocalcli/internal/pluginmgr"
)

// newPluginCmd builds the `plugin` command tree (list/install/upgrade/
// uninstall). It is attached to the root in newRootCmd.
func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "plugin",
		Aliases: []string{"plugins"},
		Short:   "🧩 Manage k8slocalcli plugins (external binaries named k8slocalcli-*)",
		Long: `🧩 k8slocalcli discovers external plugins on PATH whose binary name
starts with "k8slocalcli-". A binary called k8slocalcli-foo can be invoked as
"k8slocalcli foo [args...]".

When k8slocalcli receives a subcommand it does not recognise, it looks for a
matching plugin on PATH and execs it. The first binary on PATH wins;
subsequent binaries of the same name are reported as shadowed by
"k8slocalcli plugin list".

Plugins inherit environment variables describing k8slocalcli's state, so they
can cooperate without reparsing flags:
  K8SLOCALCLI_CONFIG_DIR  path to k8slocalcli's config directory (~/.k8slocalcli)`,
	}

	cmd.AddCommand(
		newPluginListCmd(),
		newPluginInstallCmd(),
		newPluginUpgradeCmd(),
		newPluginUninstallCmd(),
	)
	return cmd
}

func newPluginListCmd() *cobra.Command {
	var listAvailable bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List plugins discovered on PATH",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listAvailable {
				return listAvailablePlugins(cmd)
			}
			return listInstalledPlugins(cmd)
		},
	}
	cmd.Flags().BoolVar(&listAvailable, "available", false,
		"list installable plugins from the curated index instead of installed ones")
	return cmd
}

func listInstalledPlugins(cmd *cobra.Command) error {
	found, err := plugin.List()
	if err != nil {
		return err
	}
	if len(found) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(),
			"No k8slocalcli-* plugins found on PATH. Run `k8slocalcli plugin list --available` to see installable plugins.")
		return nil
	}

	// Map managed plugins by name so we can report their tracked version.
	managed := map[string]*pluginmgr.State{}
	if states, err := pluginmgr.ListStates(); err == nil {
		for _, s := range states {
			managed[s.Name] = s
		}
	}

	builtins := builtinCommandNames(cmd.Root())
	seen := make(map[string]string)

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tVERSION\tPATH\tSTATUS")
	for _, p := range found {
		var notes []string
		if prior, dup := seen[p.Name]; dup {
			notes = append(notes, fmt.Sprintf("shadowed by %s", prior))
		} else {
			seen[p.Name] = p.Path
		}
		if builtins[p.Name] {
			notes = append(notes, "shadowed by built-in command")
		}
		status := "ok"
		if len(notes) > 0 {
			status = strings.Join(notes, "; ")
		}
		version := "-"
		if s, ok := managed[p.Name]; ok {
			version = s.Version
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, version, p.Path, status)
	}
	return tw.Flush()
}

func listAvailablePlugins(cmd *cobra.Command) error {
	idx, err := pluginmgr.FetchIndex(cmd.Context())
	if err != nil {
		return fmt.Errorf("fetching plugin index: %w", err)
	}
	installed := map[string]*pluginmgr.State{}
	if states, err := pluginmgr.ListStates(); err == nil {
		for _, s := range states {
			installed[s.Name] = s
		}
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tREPO\tINSTALLED\tDESCRIPTION")
	for _, e := range idx.Plugins {
		ver := "-"
		if s, ok := installed[e.Name]; ok {
			ver = s.Version
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, e.Repo, ver, e.Description)
	}
	return tw.Flush()
}

// builtinCommandNames returns the set of subcommand names (and aliases)
// registered on the root command. Used by the dispatcher to know when to
// yield to cobra, and by `plugin list` to flag shadowed plugins.
func builtinCommandNames(root *cobra.Command) map[string]bool {
	out := map[string]bool{
		"help":       true,
		"completion": true,
	}
	for _, c := range root.Commands() {
		out[c.Name()] = true
		for _, a := range c.Aliases {
			out[a] = true
		}
	}
	return out
}
