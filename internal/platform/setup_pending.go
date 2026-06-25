package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SetupPending holds first-run options written by megadbsync-setup.
type SetupPending struct {
	AutoStartEngine bool `json:"auto_start_engine"`
}

func SetupPendingPath(dataDir string) string {
	return filepath.Join(dataDir, ".setup-pending.json")
}

func WriteSetupPending(dataDir string, p SetupPending) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(SetupPendingPath(dataDir), b, 0o644)
}

func ReadSetupPending(dataDir string) (SetupPending, bool) {
	b, err := os.ReadFile(SetupPendingPath(dataDir))
	if err != nil {
		return SetupPending{}, false
	}
	var p SetupPending
	if json.Unmarshal(b, &p) != nil {
		return SetupPending{}, false
	}
	return p, true
}

func RemoveSetupPending(dataDir string) {
	_ = os.Remove(SetupPendingPath(dataDir))
}
