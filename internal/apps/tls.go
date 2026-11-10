package apps

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
	"github.com/rogerwesterbo/k8slocalcli/internal/settings"
)

// cert-manager pins.
const (
	certManagerVersion   = "v1.21.3"
	certManagerNamespace = "cert-manager"
	certManagerChart     = "oci://quay.io/jetstack/charts/cert-manager"
	caIssuerName         = "k8slocalcli-ca"
	caSecretName         = "k8slocalcli-ca" // #nosec G101 -- a Secret name, not a credential
	caCommonName         = "k8slocalcli local development CA"
)

// gatewayDNSNames are the names on the gateway certificate.
var gatewayDNSNames = []string{"*." + cluster.LocalDomain, cluster.LocalDomain}

// setupTLS puts the gateway certificate in GatewayNamespace/TLSSecretName.
func setupTLS(ctx context.Context, env *Env, exp cluster.Exposure) error {
	if exp.TLS == cluster.TLSVerified {
		return setupVerifiedTLS(ctx, env, exp.CertManagerMethod)
	}

	fmt.Fprintf(env.Out, "🔐 Created a self-signed certificate for *.%s (browsers will warn)\n", cluster.LocalDomain)
	certPEM, keyPEM, err := selfSignedCert(gatewayDNSNames, time.Now())
	if err != nil {
		return err
	}
	if err := env.applyYAML(ctx, tlsSecretYAML(TLSSecretName, GatewayNamespace, certPEM, keyPEM)); err != nil {
		return fmt.Errorf("creating TLS secret: %w", err)
	}
	return nil
}

// setupVerifiedTLS installs cert-manager, gives it the k8slocalcli CA as a
// ClusterIssuer, issues the gateway certificate from it, and offers to trust
// the CA on this machine. The CA is shared by every cluster, so it only has to
// be trusted once.
func setupVerifiedTLS(ctx context.Context, env *Env, method cluster.InstallMethod) error {
	caCert, caKey, caPath, err := loadOrCreateCA(time.Now())
	if err != nil {
		return err
	}

	title := fmt.Sprintf("Installing cert-manager %s via %s, issuing the *.%s certificate", certManagerVersion, method, cluster.LocalDomain)
	if err := env.step(ctx, title, certManagerNamespace, func(se *Env) error {
		return issueVerifiedCert(ctx, se, method, caCert, caKey)
	}); err != nil {
		return err
	}

	// Outside the step: it may prompt, and the prompt must stay visible.
	trustCAOnHost(ctx, env, caPath)
	return nil
}

// issueVerifiedCert installs cert-manager and issues the gateway certificate
// from the k8slocalcli CA.
func issueVerifiedCert(ctx context.Context, env *Env, method cluster.InstallMethod, caCert, caKey []byte) error {
	if err := installCertManager(ctx, env, method); err != nil {
		return fmt.Errorf("installing cert-manager: %w", err)
	}
	issuer := tlsSecretYAML(caSecretName, certManagerNamespace, caCert, caKey) + "---\n" + caIssuerYAML()
	// The webhook can reject requests for a short while after its deployment
	// reports Available.
	if err := env.applyYAMLRetry(ctx, issuer, 2*time.Minute); err != nil {
		return fmt.Errorf("creating CA issuer: %w", err)
	}
	if err := env.applyYAMLRetry(ctx, certificateYAML(), time.Minute); err != nil {
		return fmt.Errorf("requesting gateway certificate: %w", err)
	}
	if err := env.kubectl(ctx, "wait", "--for=condition=Ready", "certificate/"+TLSSecretName,
		"-n", GatewayNamespace, "--timeout=3m"); err != nil {
		return fmt.Errorf("gateway certificate not issued in time: %w", err)
	}
	return nil
}

func installCertManager(ctx context.Context, env *Env, method cluster.InstallMethod) error {
	if err := installApp(ctx, env, certManagerApp, method, ""); err != nil {
		return err
	}
	// The verified-TLS flow applies cert-manager resources right after this,
	// so wait for its workloads here (Install does this in its verify step).
	return verifyWorkloads(ctx, env, certManagerApp)
}

func caIssuerYAML() string {
	return fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: %s
spec:
  ca:
    secretName: %s
`, caIssuerName, caSecretName)
}

func certificateYAML() string {
	return fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  secretName: %[1]s
  dnsNames:
    - "%[3]s"
    - "%[4]s"
  issuerRef:
    kind: ClusterIssuer
    name: %[5]s
`, TLSSecretName, GatewayNamespace, gatewayDNSNames[0], gatewayDNSNames[1], caIssuerName)
}

func tlsSecretYAML(name, namespace string, certPEM, keyPEM []byte) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: kubernetes.io/tls
data:
  tls.crt: %s
  tls.key: %s
