// Package progress shows long-running steps to the user. On a terminal a step
// is one live line — spinner, title, elapsed time and a status (e.g. pod
// readiness) — while the commands' own output is held back, and the line turns
// into ✅/❌ when the step ends. Elsewhere (CI, pipes, --verbose) the title is
// printed and the output streams through as before, with status changes logged.
package progress

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

var frames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// StatusFunc reports what the step is waiting on; "" means nothing to say.
type StatusFunc func(ctx context.Context) string

// Reporter runs steps against one output.
type Reporter struct {
	Out io.Writer
	// Live enables the spinner; set only when Out is an interactive terminal.
	Live bool
	// Width is the terminal width used to keep the live line on one row.
	Width int
	// statusEvery is how often status is polled (tests shorten it).
	statusEvery time.Duration
}

// New returns a Reporter for out. live should be true only for a terminal.
func New(out io.Writer, live bool, width int) *Reporter {
	if width < 20 {
		width = 80
	}
	return &Reporter{Out: out, Live: live, Width: width, statusEvery: 3 * time.Second}
}

// Step runs fn as one user-visible step. fn writes command output to the
// writer it is given; status (optional) is polled while it runs.
func (r *Reporter) Step(ctx context.Context, title string, status StatusFunc, fn func(out io.Writer) error) error {
	if !r.Live {
		return r.plain(ctx, title, status, fn)
	}

	var buf bytes.Buffer
	var mu sync.Mutex
	current := ""
	start := time.Now()
	done := make(chan struct{})
	var wg sync.WaitGroup

	if status != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(r.statusEvery)
			defer t.Stop()
			for {
				s := status(ctx)
				mu.Lock()
				current = s
				mu.Unlock()
				select {
				case <-done:
					return
				case <-t.C:
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			mu.Lock()
			s := current
			mu.Unlock()
			line := fmt.Sprintf("%c %s · %s", frames[i%len(frames)], title, elapsed(start))
			if s != "" {
				line += " · " + s
			}
			fmt.Fprintf(r.Out, "\r\x1b[2K%s", truncate(line, r.Width-1))
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()

	err := fn(&lockedWriter{w: &buf, mu: &mu})
	close(done)
	wg.Wait()

	fmt.Fprint(r.Out, "\r\x1b[2K")
	if err != nil {
		fmt.Fprintf(r.Out, "❌ %s (%s)\n", title, elapsed(start))
		mu.Lock()
		if current != "" {
			fmt.Fprintf(r.Out, "   last status: %s\n", current)
		}
		mu.Unlock()
		return err
	}
	fmt.Fprintf(r.Out, "✅ %s (%s)\n", title, elapsed(start))
	return nil
}

// plain prints the title, streams output, and logs status changes.
func (r *Reporter) plain(ctx context.Context, title string, status StatusFunc, fn func(out io.Writer) error) error {
	fmt.Fprintf(r.Out, "\n⏳ %s\n", title)
	start := time.Now()
	done := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	out := &lockedWriter{w: r.Out, mu: &mu}
	if status != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Log at a calmer pace than the live line; only changes.
			t := time.NewTicker(5 * r.statusEvery)
			defer t.Stop()
			last := ""
			for {
				select {
				case <-done:
					return
				case <-t.C:
				}
				if s := status(ctx); s != "" && s != last {
					last = s
					fmt.Fprintf(out, "   … %s · %s\n", elapsed(start), s)
				}
			}
		}()
	}
	err := fn(out)
	close(done)
	wg.Wait()
	if err == nil {
		fmt.Fprintf(r.Out, "✅ %s (%s)\n", title, elapsed(start))
	}
	return err
}

// lockedWriter serialises writes from the step and the status logger.
type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func elapsed(start time.Time) string {
	d := time.Since(start).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// truncate shortens s to max runes so the live line never wraps (a wrapped
// line cannot be redrawn in place with \r).
func truncate(s string, max int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= max {
		return s
	}
	if max < 2 {
		return ""
	}
	return string(r[:max-1]) + "…"
}
