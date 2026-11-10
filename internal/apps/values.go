package apps

import (
	"fmt"
	"os"
	"strings"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"sigs.k8s.io/yaml"
)

// ParseSelection turns the command-line app selection into requests and
// per-app chart values: app arguments (name[:method][=host]), --preset names
// (their apps added after the named ones) and --values <app>=<file> flags.
// `create` and `app install` both use it.
func ParseSelection(appArgs, presetNames, valuesFlags []string) ([]cluster.AppRequest, map[string]map[string]any, error) {
	var reqs []cluster.AppRequest
	for _, a := range appArgs {
		req, err := ParseRequest(a)
		if err != nil {
			return nil, nil, fmt.Errorf("app %s: %w", a, err)
		}
		reqs = append(reqs, req)
	}
	fromPresets, err := ExpandPresets(presetNames, reqs)
	if err != nil {
		return nil, nil, err
	}
	values, err := ParseValuesFlags(valuesFlags)
	if err != nil {
		return nil, nil, err
	}
	return append(reqs, fromPresets...), values, nil
}

// ParseValuesFlags reads --values flags of the form <app>=<file.yaml> into
// per-app chart values. Several files for one app are merged in order (later
// wins), like helm's own --values. Apps must exist and have a chart.
func ParseValuesFlags(flags []string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for _, f := range flags {
		name, path, ok := strings.Cut(f, "=")
		if !ok || name == "" || path == "" {
			return nil, fmt.Errorf("--values %q: want <app>=<file.yaml>", f)
		}
		app, found := Lookup(name)
		if !found {
			return nil, fmt.Errorf("--values %s: unknown app %q", f, name)
		}
		if app.chart == nil {
			return nil, fmt.Errorf("--values %s: %s has no Helm chart, so it takes no values", f, app.Name)
		}
		data, err := os.ReadFile(path) // #nosec G304 -- a values file the user named
		if err != nil {
			return nil, fmt.Errorf("--values %s: %w", f, err)
		}
		var values map[string]any
		if err := yaml.Unmarshal(data, &values); err != nil {
			return nil, fmt.Errorf("--values %s: not a YAML map: %w", f, err)
		}
		out[app.Name] = mergeValues(out[app.Name], values)
	}
	return out, nil
}

// mergeValues deep-merges override into base the way Helm merges values
// files: maps merge key by key, anything else (lists included) is replaced.
// Neither input is modified.
func mergeValues(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		bm, bok := out[k].(map[string]any)
		om, ook := v.(map[string]any)
		if bok && ook {
			out[k] = mergeValues(bm, om)
			continue
		}
		out[k] = v
	}
	return out
}
