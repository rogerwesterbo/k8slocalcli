package cli

import (
	"strings"
	"testing"

	"github.com/rogerwesterbo/k8slocalcli/internal/release"
)

// The self entry must match the asset names release.yml publishes and
// install.sh downloads.
func TestSelfEntryAssetNames(t *testing.T) {
	e := selfEntry()
	if got := e.ArchiveName("v1.2.3", "darwin", "arm64"); got != "k8slocalcli-v1.2.3-darwin-arm64.tar.gz" {
		t.Errorf("archive = %s", got)
	}
	if got := e.InnerBinaryPath("v1.2.3", "linux", "amd64"); got != "k8slocalcli-v1.2.3-linux-amd64/k8slocalcli" {
		t.Errorf("inner path = %s", got)
	}
	if got := e.ChecksumsName("v1.2.3"); got != "k8slocalcli-v1.2.3-SHA256SUMS" {
		t.Errorf("checksums = %s", got)
	}
	if !strings.Contains(e.CosignIdentity(), "rogerwesterbo/k8slocalcli/.github/workflows/release.yml") {
		t.Errorf("cosign identity = %s", e.CosignIdentity())
	}
}

func TestUpdateCheckSkipsDevBuildsAndOptOut(t *testing.T) {
	old := version
	defer func() { version = old }()

	version = "dev"
	if shouldCheckForUpdates([]string{"list"}) {
		t.Error("dev builds must not check for updates")
	}
	version = "v0.1.0-3-gabc1234"
	if shouldCheckForUpdates([]string{"list"}) {
		t.Error("git-describe builds must not check for updates")
	}
	version = "v0.1.0"
	t.Setenv(noUpdateCheckEnv, "1")
	if shouldCheckForUpdates([]string{"list"}) {
		t.Error("the opt-out env var must disable the check")
	}
}

func TestDescribeStatus(t *testing.T) {
	if got := describeStatus(release.StatusOutdated, "v0.1.0", "v0.2.0"); !strings.Contains(got, "k8slocalcli upgrade") {
		t.Errorf("outdated message should say how to upgrade: %s", got)
	}
}
