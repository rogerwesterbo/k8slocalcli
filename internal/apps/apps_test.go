package apps

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"slices"
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
		{"argocd", cluster.AppRequest{Name: "argocd", Method: cluster.MethodHelm, MethodDefaulted: true, Host: "argocd.localtest.me"}, false},
		{"argocd:manifest", cluster.AppRequest{Name: "argocd", Method: cluster.MethodManifest, Host: "argocd.localtest.me"}, false},
		{"argocd=cd.localtest.me", cluster.AppRequest{Name: "argocd", Method: cluster.MethodHelm, MethodDefaulted: true, Host: "cd.localtest.me"}, false},
		{"argocd:manifest=cd.localtest.me", cluster.AppRequest{Name: "argocd", Method: cluster.MethodManifest, Host: "cd.localtest.me"}, false},
		{"nope", cluster.AppRequest{}, true},
		{"argocd:kustomize", cluster.AppRequest{}, true},
		{"argocd:flux", cluster.AppRequest{}, true}, // the controllers cannot install themselves
		{"prometheus", cluster.AppRequest{Name: "kube-prometheus-stack", Method: cluster.MethodHelm, MethodDefaulted: true, Host: "grafana.localtest.me"}, false},
		{"nyancat:argo", cluster.AppRequest{Name: "nyancat", Method: cluster.MethodArgo, Host: "nyancat.localtest.me"}, false},
		{"valkey=v.localtest.me", cluster.AppRequest{}, true}, // nothing to expose
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

// Every catalog entry must be installable, detectable and, if exposable, have
// a default host covered by the localtest.me DNS wildcard and certificate.
func TestCatalogEntries(t *testing.T) {
	seen := map[string]string{}
	for _, a := range Catalog() {
		if len(a.Methods) == 0 || a.Namespace == "" || len(a.Detect) == 0 || a.Description == "" || a.Category == "" {
			t.Errorf("%s: incomplete catalog entry", a.Name)
		}
		for _, n := range append([]string{a.Name}, a.Aliases...) {
			if other, dup := seen[n]; dup {
				t.Errorf("name %q used by both %s and %s", n, other, a.Name)
			}
			seen[n] = a.Name
		}
		for _, m := range a.Methods {
			switch {
			case m == cluster.MethodManifest && a.manifestURL == "",
				m != cluster.MethodManifest && a.chart == nil:
				t.Errorf("%s: offers %s without the means to install it", a.Name, m)
			}
		}
		for _, d := range a.Requires {
			if _, ok := Lookup(d); !ok {
				t.Errorf("%s: requires unknown app %q", a.Name, d)
			}
		}
		if a.Expose != nil && !cluster.IsLocalDomain(a.Expose.DefaultHost) {
			t.Errorf("%s: default host %q is not a single label under localtest.me", a.Name, a.Expose.DefaultHost)
		}
		// Node architecture names as Kubernetes reports them (GOARCH); a typo
		// such as x86_64 would make the precheck refuse every cluster.
		for _, arch := range a.arches() {
			if !slices.Contains([]string{"amd64", "arm64", "arm", "ppc64le", "s390x", "riscv64"}, arch) {
				t.Errorf("%s: unknown architecture %q in Arches", a.Name, arch)
			}
		}
	}
}

// Every chart renders valid values in both profiles, and the same description
// produces parseable Flux and Argo CD objects.
func TestChartsRender(t *testing.T) {
	for _, profile := range cluster.Profiles {
		for _, external := range []bool{false, true} {
			env := &Env{Target: Target{Name: "t", KubeContext: "k8slocalcli-test-nonexistent"}, Profile: profile}
			if !external {
				env.Provider = cluster.ProviderKind
			}
			for _, a := range Catalog() {
				if a.chart == nil {
					continue
				}
				c, err := a.renderChart(t.Context(), env, "x.localtest.me")
				if err != nil {
					t.Errorf("%s/%s: %v", a.Name, profile, err)
					continue
				}
				if c.Release == "" || c.Chart == "" || c.Namespace == "" || (!c.oci() && c.Repo == "") {
					t.Errorf("%s/%s: incomplete chart %+v", a.Name, profile, c)
				}
				if _, err := c.valuesYAML(); err != nil {
					t.Errorf("%s/%s: %v", a.Name, profile, err)
				}
				for method, render := range map[string]func() (string, error){
					"flux": func() (string, error) { return fluxManifest(a.Name, c, time.Minute) },
					"argo": func() (string, error) { return argoManifest(a.Name, c) },
				} {
					m, err := render()
					if err != nil {
						t.Errorf("%s/%s %s: %v", a.Name, profile, method, err)
						continue
					}
					assertKinds(t, a.Name+" "+method, m)
				}
			}
		}
	}
}

// Routing is Gateway API only: nyan-cat's chart enables an Ingress by default,
// so its values must turn it off for every method and profile.
func TestNyanCatHasNoIngress(t *testing.T) {
	for _, profile := range cluster.Profiles {
		env := &Env{Target: Target{Name: "t", Provider: cluster.ProviderKind}, Profile: profile}
		c, err := nyanCatApp.renderChart(t.Context(), env, "nyancat.localtest.me")
		if err != nil {
			t.Fatal(err)
		}
		ingress, _ := c.Values["ingress"].(map[string]any)
		if ingress["enabled"] != false {
			t.Errorf("%s: ingress.enabled = %v, want false", profile, ingress["enabled"])
		}
	}
}

func assertKinds(t *testing.T, name, manifest string) {
	t.Helper()
	for _, doc := range splitDocuments(manifest) {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Errorf("%s: invalid YAML: %v\n%s", name, err, doc)
			continue
		}
		if obj["kind"] == nil || obj["apiVersion"] == nil {
			t.Errorf("%s: document without kind/apiVersion:\n%s", name, doc)
		}
	}
}

