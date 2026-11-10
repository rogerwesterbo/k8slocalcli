// cabundle.go gives in-cluster components that fetch over HTTPS themselves
// the extra CA certificates (a TLS-intercepting proxy's root) the nodes trust.

package apps

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CA bundle wiring for pods that fetch over HTTPS themselves.
const (
	caBundleConfigMap = "k8slocalcli-ca-bundle"
	caBundleDir       = "/etc/ssl/k8slocalcli"
)

// trustCABundle makes the first container of each Deployment in ns trust
// env.CABundle as well as its image's own roots: the bundle goes into a
// ConfigMap mounted at caBundleDir, and SSL_CERT_DIR lists that directory
// after the system one (Go, which Helm, Flux and Argo CD are written in, reads
// every directory in the list). Node-level trust does not reach containers, so
// without this a GitOps controller behind a TLS-intercepting proxy fails every
// chart pull with "x509: certificate signed by unknown authority". The patch
// is re-applied by configure on every install, since an upgrade resets it.
func (e *Env) trustCABundle(ctx context.Context, ns string, deployments ...string) error {
	if e.CABundle == "" {
		return nil
	}
	cm := fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n  namespace: %s\ndata:\n  k8slocalcli-ca.crt: |\n", caBundleConfigMap, ns)
	for _, l := range strings.Split(strings.TrimSpace(e.CABundle), "\n") {
		cm += "    " + l + "\n"
	}
	if err := e.applyYAML(ctx, cm); err != nil {
		return fmt.Errorf("creating CA bundle ConfigMap: %w", err)
	}
	for _, d := range deployments {
		container, err := e.kubectlQuiet(ctx, "get", "deployment", d, "-n", ns, "-o", "jsonpath={.spec.template.spec.containers[0].name}")
		if err != nil {
			return err
		}
		patch := fmt.Sprintf(`{"spec":{"template":{"spec":{"volumes":[{"name":"k8slocalcli-ca","configMap":{"name":%q}}],"containers":[{"name":%q,"env":[{"name":"SSL_CERT_DIR","value":"/etc/ssl/certs:%s"}],"volumeMounts":[{"name":"k8slocalcli-ca","mountPath":%q,"readOnly":true}]}]}}}}`,
			caBundleConfigMap, strings.TrimSpace(container), caBundleDir, caBundleDir)
		if err := e.kubectl(ctx, "patch", "deployment", d, "-n", ns, "--type", "strategic", "-p", patch); err != nil {
			return fmt.Errorf("adding CA bundle to %s: %w", d, err)
		}
		if err := e.kubectl(ctx, "rollout", "status", "deployment/"+d, "-n", ns, "--timeout="+remaining(ctx, 3*time.Minute).String()); err != nil {
			return err
		}
	}
	return nil
}
