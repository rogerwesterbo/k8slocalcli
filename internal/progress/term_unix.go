//go:build !windows

package progress

import (
	"os"

	"golang.org/x/sys/unix"
)

// Terminal reports whether f is an interactive terminal and its width. It
// asks for the window size rather than checking for a character device,
// because /dev/null is one too.
func Terminal(f *os.File) (bool, int) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ) // #nosec G115 -- fd fits in int
	if err != nil || ws.Col == 0 {
		return false, 0
	}
	return true, int(ws.Col)
}
