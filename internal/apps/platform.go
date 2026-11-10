package apps

import (
	"context"
	"fmt"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// Flux pins: the fluxcd-community chart 2.20.0 ships Flux v2.10.0, the same
// release as the install.yaml below.
const (
	fluxVersion      = "v2.10.0"
	fluxChartVersion = "2.20.0"
)

var fluxApp = App{
	Name:        "flux",
	Description: "Flux " + fluxVersion,
	Category:    CategoryGitOps,
	Namespace:   fluxNamespace,
	Detect:      []string{"deployment/source-controller"},
	CRD:         fluxHelmReleaseCRD,
	// Flux cannot install itself through the flux method.
	Methods: []cluster.InstallMethod{cluster.MethodHelm, cluster.MethodManifest},
	chart: &helmChart{
		Release:   "flux",
		Chart:     "flux2",
		Repo:      "https://fluxcd-community.github.io/helm-charts",
		Version:   fluxChartVersion,
		Namespace: fluxNamespace,
	},
	values: func(_ context.Context, _ *Env, _ string) (map[string]any, error) {
		return nil, nil
	},
	manifestURL: "https://github.com/fluxcd/flux2/releases/download/" + fluxVersion + "/install.yaml",
	// The source controller pulls charts for the flux method.
	configure: func(ctx context.Context, env *Env) error {
		return env.trustCABundle(ctx, fluxNamespace, "source-controller")
	},
	notes: func(_ context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   Status:   kubectl --context %s get helmreleases,kustomizations,gitrepositories,ocirepositories -A\n", env.KubeContext)
		fmt.Fprintf(env.Out, "   Apps:     install catalog apps through Flux with <app>:flux, e.g. k8slocalcli app install nyancat:flux\n")
	},
}

// Crossplane pins.
const (
	crossplaneVersion   = "2.4.3"
	crossplaneNamespace = "crossplane-system"
)

var crossplaneApp = App{
	Name:        "crossplane",
	Description: "Crossplane v" + crossplaneVersion,
	Category:    CategoryPlatform,
	Namespace:   crossplaneNamespace,
	Detect:      []string{"deployment/crossplane"},
	CRD:         "providers.pkg.crossplane.io",
	chart: &helmChart{
		Release:   "crossplane",
		Chart:     "crossplane",
		Repo:      "https://charts.crossplane.io/stable",
		Version:   crossplaneVersion,
		Namespace: crossplaneNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		values := map[string]any{}
		if env.production() {
			values["replicas"] = 2
			values["rbacManager"] = map[string]any{"replicas": 2}
		}
		return values, nil
	},
	notes: func(_ context.Context, env *Env, _ string) {
		fmt.Fprintf(env.Out, "   Next:     add a provider, e.g. https://marketplace.upbound.io · kubectl --context %s get providers,functions\n", env.KubeContext)
	},
}
