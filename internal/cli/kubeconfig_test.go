package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "demo.kubeconfig")
	want := []byte("apiVersion: v1\nkind: Config\n")

	if err := writeKubeconfig(path, want); err != nil {
		t.Fatalf("writeKubeconfig: %v", err)
	}

	got, err := os.ReadFile(path) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatalf("reading written kubeconfig: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content = %q, want %q", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}
}

func TestKubeconfigFlagsMutuallyExclusive(t *testing.T) {
	cmd := newKubeconfigCmd()
	cmd.SetArgs([]string{"demo", "--output", "x", "--merge"})
	cmd.SetOut(os.Stderr)
	cmd.SetErr(os.Stderr)
	if err := cmd.Execute(); err == nil {
		t.Error("expected --output and --merge to be rejected together")
	}
}
