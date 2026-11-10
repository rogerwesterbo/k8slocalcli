package apps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Where the GitOps controllers installed by the catalog live. The flux and
// argo methods put their objects there (the HelmRelease/Application, not the
// app itself), which is where both controllers watch by default.
const (
	fluxNamespace = "flux-system"
	// argoCDNamespace is defined in argocd.go.
)

// CRDs whose presence means a GitOps controller can take an install.
const (
	fluxHelmReleaseCRD    = "helmreleases.helm.toolkit.fluxcd.io"
	fluxOCIRepositoryCRD  = "ocirepositories.source.toolkit.fluxcd.io"
	fluxHelmRepositoryCRD = "helmrepositories.source.toolkit.fluxcd.io"
	argoApplicationCRD    = "applications.argoproj.io"
)

// fluxInstall hands a chart to Flux: a HelmRepository or OCIRepository source
// plus a HelmRelease in flux-system that installs into the chart's namespace.
// It waits (up to timeout) for the HelmRelease to report Ready, which Flux
// only does once Helm's own wait for the release's resources succeeded.
func (e *Env) fluxInstall(ctx context.Context, name string, c helmChart, timeout time.Duration) error {
	manifest, err := fluxManifest(name, c, timeout)
	if err != nil {
		return err
	}
	if err := e.applyYAML(ctx, manifest); err != nil {
		return fmt.Errorf("creating Flux HelmRelease: %w", err)
	}
	// Ask for a reconcile now instead of waiting for the next interval.
	_ = e.kubectl(ctx, "annotate", "--overwrite", "-n", fluxNamespace, "helmrelease/"+name,
		"reconcile.fluxcd.io/requestedAt="+time.Now().Format(time.RFC3339Nano))
	return e.kubectl(ctx, "wait", "--for=condition=Ready", "helmrelease/"+name,
		"-n", fluxNamespace, "--timeout="+timeout.String())
}

// fluxManifest renders the Flux source and HelmRelease for a chart.
func fluxManifest(name string, c helmChart, timeout time.Duration) (string, error) {
	meta := map[string]any{"name": name, "namespace": fluxNamespace,
		"labels": map[string]any{"app.kubernetes.io/managed-by": "k8slocalcli"}}

	release := map[string]any{
		"interval":         "10m",
		"releaseName":      c.Release,
		"targetNamespace":  c.Namespace,
		"storageNamespace": c.Namespace,
		"timeout":          timeout.String(),
		"install": map[string]any{
			"createNamespace": true,
			"crds":            "CreateReplace",
			"remediation":     map[string]any{"retries": 3},
		},
		"upgrade": map[string]any{
			"crds":        "CreateReplace",
			"remediation": map[string]any{"retries": 3},
		},
	}
	if len(c.Values) > 0 {
		release["values"] = c.Values
	}

	var source map[string]any
	if c.oci() {
		ref := map[string]any{"semver": ">=0.0.0"}
		if c.Version != "" {
			ref = map[string]any{"tag": c.Version}
		}
		source = map[string]any{
			"apiVersion": "source.toolkit.fluxcd.io/v1",
			"kind":       "OCIRepository",
			"metadata":   meta,
			"spec": map[string]any{
				"interval": "1h",
				"url":      c.Chart,
				"ref":      ref,
				"layerSelector": map[string]any{
					"mediaType": "application/vnd.cncf.helm.chart.content.v1.tar+gzip",
					"operation": "copy",
				},
			},
		}
		release["chartRef"] = map[string]any{"kind": "OCIRepository", "name": name}
	} else {
		version := c.Version
		if version == "" {
			version = "*"
		}
		source = map[string]any{
			"apiVersion": "source.toolkit.fluxcd.io/v1",
			"kind":       "HelmRepository",
			"metadata":   meta,
			"spec":       map[string]any{"interval": "1h", "url": c.Repo},
		}
		release["chart"] = map[string]any{"spec": map[string]any{
			"chart":     c.Chart,
			"version":   version,
			"interval":  "1h",
			"sourceRef": map[string]any{"kind": "HelmRepository", "name": name},
		}}
	}

	hr := map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   meta,
		"spec":       release,
	}
	return marshalDocs(source, hr)
}

// argoInstall hands a chart to Argo CD: an auto-syncing Application in the
// argocd namespace (plus a repository Secret for OCI charts, which Argo CD
// cannot pull without one). It waits (up to timeout) for the Application to
// be Synced and Healthy.
func (e *Env) argoInstall(ctx context.Context, name string, c helmChart, timeout time.Duration) error {
	manifest, err := argoManifest(name, c)
	if err != nil {
		return err
	}
	if err := e.applyYAML(ctx, manifest); err != nil {
		return fmt.Errorf("creating Argo CD Application: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for _, cond := range []string{"{.status.sync.status}=Synced", "{.status.health.status}=Healthy"} {
		left := time.Until(deadline).Round(time.Second)
		if left <= 0 {
			left = time.Second
		}
		if err := e.kubectl(ctx, "wait", "--for=jsonpath="+cond, "application/"+name,
			"-n", argoCDNamespace, "--timeout="+left.String()); err != nil {
			return err
		}
	}
	return nil
}

// argoManifest renders the Argo CD Application (and OCI repository Secret)
// for a chart.
func argoManifest(name string, c helmChart) (string, error) {
	var docs []any
	repoURL, chart := c.Repo, c.Chart
	if c.oci() {
		repoURL, chart = c.ociParts()
		docs = append(docs, map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":      "k8slocalcli-oci-" + name,
				"namespace": argoCDNamespace,
				"labels":    map[string]any{"argocd.argoproj.io/secret-type": "repository"},
			},
			"stringData": map[string]any{
				"type":      "helm",
				"name":      name,
				"url":       repoURL,
				"enableOCI": "true",
			},
		})
	}
	version := c.Version
	if version == "" {
		version = "*"
	}
	helm := map[string]any{"releaseName": c.Release}
	if len(c.Values) > 0 {
		helm["valuesObject"] = c.Values
	}
	docs = append(docs, map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"name":       name,
			"namespace":  argoCDNamespace,
			"labels":     map[string]any{"app.kubernetes.io/managed-by": "k8slocalcli"},
			"finalizers": []any{"resources-finalizer.argocd.argoproj.io"},
		},
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{
				"repoURL":        repoURL,
				"chart":          chart,
				"targetRevision": version,
				"helm":           helm,
			},
			"destination": map[string]any{
				"server":    "https://kubernetes.default.svc",
				"namespace": c.Namespace,
			},
			"syncPolicy": map[string]any{
				"automated":   map[string]any{"prune": true, "selfHeal": true},
				"syncOptions": []any{"CreateNamespace=true", "ServerSideApply=true"},
				"retry": map[string]any{
					"limit":   5,
					"backoff": map[string]any{"duration": "10s", "factor": 2, "maxDuration": "3m"},
				},
			},
		},
	})
	return marshalDocs(docs...)
}

// marshalDocs renders objects as one multi-document YAML stream.
func marshalDocs(objs ...any) (string, error) {
	parts := make([]string, 0, len(objs))
	for _, o := range objs {
		b, err := yaml.Marshal(o)
		if err != nil {
			return "", err
		}
		parts = append(parts, strings.TrimSpace(string(b)))
	}
	return strings.Join(parts, "\n---\n") + "\n", nil
}
