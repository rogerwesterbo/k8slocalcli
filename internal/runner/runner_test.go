package runner

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
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

// A cancelled command is interrupted, not killed, so helm can mark its
// release failed instead of leaving it pending.
func TestCancelInterrupts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no interrupt signal on Windows")
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	var buf bytes.Buffer
	start := time.Now()
	err := New(&buf).Run(ctx, "sh", "-c", `trap 'echo interrupted; kill $!; exit 3' INT; sleep 10 & wait`)
	if err == nil {
		t.Fatal("expected an error from a cancelled command")
	}
	if !strings.Contains(buf.String(), "interrupted") {
		t.Errorf("command was not interrupted; output %q", buf.String())
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("cancel took %s", d)
	}
}

func TestRunError(t *testing.T) {
	r := New(nil)
	err := r.Run(context.Background(), "false")
	if err == nil {
		t.Fatal("expected error from `false`")
	}
}

func TestIsLocalDockerEndpoint(t *testing.T) {
	cases := map[string]bool{
		"unix:///var/run/docker.sock":               true,
		"unix:///Users/me/.docker/run/docker.sock":  true,
		"npipe:////./pipe/dockerDesktopLinuxEngine": true,
		"tcp://192.168.1.10:2376":                   false,
		"ssh://user@remote":                         false,
		"":                                          false,
	}
	for in, want := range cases {
		if got := isLocalDockerEndpoint(in); got != want {
			t.Errorf("isLocalDockerEndpoint(%q) = %v, want %v", in, got, want)
		}
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
