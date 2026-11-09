package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rogerwesterbo/k8slocalcli/internal/pluginmgr"
	"github.com/rogerwesterbo/k8slocalcli/internal/progress"
	"github.com/rogerwesterbo/k8slocalcli/internal/release"
	"github.com/rogerwesterbo/k8slocalcli/internal/settings"
)

const (
	selfRepo = "rogerwesterbo/k8slocalcli"
	// noUpdateCheckEnv turns the passive "new version available" notice off.
	noUpdateCheckEnv = "K8SLOCALCLI_NO_UPDATE_CHECK"
	// releaseURLEnv points upgrades at a mirror (or a local server when
	// testing); "{version}" is replaced with the tag.
	releaseURLEnv = "K8SLOCALCLI_RELEASE_URL"
	// updateCheckInterval bounds how often GitHub is asked about new releases.
	updateCheckInterval = 24 * time.Hour
)

// selfEntry describes k8slocalcli's own release assets, which install.sh and
// .github/workflows/release.yml also agree on.
func selfEntry() *pluginmgr.Entry {
	return &pluginmgr.Entry{
		Name:             "k8slocalcli",
		Repo:             selfRepo,
		ArchiveAsset:     "k8slocalcli-{version}-{os}-{arch}.tar.gz",
		ArchiveInnerPath: "k8slocalcli-{version}-{os}-{arch}/k8slocalcli",
		ChecksumsAsset:   "k8slocalcli-{version}-SHA256SUMS",
		DownloadBase:     os.Getenv(releaseURLEnv),
	}
}

func newUpgradeCmd() *cobra.Command {
	var (
		check      bool
		toVersion  string
		force      bool
		skipCosign bool
	)
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade k8slocalcli to the latest release",
		Long: "Download the latest k8slocalcli release, verify it (SHA-256, and the\n" +
			"Sigstore signature when cosign is installed) and replace the running\n" +
			"binary in place.\n\n" +
			"k8slocalcli also mentions new releases after other commands, at most\n" +
			"once a day; set " + noUpdateCheckEnv + "=1 to turn that off.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			target := toVersion
			if target == "" {
				latest, err := release.FetchLatest(ctx, selfRepo)
				if err != nil {
					return fmt.Errorf("checking the latest release: %w", err)
				}
				target = latest.Tag
				saveUpdateCache(target)
			}

			status := release.Compare(version, target)
			if check {
				fmt.Fprintln(out, describeStatus(status, version, target))
				return nil
			}
			if toVersion == "" && !force {
				switch status {
				case release.StatusUpToDate, release.StatusAhead:
					fmt.Fprintln(out, describeStatus(status, version, target))
					return nil
				case release.StatusDevelopment:
					return fmt.Errorf("this is a development build (%s); pass --force to replace it with %s", version, target)
				}
			}

			exe, err := runningBinary()
			if err != nil {
				return err
			}
			if _, err := pluginmgr.Install(ctx, selfEntry(), pluginmgr.InstallOptions{
				Version:    target,
				Prefix:     filepath.Dir(exe),
				BinaryName: filepath.Base(exe),
				SkipCosign: skipCosign,
				Stderr:     cmd.ErrOrStderr(),
			}); err != nil {
				return err
			}
			fmt.Fprintf(out, "✅ k8slocalcli %s → %s (%s)\n", version, target, exe)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().StringVar(&toVersion, "version", "", "install this release tag instead of the latest (also allows downgrades)")
	cmd.Flags().BoolVar(&force, "force", false, "replace a development build, or reinstall the same version")
	cmd.Flags().BoolVar(&skipCosign, "skip-cosign", false, "skip Sigstore signature verification (SHA-256 is still checked)")
	return cmd
}

func describeStatus(s release.Status, local, latest string) string {
	switch s {
	case release.StatusUpToDate:
		return fmt.Sprintf("k8slocalcli %s is up to date", local)
	case release.StatusOutdated:
		return fmt.Sprintf("k8slocalcli %s is available (you have %s) — run: k8slocalcli upgrade", latest, local)
	case release.StatusAhead:
		return fmt.Sprintf("k8slocalcli %s is newer than the latest release %s", local, latest)
	default:
		return fmt.Sprintf("k8slocalcli %s is a development build; the latest release is %s", local, latest)
	}
}

// runningBinary returns the path of this executable, refusing locations an
// in-place upgrade would be wrong for.
func runningBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating the k8slocalcli binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return "", errors.New("running via `go run`; build or install k8slocalcli before upgrading it")
	}
	if strings.Contains(exe, string(filepath.Separator)+"Cellar"+string(filepath.Separator)) {
		return "", errors.New("k8slocalcli is managed by Homebrew; upgrade it with brew")
	}
	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".k8slocalcli-upgrade-*")
	if err != nil {
		return "", fmt.Errorf("%s is not writable; reinstall with install.sh --prefix <dir you own>, or rerun with sufficient permissions", dir)
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return exe, nil
}

// --- passive "new version available" notice ---

type updateCache struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    string    `json:"latest"`
}

func updateCachePath() (string, error) {
	dir, err := settings.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "update-check.json"), nil
}

func loadUpdateCache() (updateCache, bool) {
	var c updateCache
	path, err := updateCachePath()
	if err != nil {
		return c, false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- fixed path under ~/.k8slocalcli
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, false
	}
	return c, true
}

func saveUpdateCache(latest string) {
	path, err := updateCachePath()
	if err != nil {
		return
	}
	data, err := json.Marshal(updateCache{CheckedAt: time.Now(), Latest: latest})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

var releaseVersionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// shouldCheckForUpdates keeps the notice out of the way: release builds only,
// interactive terminals only, never for commands where it is noise.
func shouldCheckForUpdates(args []string) bool {
	if os.Getenv(noUpdateCheckEnv) != "" || !releaseVersionRe.MatchString(version) {
		return false
	}
	if live, _ := progress.Terminal(os.Stderr); !live {
		return false
	}
	for _, a := range args {
		switch a {
		case "upgrade", "completion", "__complete", "--version", "-h", "--help", "help":
			return false
		}
	}
	return true
}

// startUpdateCheck looks for a newer release in the background and returns a
// function that yields the notice to print, or "". GitHub is asked at most
// once per updateCheckInterval; otherwise the cached answer is used.
func startUpdateCheck(ctx context.Context, args []string) func() string {
	if !shouldCheckForUpdates(args) {
		return func() string { return "" }
	}
	notice := func(latest string) string {
		if release.Compare(version, latest) != release.StatusOutdated {
			return ""
		}
		return "💡 " + describeStatus(release.StatusOutdated, version, latest)
	}
	if c, ok := loadUpdateCache(); ok && time.Since(c.CheckedAt) < updateCheckInterval {
		s := notice(c.Latest)
		return func() string { return s }
	}

	result := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		latest, err := release.FetchLatest(ctx, selfRepo)
		if err != nil {
			result <- ""
			return
		}
		saveUpdateCache(latest.Tag)
		result <- notice(latest.Tag)
	}()
	return func() string {
		// Never hold the user up for long; a missed check runs next time.
		select {
		case s := <-result:
			return s
		case <-time.After(1500 * time.Millisecond):
			return ""
		}
	}
}

func printUpdateNotice(w io.Writer, notice string) {
	if notice != "" {
		fmt.Fprintf(w, "\n%s\n", notice)
	}
}
