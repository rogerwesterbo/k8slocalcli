package apps

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
)

func TestParseRequest(t *testing.T) {
	tests := []struct {
		in      string
		want    cluster.AppRequest
		wantErr bool
	}{
		{"argocd", cluster.AppRequest{Name: "argocd", Method: cluster.MethodHelm, Host: "argocd.localtest.me"}, false},
		{"argocd:manifest", cluster.AppRequest{Name: "argocd", Method: cluster.MethodManifest, Host: "argocd.localtest.me"}, false},
		{"argocd=cd.localtest.me", cluster.AppRequest{Name: "argocd", Method: cluster.MethodHelm, Host: "cd.localtest.me"}, false},
		{"argocd:manifest=cd.localtest.me", cluster.AppRequest{Name: "argocd", Method: cluster.MethodManifest, Host: "cd.localtest.me"}, false},
		{"nope", cluster.AppRequest{}, true},
		{"argocd:kustomize", cluster.AppRequest{}, true},
		{"argocd=Bad_Host", cluster.AppRequest{}, true},
	}
	for _, tc := range tests {
		got, err := ParseRequest(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseRequest(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("ParseRequest(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

// Every catalog entry must be installable and, if exposable, have a default
// host covered by the localtest.me DNS wildcard and certificate.
func TestCatalogEntries(t *testing.T) {
	for _, a := range Catalog() {
		if len(a.Methods) == 0 || a.install == nil || a.Namespace == "" {
			t.Errorf("%s: incomplete catalog entry", a.Name)
		}
		if a.Expose != nil && !cluster.IsLocalDomain(a.Expose.DefaultHost) {
			t.Errorf("%s: default host %q is not a single label under localtest.me", a.Name, a.Expose.DefaultHost)
		}
	}
}

// The first cluster gets bare URLs; later clusters carry their ports.
func TestTargetURL(t *testing.T) {
	first := Target{HTTPPort: 80, HTTPSPort: 443}
	if got := first.URL("http", "argocd.localtest.me"); got != "http://argocd.localtest.me" {
		t.Errorf("got %s", got)
	}
	if got := first.URL("https", "argocd.localtest.me"); got != "https://argocd.localtest.me" {
		t.Errorf("got %s", got)
	}
	second := Target{HTTPPort: 8080, HTTPSPort: 8443}
	if got := second.URL("http", "argocd.localtest.me"); got != "http://argocd.localtest.me:8080" {
		t.Errorf("got %s", got)
	}
	if got := second.URL("https", "argocd.localtest.me"); got != "https://argocd.localtest.me:8443" {
		t.Errorf("got %s", got)
	}
}

func TestWithoutGatewayAPIResources(t *testing.T) {
	manifest := `# leading comment
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: httproutes.gateway.networking.k8s.io
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: xmeshes.gateway.networking.x-k8s.io
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: "safe-upgrades.gateway.networking.k8s.io"
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: envoyproxies.gateway.envoyproxy.io
---
apiVersion: v1
kind: Namespace
metadata:
  name: envoy-gateway-system
`
	got, dropped, err := withoutGatewayAPIResources(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 3 {
		t.Errorf("dropped = %d, want 3", dropped)
	}
	for _, gone := range []string{"httproutes.", "xmeshes.", "safe-upgrades."} {
		if strings.Contains(got, gone) {
			t.Errorf("%s should have been dropped:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"envoyproxies.gateway.envoyproxy.io", "kind: Namespace"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s should have been kept:\n%s", kept, got)
		}
	}
}

// The generated manifests must at least be valid YAML with the expected kinds.
func TestGeneratedManifestsParse(t *testing.T) {
	docs := map[string]string{
		"gateway":     gatewayYAML(envoyClassName),
		"httproute":   httpRouteYAML("argocd", "argocd", "argocd.localtest.me", "argocd-server", 80),
		"grpcroute":   grpcRouteYAML("api", "api", "api.localtest.me", "api", 9090),
		"envoy":       envoyClassYAML("demo-control-plane"),
		"issuer":      caIssuerYAML(),
		"certificate": certificateYAML(),
		"secret":      tlsSecretYAML("s", "ns", []byte("c"), []byte("k")),
	}
	for name, manifest := range docs {
		for _, doc := range splitDocuments(manifest) {
			var obj map[string]any
			if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
				t.Errorf("%s: invalid YAML: %v\n%s", name, err, doc)
			}
			if obj["kind"] == nil {
				t.Errorf("%s: document without kind:\n%s", name, doc)
			}
		}
	}
	envoy := envoyClassYAML("demo-control-plane")
	for _, want := range []string{
		"nodePort: 30080", "nodePort: 30443",
		// Pinned to the node the host ports enter on: forwarding to another
		// node over VXLAN drops Docker Desktop's checksum-offloaded packets.
		"kubernetes.io/hostname: demo-control-plane",
		"key: node-role.kubernetes.io/control-plane",
		"externalTrafficPolicy: Local",
	} {
		if !strings.Contains(envoy, want) {
			t.Errorf("EnvoyProxy is missing %q:\n%s", want, envoy)
		}
	}
}

func TestSelfSignedCert(t *testing.T) {
	certPEM, keyPEM, err := selfSignedCert(gatewayDNSNames, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(keyPEM) == 0 {
		t.Fatal("no key")
	}
	cert := parseCert(t, certPEM)
	if err := cert.VerifyHostname("argocd.localtest.me"); err != nil {
		t.Errorf("wildcard should cover argocd.localtest.me: %v", err)
	}
}

// A leaf from the CA verifies for localtest.me names, and the name constraint
// stops the CA from vouching for anything else even if its key leaks.
func TestCANameConstraint(t *testing.T) {
	now := time.Now()
	caPEM, caKeyPEM, err := newCA(now)
	if err != nil {
		t.Fatal(err)
	}
	ca := parseCert(t, caPEM)
	block, _ := pem.Decode(caKeyPEM)
	caKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)

	issue := func(name string) *x509.Certificate {
		tmpl := &x509.Certificate{
			SerialNumber: randomSerial(),
			Subject:      pkix.Name{CommonName: name},
			DNSNames:     []string{name},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &caKey.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	if _, err := issue("*.localtest.me").Verify(x509.VerifyOptions{Roots: roots, DNSName: "argocd.localtest.me"}); err != nil {
		t.Errorf("localtest.me leaf should verify: %v", err)
	}
	if _, err := issue("example.com").Verify(x509.VerifyOptions{Roots: roots, DNSName: "example.com"}); err == nil {
		t.Error("example.com leaf must be rejected by the name constraint")
	}
}

func parseCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
