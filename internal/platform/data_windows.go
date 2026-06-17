//go:build windows

package platform

import (
	"os"
	"path/filepath"
)

// DefaultDataDir returns the preferred state directory on Windows.
// Order: MDAS_DATA env → %ProgramData%\MDAS → %LOCALAPPDATA%\MDAS → .\data next to exe.
func DefaultDataDir() string {
	if d := os.Getenv("MDAS_DATA"); d != "" {
		return d
	}
	if d := os.Getenv("ProgramData"); d != "" {
		return filepath.Join(d, "MDAS")
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "MDAS")
	}
	exe, err := os.Executable()
	if err != nil {
		return "data"
	}
	return filepath.Join(filepath.Dir(exe), "data")
}
