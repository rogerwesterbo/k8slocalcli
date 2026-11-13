package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rogerwesterbo/k8slocalcli/internal/cluster"
	"github.com/rogerwesterbo/k8slocalcli/internal/runner"
)

// TestImageArchAudit renders every app (chart: both profiles, kind and Talos
// values; manifest: the pinned URL) plus the Envoy Gateway install, collects
// every image it references, and checks in each image's manifest list that it
// is built for all of the app's Arches (default amd64 and arm64). A pinned
// version with a broken image index — Prometheus Operator v0.95.1 labelled
// every platform amd64 (Nov 2026) — fails here instead of on a user's Mac.
//
// It needs helm and docker and talks to the registries, so it is opt-in;
// "1" audits everything, or name the apps (and/or envoy-gateway) just bumped:
//
//	K8SLOCALCLI_IMAGE_AUDIT=1 go test ./internal/apps -run TestImageArchAudit -v -timeout 30m
//	K8SLOCALCLI_IMAGE_AUDIT=kube-prometheus-stack go test ./internal/apps -run TestImageArchAudit -v
//
// Images an operator picks at runtime are covered only when they appear in
// the rendered objects (a flag like --prometheus-config-reloader=<image>, a
// custom resource's image field). Run it after adding an app or bumping a
// version.
func TestImageArchAudit(t *testing.T) {
	selection := os.Getenv("K8SLOCALCLI_IMAGE_AUDIT")
	if selection == "" {
		t.Skip("set K8SLOCALCLI_IMAGE_AUDIT=1 (or app names) to run (needs helm, docker and registry access)")
	}
	audited := func(source string) bool {
		return selection == "1" || slices.Contains(strings.Split(selection, ","), source)
	}
	if !runner.LookPath("helm") || !runner.LookPath("docker") {
		t.Skip("needs helm and docker")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Minute)
	defer cancel()

	// source → images, and the architectures each source must provide.
	images := map[string][]string{}
	want := map[string][]string{}
	add := func(source, yaml string, arches []string) {
		images[source] = mergeSorted(images[source], imageRefs(yaml))
		want[source] = arches
	}
	for _, a := range Catalog() {
		if !audited(a.Name) {
			continue
		}
		if a.chart != nil {
			for _, provider := range []cluster.Provider{cluster.ProviderKind, cluster.ProviderTalos} {
				for _, profile := range cluster.Profiles {
					// A context that does not exist: values that read the
					// cluster fall back to their defaults, and nothing ever
					// reaches the user's current cluster.
					env := &Env{Target: Target{Name: "audit", KubeContext: "k8slocalcli-image-audit", Provider: provider}, Profile: profile}
					rendered, err := renderChartYAML(ctx, env, a, a.Namespace)
					if err != nil {
						t.Errorf("%s (%s, %s): %v", a.Name, provider, profile, err)
						continue
					}
					add(a.Name, rendered, a.arches())
				}
			}
		}
		if a.manifestURL != "" {
			manifest, err := fetch(ctx, a.manifestURL)
			if err != nil {
				t.Errorf("%s manifest: %v", a.Name, err)
				continue
			}
			add(a.Name, manifest, a.arches())
		}
	}
	if audited("envoy-gateway") {
		gateway, err := fetch(ctx, envoyReleaseURL("install.yaml"))
		if err != nil {
			t.Errorf("Envoy Gateway install.yaml: %v", err)
		} else {
			add("envoy-gateway", gateway, defaultArches)
		}
	}
	if len(images) == 0 {
		t.Fatalf("K8SLOCALCLI_IMAGE_AUDIT=%s matches no app", selection)
	}

	// kpsInstallCRDs installs the CRDs of kpsOperatorVersion, so it must be
	// the operator the chart runs.
	if refs, ok := images[kubePrometheusStackApp.Name]; ok {
		if op := "quay.io/prometheus-operator/prometheus-operator:" + kpsOperatorVersion; !slices.Contains(refs, op) {
			t.Errorf("kube-prometheus-stack %s does not run %s: bump kpsOperatorVersion with the chart", kpsChartVersion, op)
		}
	}

	var unique []string
	for _, refs := range images {
		unique = mergeSorted(unique, refs)
	}
	t.Logf("checking %d images from %d sources", len(unique), len(images))
	platforms := inspectAll(ctx, unique, 8)

	sources := make([]string, 0, len(images))
	for s := range images {
		sources = append(sources, s)
	}
	sort.Strings(sources)
	for _, source := range sources {
		for _, ref := range images[source] {
			p := platforms[ref]
			if p.err != nil {
				t.Errorf("%s: %s: %v", source, ref, p.err)
				continue
			}
			if missing := missingArches(p.arches, want[source]); len(missing) > 0 {
				t.Errorf("%s: %s has no %s build (has %s)", source, ref, strings.Join(missing, "/"), strings.Join(p.arches, ", "))
			}
		}
	}
}

