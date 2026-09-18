package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// Unlike kind, which puts every cluster on the shared "kind" network, talosctl
// creates one Docker network per cluster and defaults all of them to the same
// 10.5.0.0/24 (`talosctl cluster create docker --subnet`). Docker refuses
// overlapping pools:
//
//	Error response from daemon: invalid pool request: Pool overlaps with other
//	one on this address space
//
// so a second Talos cluster cannot be created without moving it to a free
// subnet. talosTakenSubnet scans the third octet for one nothing else is using.
const (
	talosDefaultSubnet = "10.5.0.0/24"
	// talosSubnetScanLimit bounds the third octet search (10.5.0.0/24 ..
	// 10.5.255.0/24 would be excessive; a handful of local clusters is the case
	// worth supporting).
	talosSubnetScanLimit = 32
)

// dockerSubnets returns every subnet currently allocated to a Docker network.
// A failure is reported so callers can fall back to talosctl's default rather
// than guessing.
func dockerSubnets(ctx context.Context) ([]string, error) {
	r := runner.New(nil)
	ids, err := r.Capture(ctx, "docker", "network", "ls", "-q")
	if err != nil {
		return nil, err
	}
	networks := nonEmptyLines(ids)
	if len(networks) == 0 {
		return nil, nil
	}

	args := append([]string{"network", "inspect"}, networks...)
	args = append(args, "--format", "{{range .IPAM.Config}}{{.Subnet}} {{end}}")
	out, err := r.Capture(ctx, "docker", args...)
	if err != nil {
		return nil, err
	}

	var subnets []string
	for _, line := range nonEmptyLines(out) {
		subnets = append(subnets, strings.Fields(line)...)
	}
	return subnets, nil
}

// pickTalosSubnet returns a /24 in 10.5.x.0/24 that none of the given subnets
// occupy, or "" when the default is free (meaning: pass nothing and let talosctl
// decide) or nothing in range is available.
func pickTalosSubnet(allocated []string) string {
	taken := map[string]bool{}
	for _, s := range allocated {
		taken[s] = true
	}
	if !taken[talosDefaultSubnet] {
		return ""
	}

	for octet := 1; octet < talosSubnetScanLimit; octet++ {
		candidate := fmt.Sprintf("10.5.%d.0/24", octet)
		if !taken[candidate] {
			return candidate
		}
	}
	return ""
}

// freeTalosSubnet asks Docker what is allocated and picks a free /24.
func freeTalosSubnet(ctx context.Context) string {
	subnets, err := dockerSubnets(ctx)
	if err != nil {
		return "" // Docker unreachable: let talosctl use its default and fail loudly.
	}
	return pickTalosSubnet(subnets)
}