`, name, namespace, base64.StdEncoding.EncodeToString(certPEM), base64.StdEncoding.EncodeToString(keyPEM))
}

// selfSignedCert returns a one-year self-signed serving certificate for names.
func selfSignedCert(names []string, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: names[0], Organization: []string{"k8slocalcli"}},
		DNSNames:              names,
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	return encodePair(der, key)
}

// newCA returns a ten-year CA that can only issue for the localtest.me domain.
// The name constraint limits the damage if the key leaks: it is copied into
// every cluster's cert-manager namespace and is trusted by this machine.
func newCA(now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: caCommonName, Organization: []string{"k8slocalcli"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		PermittedDNSDomains:   []string{cluster.LocalDomain},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	return encodePair(der, key)
}

// caDir is where the shared CA lives: ~/.k8slocalcli/ca.
func caDir() (string, error) {
	dir, err := settings.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ca"), nil
}

// loadOrCreateCA returns the shared CA, creating it on first use, plus the
// path of its certificate.
func loadOrCreateCA(now time.Time) (certPEM, keyPEM []byte, certPath string, err error) {
	dir, err := caDir()
	if err != nil {
		return nil, nil, "", err
	}
	certPath = filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	certPEM, certErr := os.ReadFile(certPath) // #nosec G304 -- fixed path under ~/.k8slocalcli
	keyPEM, keyErr := os.ReadFile(keyPath)    // #nosec G304 -- fixed path under ~/.k8slocalcli
	if certErr == nil && keyErr == nil {
		return certPEM, keyPEM, certPath, nil
	}
	// Only a missing file means "create"; anything else (permissions, ...)
	// must not silently replace a CA that may already be trusted.
	for _, e := range []error{certErr, keyErr} {
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, nil, "", fmt.Errorf("reading local CA in %s: %w", dir, e)
		}
	}

	certPEM, keyPEM, err = newCA(now)
	if err != nil {
		return nil, nil, "", fmt.Errorf("creating local CA: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, nil, "", err
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return nil, nil, "", err
	}
	return certPEM, keyPEM, certPath, nil
}

// trustCAOnHost makes this machine trust the CA, after asking. On macOS it is
// added to the login keychain (macOS asks for the password); elsewhere the
// commands are printed, since they need root.
func trustCAOnHost(ctx context.Context, env *Env, caPath string) {
	if runtime.GOOS != "darwin" || !runner.LookPath("security") {
		printTrustInstructions(env, caPath)
		return
	}
	// verify-cert succeeds only once the CA is a trusted root.
	if _, err := runner.New(nil).Capture(ctx, "security", "verify-cert", "-c", caPath); err == nil {
		fmt.Fprintf(env.Out, "✅ The k8slocalcli CA is already trusted on this machine\n")
		return
	}

	ok, err := env.Confirm(fmt.Sprintf("🔐 Add the k8slocalcli CA (%s) to your login keychain so browsers trust *.%s?", caPath, cluster.LocalDomain), true)
	if err != nil || !ok {
		printTrustInstructions(env, caPath)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		printTrustInstructions(env, caPath)
		return
	}
	keychain := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
	if err := env.runner().Run(ctx, "security", "add-trusted-cert", "-r", "trustRoot", "-k", keychain, caPath); err != nil {
		fmt.Fprintf(env.Out, "⚠️  Could not trust the CA: %v\n", err)
		printTrustInstructions(env, caPath)
		return
	}
	fmt.Fprintf(env.Out, "✅ The k8slocalcli CA is now trusted (Firefox uses its own store; set NODE_EXTRA_CA_CERTS=%s for Node.js)\n", caPath)
}

func printTrustInstructions(env *Env, caPath string) {
	fmt.Fprintf(env.Out, "\nℹ️  To trust HTTPS endpoints, trust the k8slocalcli CA at %s:\n", caPath)
	switch runtime.GOOS {
	case "darwin":
		fmt.Fprintf(env.Out, "   security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db %s\n", caPath)
	case "linux":
		fmt.Fprintf(env.Out, "   sudo cp %s /usr/local/share/ca-certificates/k8slocalcli.crt && sudo update-ca-certificates   # Debian/Ubuntu\n", caPath)
		fmt.Fprintf(env.Out, "   sudo cp %s /etc/pki/ca-trust/source/anchors/k8slocalcli.crt && sudo update-ca-trust          # Fedora/RHEL\n", caPath)
	case "windows":
		fmt.Fprintf(env.Out, "   certutil -user -addstore Root %s\n", caPath)
	}
}

func randomSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

func encodePair(der []byte, key *ecdsa.PrivateKey) (certPEM, keyPEM []byte, err error) {
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}
