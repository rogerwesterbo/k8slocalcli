package runner

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCapture(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf)
	out, err := r.Capture(context.Background(), "echo", "hello", "world")
	if err != nil {
		t.Fatalf("Capture returned error: %v", err)
	}
	if out != "hello world" {
		t.Errorf("Capture out = %q, want %q", out, "hello world")
	}
	if !strings.Contains(buf.String(), "$ echo hello world") {
		t.Errorf("expected command echo in streamed output, got %q", buf.String())
	}
}

func TestRunError(t *testing.T) {
	r := New(nil)
	err := r.Run(context.Background(), "false")
	if err == nil {
		t.Fatal("expected error from `false`")
	}
}

func TestLookPath(t *testing.T) {
	if !LookPath("echo") {
		t.Error("expected echo to be on PATH")
	}
	if LookPath("definitely-not-a-real-binary-xyz") {
		t.Error("expected missing binary to report false")
	}
}
