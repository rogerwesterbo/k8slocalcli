package provider

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rogerwesterbo/k8slocalcli/internal/settings"
)

// caCertsDirName is the directory under ~/.k8slocalcli whose *.pem / *.crt
// files are trusted by every node without passing --ca-cert. Useful behind a
// TLS-intercepting corporate proxy, where image pulls from the nodes fail with
// "x509: certificate signed by unknown authority".
const caCertsDirName = "ca-certificates"

// caBundle collects the CA certificates the nodes should trust: the explicit
// files, then any in ~/.k8slocalcli/ca-certificates. It returns the
// concatenated PEM bundle (only CERTIFICATE blocks, each validated) and the
// files it came from. A missing default directory is not an error.
func caBundle(explicit []string) (bundle string, sources []string, err error) {
	files := slices.Clone(explicit)
	if dir, derr := settings.ConfigDir(); derr == nil {
		for _, pattern := range []string{"*.pem", "*.crt"} {
			m, _ := filepath.Glob(filepath.Join(dir, caCertsDirName, pattern))
			files = append(files, m...)
		}
	}

	var b strings.Builder
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		data, err := os.ReadFile(f) // #nosec G304 -- user-chosen CA file
		if err != nil {
			return "", nil, fmt.Errorf("reading CA certificate %s: %w", f, err)
		}
		certs, err := pemCertificates(data)
		if err != nil {
			return "", nil, fmt.Errorf("CA certificate %s: %w", f, err)
		}
		b.WriteString(certs)
		sources = append(sources, f)
	}
	return b.String(), sources, nil
}

// pemCertificates returns the parseable CERTIFICATE blocks of data re-encoded
// as PEM, and an error when there are none. Other block types (e.g. a private
// key accidentally in the same file) are dropped rather than shipped to nodes.
func pemCertificates(data []byte) (string, error) {
	var b strings.Builder
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return "", fmt.Errorf("invalid certificate: %w", err)
		}
		b.Write(pem.EncodeToMemory(block))
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("no PEM certificates found")
	}
	return b.String(), nil
}

// talosTrustedRootsPatch returns a machine-config patch adding bundle to the
// nodes' trusted roots, so containerd can pull through an intercepting proxy.
func talosTrustedRootsPatch(bundle string) string {
	var b strings.Builder
	b.WriteString("apiVersion: v1alpha1\nkind: TrustedRootsConfig\nname: k8slocalcli\ncertificates: |-\n")
	for _, line := range strings.Split(strings.TrimRight(bundle, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// talosDiscoveryOffPatch disables the Talos discovery service. It is only used
// for KubeSpan and membership across networks, which a single-host Docker
// cluster does not need, and otherwise produces a stream of "hello failed"
// errors offline or behind a proxy. talosctl 1.14 moved discovery into its own
// DiscoveryServiceConfig document; older versions use cluster.discovery.
func talosDiscoveryOffPatch(talosMajorMinor string) string {
	if talosAtLeast(talosMajorMinor, 1, 14) {
		return "apiVersion: v1alpha1\nkind: DiscoveryServiceConfig\nname: default\n$patch: delete\n"
	}
	return "cluster:\n  discovery:\n    enabled: false\n"
}
