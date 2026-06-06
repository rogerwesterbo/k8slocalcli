package provider

import (
	"context"
	"fmt"
	"io"

	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// controlPlaneTaint is the standard taint kubeadm/Talos place on control-plane
// nodes to keep regular workloads off them (Kubernetes 1.25+).
const controlPlaneTaint = "node-role.kubernetes.io/control-plane:NoSchedule"

// makeControlPlaneSchedulable removes the control-plane NoSchedule taint from
// every node so workloads can run when a cluster has no worker nodes. Without
// this, a control-plane-only cluster (e.g. Talos in Docker, or kind with zero
// workers) has nowhere to schedule pods.
//
// It is best-effort: removing a taint that is already absent (some providers
// leave the control plane schedulable) is reported as a no-op, not an error.
func makeControlPlaneSchedulable(ctx context.Context, kubeContext string, out io.Writer) {
	fmt.Fprintf(out, "\n🔧 No workers requested — allowing workloads to run on the control plane\n")

	// Use a quiet runner so the "taint not found" message (when the taint is
	// already absent) is not streamed to the user as a scary error.
	q := runner.New(nil)
	if _, err := q.Capture(ctx, "kubectl", "--context", kubeContext,
		"taint", "nodes", "--all", controlPlaneTaint+"-"); err != nil {
		fmt.Fprintf(out, "ℹ️  Control plane already accepts workloads\n")
		return
	}
	fmt.Fprintf(out, "✅ Control plane now accepts workloads\n")
}
