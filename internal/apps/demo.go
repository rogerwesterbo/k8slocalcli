package apps

import "context"

// nyan-cat is installed from its OCI chart without a pin: Helm takes the
// newest release, and the flux/argo methods resolve the newest version once
// at install time (resolveVersion).
const (
	nyanCatChart     = "oci://ghcr.io/rogerwesterbo/helm/nyan-cat"
	nyanCatNamespace = "nyan-cat"
)

var nyanCatApp = App{
	Name:        "nyancat",
	Aliases:     []string{"nyan-cat"},
	Description: "nyan-cat (latest, demo web app)",
	Category:    CategoryDemo,
	Namespace:   nyanCatNamespace,
	Detect:      []string{"deployment/nyan-cat"},
	Expose: &Expose{
		DefaultHost: "nyancat.localtest.me",
		Service:     "nyan-cat",
		Port:        8080,
		Protocol:    ProtocolHTTP,
		LocalPort:   8084,
	},
	chart: &helmChart{
		Release:   "nyan-cat",
		Chart:     nyanCatChart,
		Namespace: nyanCatNamespace,
	},
	values: func(_ context.Context, env *Env, _ string) (map[string]any, error) {
		values := map[string]any{}
		if env.production() {
			values["replicaCount"] = 2
		}
		return values, nil
	},
}
