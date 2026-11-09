package provider

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestCABundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no default ca-certificates dir
	dir := t.TempDir()
	ca := testCAPEM(t)

	good := filepath.Join(dir, "ca.pem")
	// A private key in the same file must not end up in the bundle.
	if err := os.WriteFile(good, []byte(ca+"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, sources, err := caBundle([]string{good, good})
	if err != nil {
		t.Fatalf("caBundle: %v", err)
	}
	if bundle != ca || len(sources) != 1 {
		t.Errorf("bundle/sources unexpected: sources=%v, bundle=%q", sources, bundle)
	}

	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not a cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := caBundle([]string{bad}); err == nil {
		t.Error("expected error for a file without certificates")
	}
	if _, _, err := caBundle([]string{filepath.Join(dir, "missing.pem")}); err == nil {
		t.Error("expected error for a missing file")
	}
}

func TestCABundleDefaultDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	certDir := filepath.Join(home, ".k8slocalcli", caCertsDirName)
	if err := os.MkdirAll(certDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "corp.crt"), []byte(testCAPEM(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, sources, err := caBundle(nil); err != nil || len(sources) != 1 {
		t.Errorf("default dir not picked up: sources=%v err=%v", sources, err)
	}
}

func TestTalosPatches(t *testing.T) {
	p := talosTrustedRootsPatch("-----BEGIN CERTIFICATE-----\nAB\n-----END CERTIFICATE-----\n")
	if !strings.Contains(p, "kind: TrustedRootsConfig") || !strings.Contains(p, "\n  AB\n") {
		t.Errorf("trusted roots patch unexpected:\n%s", p)
	}
	if p := talosDiscoveryOffPatch("1.14"); !strings.Contains(p, "kind: DiscoveryServiceConfig") {
		t.Errorf("1.14 discovery patch unexpected:\n%s", p)
	}
	if p := talosDiscoveryOffPatch("1.13"); !strings.Contains(p, "enabled: false") {
		t.Errorf("1.13 discovery patch unexpected:\n%s", p)
	}
}