type imagePlatforms struct {
	arches []string
	err    error
}

// inspectAll looks up the platforms of every image, workers at a time.
func inspectAll(ctx context.Context, refs []string, workers int) map[string]imagePlatforms {
	results := make(map[string]imagePlatforms, len(refs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for _, ref := range refs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ictx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			out, err := inspectImage(ictx, ref)
			var p imagePlatforms
			if err != nil {
				p.err = fmt.Errorf("cannot inspect: %w", err)
			} else {
				p.arches, p.err = manifestArches([]byte(out))
			}
			mu.Lock()
			results[ref] = p
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}

// inspectImage runs `docker manifest inspect -v`, retrying when the registry
// rate-limits (ECR Public and Docker Hub do, under the audit's parallelism).
func inspectImage(ctx context.Context, ref string) (string, error) {
	backoff := 5 * time.Second
	for {
		out, err := runner.New(nil).Capture(ctx, "docker", "manifest", "inspect", "-v", ref)
		if err == nil || !isRateLimited(err.Error()) || ctx.Err() != nil {
			return out, err
		}
		select {
		case <-ctx.Done():
			return out, err
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

func isRateLimited(msg string) bool {
	return strings.Contains(msg, "toomanyrequests") || strings.Contains(msg, "429") || strings.Contains(msg, "Rate exceeded")
}

// manifestArches returns the linux architectures (arm variants as
// "arm/v7") in `docker manifest inspect -v` output: a list for an image index,
// a single object for a single-platform image. Attestation entries
// ("unknown") are skipped.
func manifestArches(data []byte) ([]string, error) {
	type entry struct {
		Descriptor struct {
			Platform *struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		}
	}
	var list []entry
	if err := json.Unmarshal(data, &list); err != nil {
		var single entry
		if err := json.Unmarshal(data, &single); err != nil {
			return nil, fmt.Errorf("unexpected manifest output: %w", err)
		}
		list = []entry{single}
	}
	var arches []string
	for _, e := range list {
		p := e.Descriptor.Platform
		if p == nil || p.OS != "linux" {
			continue
		}
		a := p.Architecture
		if p.Variant != "" && a == "arm" {
			a += "/" + p.Variant
		}
		if !slices.Contains(arches, a) {
			arches = append(arches, a)
		}
	}
	slices.Sort(arches)
	return arches, nil
}

// missingArches returns the wanted architectures that have is lacking.
func missingArches(have, wanted []string) []string {
	var missing []string
	for _, w := range wanted {
		if !slices.Contains(have, w) {
			missing = append(missing, w)
		}
	}
	return missing
}

var (
	// imageField matches "image: x", "- image: x", "imageName: x" (cnpg) and
	// "*Image: x", but not imagePullPolicy and friends.
	imageField = regexp.MustCompile(`(?m)^\s*-?\s*(?i:[a-z]*image(?:name)?):\s*["']?([^"'\s]+)["']?\s*$`)
	// imageFlag matches an image passed to an operator as a flag, e.g.
	// --prometheus-config-reloader=quay.io/...:v0.95.1. Only references
	// with a registry host count, so ordinary flag values do not.
	imageFlag = regexp.MustCompile(`--[a-z0-9-]+=([a-z0-9-]+(?:\.[a-z0-9-]+)+(?::\d+)?/[a-z0-9._/-]+(?::[A-Za-z0-9._-]+|@sha256:[a-f0-9]{64}))`)
	// imageRef is what a value must look like to be an image reference.
	imageRef = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(?::[A-Za-z0-9._-]+)?(?:@sha256:[a-f0-9]{64})?$`)
)

// imageRefs returns the distinct image references in rendered YAML, sorted.
func imageRefs(yaml string) []string {
	var refs []string
	for _, m := range imageField.FindAllStringSubmatch(yaml, -1) {
		if v := m[1]; imageRef.MatchString(v) && strings.ContainsAny(v, "/:") {
			refs = append(refs, v)
		}
	}
	for _, m := range imageFlag.FindAllStringSubmatch(yaml, -1) {
		refs = append(refs, m[1])
	}
	return mergeSorted(nil, refs)
}

// mergeSorted returns the sorted union of a and b without duplicates.
func mergeSorted(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

// The parsers above run without a network.
func TestImageRefs(t *testing.T) {
	yaml := `
spec:
  containers:
    - name: operator
      image: "quay.io/prometheus-operator/prometheus-operator:v0.95.1"
      imagePullPolicy: IfNotPresent
      args:
        - --prometheus-config-reloader=quay.io/prometheus-operator/prometheus-config-reloader:v0.95.1
        - --web.listen-address=:10250
        - --log-level=info
    - image: busybox:1.37
  imageName: ghcr.io/cloudnative-pg/postgresql:18.1
  image: '{{ .Values.image }}'
  image:
    repository: nope
data:
  helperPod.yaml: |-
    spec:
      containers:
      - name: helper-pod
        image: busybox@sha256:` + strings.Repeat("a", 64) + `
`
	got := imageRefs(yaml)
	want := []string{
		"busybox:1.37",
		"busybox@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/cloudnative-pg/postgresql:18.1",
		"quay.io/prometheus-operator/prometheus-config-reloader:v0.95.1",
		"quay.io/prometheus-operator/prometheus-operator:v0.95.1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("imageRefs =\n  %v\nwant\n  %v", got, want)
	}
}

func TestManifestArches(t *testing.T) {
	// The broken v0.95.1 index: every image labelled amd64, plus attestations.
	broken := `[{"Descriptor":{"platform":{"architecture":"amd64","os":"linux"}}},
	 {"Descriptor":{"platform":{"architecture":"unknown","os":"unknown"}}},
	 {"Descriptor":{"platform":{"architecture":"amd64","os":"linux"}}}]`
	got, err := manifestArches([]byte(broken))
	if err != nil || !slices.Equal(got, []string{"amd64"}) {
		t.Errorf("broken index = %v, %v", got, err)
	}
	if m := missingArches(got, defaultArches); !slices.Equal(m, []string{"arm64"}) {
		t.Errorf("missing = %v, want [arm64]", m)
	}

	multi := `[{"Descriptor":{"platform":{"architecture":"arm64","os":"linux","variant":"v8"}}},
	 {"Descriptor":{"platform":{"architecture":"arm","os":"linux","variant":"v7"}}},
	 {"Descriptor":{"platform":{"architecture":"amd64","os":"linux"}}},
	 {"Descriptor":{"platform":{"architecture":"amd64","os":"windows"}}}]`
	got, err = manifestArches([]byte(multi))
	if err != nil || !slices.Equal(got, []string{"amd64", "arm/v7", "arm64"}) {
		t.Errorf("multi-arch index = %v, %v", got, err)
	}

	single := `{"Descriptor":{"platform":{"architecture":"arm64","os":"linux"}}}`
	got, err = manifestArches([]byte(single))
	if err != nil || !slices.Equal(got, []string{"arm64"}) {
		t.Errorf("single image = %v, %v", got, err)
	}
}

func TestUnsupportedArches(t *testing.T) {
	missing, some := unsupportedArches([]string{"arm64", "arm64", "amd64"}, defaultArches)
	if len(missing) != 0 || !some {
		t.Errorf("supported cluster: %v, %v", missing, some)
	}
	missing, some = unsupportedArches([]string{"s390x", "arm64", "s390x"}, defaultArches)
	if !slices.Equal(missing, []string{"s390x"}) || !some {
		t.Errorf("mixed cluster: %v, %v", missing, some)
	}
	missing, some = unsupportedArches([]string{"arm64"}, []string{"amd64"})
	if !slices.Equal(missing, []string{"arm64"}) || some {
		t.Errorf("unsupported cluster: %v, %v", missing, some)
	}
}
