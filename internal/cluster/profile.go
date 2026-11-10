package cluster

// Profile selects how catalog apps are sized and configured.
//
// k8slocalcli is a development tool, and nothing it installs is meant to carry
// production traffic. The production profile exists so a cluster can be shaped
// like a real one — to test upgrades, failover, persistence and resource
// pressure — not so it can be relied on.
type Profile string

const (
	// ProfileDev is for throwaway testing: single replicas, no persistence
	// where the app allows it, dev modes (OpenBao dev server, Keycloak
	// start-dev), well-known credentials only where the app would otherwise be
	// unusable.
	ProfileDev Profile = "dev"
	// ProfileProduction is "near production": multiple replicas, persistent
	// volumes, resource requests, real server modes, generated credentials.
	ProfileProduction Profile = "production"
)

// Profiles is the ordered list of profiles (index 0 is the default).
var Profiles = []Profile{ProfileDev, ProfileProduction}

// Valid reports whether p is a known profile.
func (p Profile) Valid() bool {
	for _, known := range Profiles {
		if p == known {
			return true
		}
	}
	return false
}

// OrDefault returns p, or ProfileDev when p is empty.
func (p Profile) OrDefault() Profile {
	if p == "" {
		return ProfileDev
	}
	return p
}

// Production reports whether p asks for the near-production setup.
func (p Profile) Production() bool { return p == ProfileProduction }

// Describe is a short human label for p.
func (p Profile) Describe() string {
	if p.Production() {
		return "near-production (HA replicas, persistence, generated credentials)"
	}
	return "dev/testing (single replicas, minimal resources, dev modes)"
}
