// Package settings resolves the user-scoped paths k8slocalcli stores state
// under. Today this is just the config directory (~/.k8slocalcli), shared by
// the Talos provider's cluster state and the plugin manager's metadata.
package settings

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDirName is the per-user directory under $HOME that holds all
// k8slocalcli state (cluster state, plugin metadata, ...).
const ConfigDirName = ".k8slocalcli"

// ConfigDir returns ~/.k8slocalcli. The directory is not created here;
// callers that write into it create the specific subdirectory they need.
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ConfigDirName), nil
}
