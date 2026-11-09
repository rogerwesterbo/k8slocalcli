package engine

import "testing"

func TestParsePortOutput(t *testing.T) {
	cases := map[string]int{
		"0.0.0.0:64659":             64659,
		"[::]:64659":                64659,
		"0.0.0.0:64659\n[::]:64659": 64659,
		"":                          0,
		"no port here":              0,
	}
	for in, want := range cases {
		if got := ParsePortOutput(in); got != want {
			t.Errorf("ParsePortOutput(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCurrentIsDocker(t *testing.T) {
	if Current().Binary != "docker" || !Current().PrivilegedPorts {
		t.Errorf("Current() = %+v, want the Docker engine", Current())
	}
}
