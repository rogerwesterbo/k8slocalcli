package progress

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPlainStreamsOutput(t *testing.T) {
	var out bytes.Buffer
	r := New(&out, false, 80)
	err := r.Step(context.Background(), "Installing x", nil, func(w io.Writer) error {
		_, _ = io.WriteString(w, "helm says hi\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"⏳ Installing x", "helm says hi", "✅ Installing x"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// On a terminal, command output is held back and the step ends in one line;
// a failure keeps the last status, which is the useful diagnosis.
func TestLiveHidesOutputAndKeepsStatusOnFailure(t *testing.T) {
	var out bytes.Buffer
	r := New(&out, true, 80)
	r.statusEvery = time.Millisecond
	err := r.Step(context.Background(), "Installing x", func(context.Context) string {
		return "3/4 pods ready · redis: ImagePullBackOff"
	}, func(w io.Writer) error {
		_, _ = io.WriteString(w, "noisy helm output\n")
		time.Sleep(20 * time.Millisecond)
		return errors.New("timed out")
	})
	if err == nil {
		t.Fatal("expected the error to propagate")
	}
	got := out.String()
	if strings.Contains(got, "noisy helm output") {
		t.Errorf("live mode leaked command output:\n%s", got)
	}
	if !strings.Contains(got, "❌ Installing x") || !strings.Contains(got, "last status: 3/4 pods ready · redis: ImagePullBackOff") {
		t.Errorf("missing failure summary:\n%s", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdef", 4); got != "abc…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("truncate = %q", got)
	}
}
