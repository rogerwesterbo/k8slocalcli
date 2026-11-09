package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// prompter asks yes/no questions on the terminal. assumeYes (the --yes flag)
// answers every question with yes without asking.
type prompter struct {
	in          io.Reader
	out         io.Writer
	assumeYes   bool
	interactive bool // false when stdin is not a terminal (CI, pipes)
}

func newPrompter(out io.Writer, assumeYes bool) *prompter {
	return &prompter{in: os.Stdin, out: out, assumeYes: assumeYes, interactive: stdinIsTerminal()}
}

// errNeedsConfirmation is returned when a question cannot be asked because
// stdin is not a terminal and --yes was not given.
var errNeedsConfirmation = fmt.Errorf("confirmation required: re-run in a terminal or pass --yes")

// confirm prints question with a [Y/n] or [y/N] hint (per def) and returns the
// answer. An empty answer returns def.
func (p *prompter) confirm(question string, def bool) (bool, error) {
	if p.assumeYes {
		fmt.Fprintf(p.out, "%s yes (--yes)\n", question)
		return true, nil
	}
	if !p.interactive {
		return false, errNeedsConfirmation
	}
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Fprintf(p.out, "%s %s ", question, hint)
	line, err := bufio.NewReader(p.in).ReadString('\n')
	if err != nil && line == "" {
		return false, nil // EOF / Ctrl-D: treat as "no"
	}
	return parseAnswer(line, def), nil
}

func parseAnswer(line string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	default:
		return false
	}
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
