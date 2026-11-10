package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/rogerwesterbo/k8slocalcli/internal/apps"
	"github.com/rogerwesterbo/k8slocalcli/internal/provider"
	"github.com/rogerwesterbo/k8slocalcli/internal/tui"
)

// targetFlags selects the cluster an app command acts on: a k8slocalcli
// cluster (--cluster, picker when omitted) or any kube context (--context).
// Every command that installs into or reads from a cluster registers these
// and calls resolve, so the choice works the same everywhere.
type targetFlags struct {
	cluster     string
	provider    string
	kubeContext string
}

// register adds the target flags to cmd; verb completes the help text
// ("install into", "uninstall from").
func (t *targetFlags) register(cmd *cobra.Command, verb string) {
	cmd.Flags().StringVarP(&t.cluster, "cluster", "c", "", "k8slocalcli cluster to "+verb+" (picker when omitted and several exist)")
	cmd.Flags().StringVar(&t.provider, "provider", "", "provider of the cluster (auto-detected if omitted)")
	cmd.Flags().StringVar(&t.kubeContext, "context", "", verb+" any cluster by kube context instead of a k8slocalcli cluster")
}

// resolve builds the Env for the chosen cluster and checks it answers. ok is
// false when the user cancelled the cluster picker.
func (t *targetFlags) resolve(ctx context.Context, out io.Writer, ask *prompter) (env *apps.Env, ok bool, err error) {
	if t.kubeContext != "" && t.cluster != "" {
		return nil, false, fmt.Errorf("use either --cluster (a k8slocalcli cluster) or --context (any kube context), not both")
	}
	if t.kubeContext != "" {
		env = newEnv(apps.Target{Name: t.kubeContext, KubeContext: t.kubeContext}, out, ask)
		fmt.Fprintf(out, "ℹ️  Using kube context %q (not managed by k8slocalcli)\n", t.kubeContext)
	} else {
		p, name, picked, err := pickCluster(ctx, t.cluster, t.provider, out)
		if err != nil || !picked {
			return nil, false, err
		}
		env = newEnv(localTarget(ctx, p, name, nil), out, ask)
	}
	// Fail on an unreachable cluster before anything is asked.
	if err := apps.CheckConnection(ctx, env); err != nil {
		return nil, false, err
	}
	return env, true, nil
}

// newEnv wraps a target with this process's output, prompts and progress.
func newEnv(target apps.Target, out io.Writer, ask *prompter) *apps.Env {
	return &apps.Env{
		Target:   target,
		Out:      out,
		Confirm:  ask.confirm,
		Progress: newReporter(out),
	}
}

// localTarget describes a k8slocalcli cluster for the apps package, reading
// its published gateway ports and network back from the container engine.
// caCerts are extra CA files besides ~/.k8slocalcli/ca-certificates (the
// create command's --ca-cert).
func localTarget(ctx context.Context, p provider.Provider, name string, caCerts []string) apps.Target {
	httpPort, httpsPort, exposed := provider.GatewayHostPorts(ctx, p, name)
	return apps.Target{
		Name:              name,
		KubeContext:       p.Context(name),
		Exposed:           exposed,
		GatewayNode:       p.GatewayNode(name),
		HTTPPort:          httpPort,
		HTTPSPort:         httpsPort,
		Provider:          p.Name(),
		LoadBalancerRange: provider.LoadBalancerRange(ctx, p, name),
		CABundle:          provider.CABundle(caCerts),
	}
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
