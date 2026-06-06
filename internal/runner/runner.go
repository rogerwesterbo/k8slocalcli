// Package runner provides a small helper for executing external CLI tools
// (kind, talosctl, kubectl, docker) and streaming their output. Both providers
// share it so command execution, logging and error wrapping are consistent.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Runner executes external commands and writes their combined stdout/stderr to
// Out. A zero Runner discards output; set Out to stream progress to the user.
type Runner struct {
	// Out receives the live combined output of every Run call. If nil, output
	// is discarded (but still captured for error messages).
	Out io.Writer
}

// New returns a Runner that streams output to out.
func New(out io.Writer) *Runner {
	return &Runner{Out: out}
}

// Run executes name with args, streaming output to r.Out. It returns an error
// that includes the tail of the command output when the command fails.
func (r *Runner) Run(ctx context.Context, name string, args ...string) error {
	_, _, err := r.run(ctx, name, args...)
	return err
}

// Capture executes name with args and returns the trimmed standard output only.
// Diagnostics on stderr (e.g. kind's "No kind clusters found.") are streamed to
// r.Out and used for error context, but are not part of the returned value so
// callers can safely parse the result line by line.
func (r *Runner) Capture(ctx context.Context, name string, args ...string) (string, error) {
	stdout, _, err := r.run(ctx, name, args...)
	return strings.TrimSpace(stdout), err
}

// run executes the command, returning stdout and stderr separately. Both are
// streamed to r.Out (when set) so the user still sees live progress.
func (r *Runner) run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error) {
	if r.Out != nil {
		fmt.Fprintf(r.Out, "$ %s %s\n", name, strings.Join(args, " "))
	}

	// The command name is always a hardcoded tool ("kind", "talosctl",
	// "kubectl", "docker") chosen by the providers, never user input. Argument
	// values such as the cluster name are validated by cluster.Spec.Validate()
	// before reaching here. This is the intended behaviour of an orchestrator.
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204

	var outBuf, errBuf bytes.Buffer
	// Mirror each stream to the caller-visible writer and to a buffer so we can
	// return stdout for parsing and surface the tail of output on failure.
	var outSink, errSink io.Writer = &outBuf, &errBuf
	if r.Out != nil {
		outSink = io.MultiWriter(&outBuf, r.Out)
		errSink = io.MultiWriter(&errBuf, r.Out)
	}
	cmd.Stdout = outSink
	cmd.Stderr = errSink

	if err := cmd.Run(); err != nil {
		combined := strings.TrimSpace(outBuf.String() + "\n" + errBuf.String())
		return outBuf.String(), errBuf.String(),
			fmt.Errorf("%s %s: %w%s", name, strings.Join(args, " "), err, tail(combined))
	}
	return outBuf.String(), errBuf.String(), nil
}

// tail returns the last few lines of s, formatted for inclusion in an error.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	const maxLines = 8
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return "\n  " + strings.Join(lines, "\n  ")
}

// LookPath reports whether the named executable is found on PATH.
func LookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
