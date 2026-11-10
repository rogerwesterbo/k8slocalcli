package provider

import "testing"

func TestLBRange(t *testing.T) {
	tests := map[string]string{
		"172.18.0.0/16":           "172.18.255.200-172.18.255.250",
		"10.5.0.0/24":             "10.5.0.200-10.5.0.250",
		"10.5.3.0/24":             "10.5.3.200-10.5.3.250",
		"10.0.0.0/26":             "",
		"fc00:f853:ccd:e793::/64": "",
		"garbage":                 "",
	}
	for in, want := range tests {
		if got := lbRange(in); got != want {
			t.Errorf("lbRange(%q) = %q, want %q", in, got, want)
		}
	}
}
