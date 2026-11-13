package provider

import (
	"os"
	"path/filepath"
	"testing"
)

// talosctl 1.14 destroy removes its whole --state directory, so Delete must
// hand it a directory that holds only the cluster being deleted.
func TestIsolateState(t *testing.T) {
	state := t.TempDir()
	for _, f := range []string{"victim/state.yaml", "other/state.yaml"} {
		p := filepath.Join(state, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	dir, cleanup, err := isolateState(state, "victim")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "victim" {
		t.Errorf("isolated dir should hold only victim, got %v", entries)
	}
	if err := os.RemoveAll(dir); err != nil { // what talosctl destroy does
		t.Fatal(err)
	}
	cleanup()

	if _, err := os.Stat(filepath.Join(state, "other", "state.yaml")); err != nil {
		t.Errorf("another cluster's state was removed: %v", err)
	}
	if left, _ := os.ReadDir(state); len(left) != 1 {
		t.Errorf("state dir should only hold other, got %v", left)
	}

	// A cluster without state (failed create) still gets an empty dir.
	dir, cleanup, err = isolateState(state, "missing")
	if err != nil {
		t.Fatalf("missing state should not be an error: %v", err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s behind", dir)
	}
}
