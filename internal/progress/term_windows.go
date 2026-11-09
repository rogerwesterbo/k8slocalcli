//go:build windows

package progress

import "os"

// Terminal always reports false on Windows for now, which falls back to
// plain, streamed output.
func Terminal(_ *os.File) (bool, int) { return false, 0 }
