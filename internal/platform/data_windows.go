//go:build windows

package platform

import (
	"os"
	"path/filepath"
)

// DefaultDataDir returns the preferred state directory on Windows.
// Order: MEGADBSYNC_DATA → %ProgramData%\MegaDBSync →
// %LOCALAPPDATA%\MegaDBSync → .\data next to exe.
func DefaultDataDir() string {
	if d := os.Getenv("MEGADBSYNC_DATA"); d != "" {
		return d
	}
	if d := os.Getenv("ProgramData"); d != "" {
		return filepath.Join(d, "MegaDBSync")
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "MegaDBSync")
	}
	exe, err := os.Executable()
	if err != nil {
		return "data"
	}
	return filepath.Join(filepath.Dir(exe), "data")
}
