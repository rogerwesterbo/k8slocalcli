package tui

import (
	"testing"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

// The form opens on the cluster name, and the provider picker comes right after.
func TestFormFieldOrder(t *testing.T) {
	f := newForm(cluster.Spec{}, nil)
	if f.focus != fieldName {
		t.Fatalf("initial focus = %d, want fieldName", f.focus)
	}
	_, _, _ = f.handleKey("<Tab>")
	if f.focus != fieldProvider {
		t.Fatalf("focus after Tab = %d, want fieldProvider", f.focus)
	}
	before := f.spec.Provider
	_, _, _ = f.handleKey("<Right>")
	if f.spec.Provider == before {
		t.Errorf("→ on provider field did not change provider (still %s)", before)
	}
}
