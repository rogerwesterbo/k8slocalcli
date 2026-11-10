package apps

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

func TestMergeValues(t *testing.T) {
	base := map[string]any{
		"rbac": map[string]any{"mode": "admin", "create": true},
		"auth": map[string]any{"publicURL": "https://x"},
		"list": []any{"a"},
	}
	override := map[string]any{
		"rbac": map[string]any{"mode": "custom", "rules": []any{"r"}},
		"list": []any{"b"},
	}
	got := mergeValues(base, override)
	want := map[string]any{
		"rbac": map[string]any{"mode": "custom", "create": true, "rules": []any{"r"}},
		"auth": map[string]any{"publicURL": "https://x"},
		"list": []any{"b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merge = %#v\nwant %#v", got, want)
	}
	if base["rbac"].(map[string]any)["mode"] != "admin" {
		t.Error("mergeValues must not modify its inputs")
	}
}

func TestParseValuesFlags(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b.yaml")
	if err := os.WriteFile(a, []byte("rbac:\n  mode: none\n  extraClusterRoles: [edit]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("rbac:\n  mode: readonly\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ParseValuesFlags([]string{"dockside=" + a, "k8sdockside=" + b})
	if err != nil {
		t.Fatal(err)
	}
	rbac := got["k8sdockside"]["rbac"].(map[string]any)
	if rbac["mode"] != "readonly" || rbac["extraClusterRoles"] == nil {
		t.Errorf("files for one app (alias included) should merge in order, got %v", rbac)
	}
	for _, bad := range []string{"k8sdockside", "nope=" + a, "flux=" + filepath.Join(dir, "missing.yaml")} {
		if _, err := ParseValuesFlags([]string{bad}); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

// dev lets the dashboard do everything; production keeps read-only; user
// values win over both.
func TestK8sDocksideRBAC(t *testing.T) {
	for profile, want := range map[cluster.Profile]string{cluster.ProfileDev: "admin", cluster.ProfileProduction: "readonly"} {
		c, err := k8sDocksideApp.renderChart(t.Context(), &Env{Profile: profile}, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Values["rbac"].(map[string]any)["mode"]; got != want {
			t.Errorf("%s: rbac.mode = %v, want %s", profile, got, want)
		}
	}
	c, _ := k8sDocksideApp.renderChart(t.Context(), &Env{Profile: cluster.ProfileDev}, "")
	merged := mergeValues(c.Values, map[string]any{"rbac": map[string]any{"mode": "custom"}})
	if merged["rbac"].(map[string]any)["mode"] != "custom" {
		t.Error("user values must win")
	}
}

func TestDescribeRoles(t *testing.T) {
	lines := "k8sdockside=cluster-admin\nother=view\nk8sdockside=edit\n"
	got := describeRoles(roleNamesFor(lines, "k8sdockside"))
	if got != "cluster-admin (everything) + edit" {
		t.Errorf("got %q", got)
	}
	if got := describeRoles(nil); !strings.HasPrefix(got, "none") {
		t.Errorf("got %q", got)
	}
}
