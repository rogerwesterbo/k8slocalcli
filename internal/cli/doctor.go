package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rogerwesterbo/k8slocalcli/internal/apps"
)

func newAppDoctorCmd() *cobra.Command {
	var (
		target    targetFlags
		fix       bool
		assumeYes bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Find (and fix) Pod Security problems that keep pods from starting",
		Long: "Check a cluster for pods that Pod Security admission rejects. Talos enforces\n" +
			"the baseline level on every namespace except kube-system, so a workload\n" +
			"that needs host access (hostPath, host network, extra capabilities) never\n" +
			"starts there — and a storage provisioner's helper pod failing makes another\n" +
			"app's volume stay Pending. Other hardened clusters behave the same.\n\n" +
			"Found:\n" +
			"  - catalog apps that need privileged pods but whose namespace lacks the\n" +
			"    labels (e.g. installed by an older k8slocalcli) — always safe to fix;\n" +
			"  - any other namespace where a controller's pods were rejected — fixing\n" +
			"    relaxes that namespace's security, so each one is confirmed.\n\n" +
			"With --fix the namespaces are labelled pod-security.kubernetes.io/enforce=privileged.\n" +
			"`app install` repairs the catalog case on its own before installing.\n\n" +
			"Examples:\n" +
			"  k8slocalcli app doctor --cluster test\n" +
			"  k8slocalcli app doctor --cluster test --fix",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			ask := newPrompter(out, assumeYes)
			env, ok, err := target.resolve(ctx, out, ask)
			if err != nil || !ok {
				return err
			}

			findings := apps.PodSecurityFindings(ctx, env)
			if len(findings) == 0 {
				fmt.Fprintf(out, "✅ No Pod Security problems found in %s\n", env.Name)
				return nil
			}
			for _, f := range findings {
				owner := "not a catalog app"
				if f.Known() {
					owner = f.App
				}
				fmt.Fprintf(out, "⚠️  namespace %s (%s): %s\n", f.Namespace, owner, f.Problem)
			}
			if !fix {
				fmt.Fprintf(out, "\nFix with: k8slocalcli app doctor %s --fix\n", target.flagsText())
				return nil
			}

			fixed := 0
			for _, f := range findings {
				if !f.Known() {
					yes, err := ask.confirm(fmt.Sprintf("Allow privileged pods in namespace %s? This turns off its Pod Security restrictions.", f.Namespace), false)
					if err != nil || !yes {
						fmt.Fprintf(out, "⏭️  left namespace %s as it is\n", f.Namespace)
						continue
					}
				}
				if err := apps.FixPodSecurity(ctx, env, f); err != nil {
					return err
				}
				fixed++
				fmt.Fprintf(out, "🔧 namespace %s now allows privileged pods\n", f.Namespace)
			}
			if fixed > 0 {
				fmt.Fprintln(out, "Controllers retry on their own; rejected volumes and pods are created within a minute or two.")
			}
			return nil
		},
	}
	target.register(cmd, "check")
	cmd.Flags().BoolVar(&fix, "fix", false, "label the namespaces found for privileged pods (namespaces of non-catalog apps are confirmed first)")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "with --fix, also relax non-catalog namespaces without asking")
	return cmd
}
