package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rogerwesterbo/k8slocalcli/internal/plugin"
	"github.com/rogerwesterbo/k8slocalcli/internal/settings"
)

// maybeDispatchPlugin inspects os.Args[1:] and, if the invocation does not
// match a built-in subcommand but does match a k8slocalcli-* plugin on PATH,
// replaces the current process with the plugin. It returns (true, err) if it
// attempted a dispatch, in which case the caller should return err without
// invoking cobra. (false, nil) means "let cobra handle it".
func maybeDispatchPlugin(root *cobra.Command) (bool, error) {
	args := os.Args[1:]
	if len(args) == 0 {
		return false, nil
	}

	tokens, firstIdx := collectPluginTokens(args)
	if len(tokens) == 0 {
		return false, nil
	}

	// If the first candidate is a built-in command, let cobra handle it —
	// even if a same-named plugin also happens to exist on PATH. This matches
	// kubectl's behaviour: built-ins always win.
	if builtinCommandNames(root)[tokens[0]] {
		return false, nil
	}

	path, matched, ok := plugin.LongestMatch(tokens)
	if !ok {
		return false, nil
	}

	pluginArgs := argsAfterMatch(args, firstIdx, len(matched))
	env := buildPluginEnv()
	return true, plugin.Exec(path, pluginArgs, env)
}

// collectPluginTokens returns the sequence of consecutive non-flag tokens that
// could form a plugin name, along with the index of the first such token in
// args. k8slocalcli has no value-taking global flags, so any leading flags are
// simply skipped.
func collectPluginTokens(args []string) (tokens []string, firstIdx int) {
	firstIdx = -1
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		firstIdx = i
		break
	}
	if firstIdx < 0 {
		return nil, -1
	}
	for j := firstIdx; j < len(args); j++ {
		if strings.HasPrefix(args[j], "-") {
			break
		}
		tokens = append(tokens, args[j])
	}
	return tokens, firstIdx
}

// argsAfterMatch returns the slice of args that should be forwarded to the
// plugin. It preserves original order and keeps every flag that appeared
// before the matched tokens — only the matched tokens themselves are dropped.
func argsAfterMatch(args []string, firstIdx, matchedLen int) []string {
	out := make([]string, 0, len(args)-matchedLen)
	out = append(out, args[:firstIdx]...)
	out = append(out, args[firstIdx+matchedLen:]...)
	return out
}

// buildPluginEnv extends os.Environ() with k8slocalcli-specific variables so
// plugins can read shared state without reparsing flags.
func buildPluginEnv() []string {
	env := os.Environ()
	if dir, err := settings.ConfigDir(); err == nil {
		env = append(env, "K8SLOCALCLI_CONFIG_DIR="+dir)
	}
	return env
}
