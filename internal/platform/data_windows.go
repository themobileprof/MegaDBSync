//go:build windows

package platform

import (
	"os"
	"path/filepath"
)

// DefaultDataDir returns the preferred state directory on Windows.
// Order: MEGADBSYNC_DATA → MDAS_DATA (legacy) → %ProgramData%\MegaDBSync
// (falls back to %ProgramData%\MDAS if that folder already exists) →
// %LOCALAPPDATA%\MegaDBSync → .\data next to exe.
func DefaultDataDir() string {
	if d := os.Getenv("MEGADBSYNC_DATA"); d != "" {
		return d
	}
	if d := os.Getenv("MDAS_DATA"); d != "" {
		return d
	}
	if d := os.Getenv("ProgramData"); d != "" {
		next := filepath.Join(d, "MegaDBSync")
		legacy := filepath.Join(d, "MDAS")
		if _, err := os.Stat(next); err != nil {
			if _, err := os.Stat(legacy); err == nil {
				return legacy
			}
		}
		return next
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
