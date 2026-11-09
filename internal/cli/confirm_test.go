package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseAnswer(t *testing.T) {
	cases := []struct {
		in       string
		def      bool
		expected bool
	}{
		{"\n", true, true},
		{"\n", false, false},
		{"y\n", false, true},
		{"YES\n", false, true},
		{"n\n", true, false},
		{"whatever\n", true, false},
	}
	for _, c := range cases {
		if got := parseAnswer(c.in, c.def); got != c.expected {
			t.Errorf("parseAnswer(%q, %v) = %v, want %v", c.in, c.def, got, c.expected)
		}
	}
}

func TestPrompter(t *testing.T) {
	var out bytes.Buffer

	yes := &prompter{out: &out, assumeYes: true}
	if ok, err := yes.confirm("q?", false); !ok || err != nil {
		t.Errorf("--yes: got (%v, %v), want (true, nil)", ok, err)
	}

	ci := &prompter{out: &out}
	if _, err := ci.confirm("q?", true); !errors.Is(err, errNeedsConfirmation) {
		t.Errorf("non-interactive: err = %v, want errNeedsConfirmation", err)
	}

	tty := &prompter{in: strings.NewReader("\n"), out: &out, interactive: true}
	if ok, _ := tty.confirm("q?", true); !ok {
		t.Error("empty answer should take the default (yes)")
	}
	eof := &prompter{in: strings.NewReader(""), out: &out, interactive: true}
	if ok, _ := eof.confirm("q?", true); ok {
		t.Error("EOF should answer no")
	}
}