// OCI charts use an OCIRepository (Flux) or an OCI repository Secret (Argo
// CD); classic repos a HelmRepository / repoURL.
func TestGitOpsManifests(t *testing.T) {
	oci := helmChart{Release: "nyan-cat", Chart: "oci://ghcr.io/rogerwesterbo/helm/nyan-cat", Namespace: "nyan-cat", Version: "0.2.3",
		Values: map[string]any{"replicaCount": 2}}
	flux, err := fluxManifest("nyancat", oci, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: OCIRepository", "url: oci://ghcr.io/rogerwesterbo/helm/nyan-cat", "tag: 0.2.3",
		"kind: HelmRelease", "chartRef:", "targetNamespace: nyan-cat", "replicaCount: 2", "timeout: 5m0s"} {
		if !strings.Contains(flux, want) {
			t.Errorf("flux manifest missing %q:\n%s", want, flux)
		}
	}
	argo, err := argoManifest("nyancat", oci)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"enableOCI: \"true\"", "repoURL: ghcr.io/rogerwesterbo/helm", "chart: nyan-cat",
		"targetRevision: 0.2.3", "CreateNamespace=true", "valuesObject:"} {
		if !strings.Contains(argo, want) {
			t.Errorf("argo manifest missing %q:\n%s", want, argo)
		}
	}

	classic := helmChart{Release: "valkey", Chart: "valkey", Repo: "https://valkey.io/valkey-helm/", Namespace: "valkey"}
	flux, _ = fluxManifest("valkey", classic, time.Minute)
	for _, want := range []string{"kind: HelmRepository", "version: '*'", "sourceRef:"} {
		if !strings.Contains(flux, want) {
			t.Errorf("flux manifest missing %q:\n%s", want, flux)
		}
	}
	argo, _ = argoManifest("valkey", classic)
	if strings.Contains(argo, "kind: Secret") || !strings.Contains(argo, "repoURL: https://valkey.io/valkey-helm/") {
		t.Errorf("classic chart should need no repository secret:\n%s", argo)
	}
}

// Dependencies come first, are added once, and are marked implicit.
func TestPlan(t *testing.T) {
	plan, added, err := Plan([]cluster.AppRequest{
		{Name: "keycloak"},
		{Name: "nyancat", Method: cluster.MethodFlux},
		{Name: "valkey", Method: cluster.MethodArgo},
		{Name: "cnpg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	pos := map[string]int{}
	for i, r := range plan {
		order = append(order, r.Name)
		pos[r.Name] = i
	}
	for _, dep := range [][2]string{{"cnpg", "keycloak"}, {"flux", "nyancat"}, {"argocd", "valkey"}} {
		if pos[dep[0]] >= pos[dep[1]] {
			t.Errorf("%s must come before %s: %v", dep[0], dep[1], order)
		}
	}
	if len(plan) != 6 {
		t.Errorf("plan = %v, want 6 apps", order)
	}
	if len(added) != 2 {
		t.Errorf("added = %v, want flux and argocd", added)
	}
	for _, r := range plan {
		if want := r.Name == "flux" || r.Name == "argocd"; r.Implicit != want {
			t.Errorf("%s: Implicit = %t, want %t", r.Name, r.Implicit, want)
		}
	}
	if _, _, err := Plan([]cluster.AppRequest{{Name: "cnpg"}, {Name: "postgres"}}); err == nil {
		t.Error("an app and its alias are the same request twice")
	}

	// Storage goes first (apps with volumes check for a default
	// StorageClass), in catalog order; the rest keep the requested order.
	plan, _, err = Plan([]cluster.AppRequest{
		{Name: "kube-prometheus-stack"}, {Name: "nfs"}, {Name: "nyancat"}, {Name: "local-path"},
	})
	if err != nil {
		t.Fatal(err)
	}
	order = nil
	for _, r := range plan {
		order = append(order, r.Name)
	}
	if want := []string{"local-path", "nfs", "kube-prometheus-stack", "nyancat"}; !slices.Equal(order, want) {
		t.Errorf("plan = %v, want %v", order, want)
	}
}

// Presets only name catalog apps, and expanding skips what is already asked.
func TestPresets(t *testing.T) {
	for _, p := range Presets() {
		for _, a := range p.Apps {
			if _, ok := Lookup(a); !ok {
				t.Errorf("preset %s: unknown app %q", p.Name, a)
			}
		}
	}
	got, err := ExpandPresets([]string{"demo", "gitops"}, []cluster.AppRequest{{Name: "nyancat", Method: cluster.MethodFlux}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "k8sdockside,flux,argocd" {
		t.Errorf("expanded = %v", names)
	}
	if _, err := ExpandPresets([]string{"nope"}, nil); err == nil {
		t.Error("unknown preset should fail")
	}
}

func TestParseHelmMajor(t *testing.T) {
	for in, want := range map[string]int{"v4.3.1+gc0d08d0": 4, "v3.17.0+g301108e": 3, "": 0, "garbage": 0} {
		if got := parseHelmMajor(in); got != want {
			t.Errorf("parseHelmMajor(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestChartVersion(t *testing.T) {
	if v := chartVersion("apiVersion: v2\nname: nyan-cat\nversion: 0.2.3\nappVersion: 0.2.3\n"); v != "0.2.3" {
		t.Errorf("chartVersion = %q", v)
	}
}

func TestWithTimeoutNamesPhase(t *testing.T) {
	err := withTimeout(t.Context(), 10*time.Millisecond, "verify", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "verify timed out after 10ms") {
		t.Errorf("err = %v", err)
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
