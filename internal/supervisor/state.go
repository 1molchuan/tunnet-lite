package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// state persists the choices that should stay stable across restarts. The
// reference client caches its root domain the same way, so a client keeps
// hitting the same <slug>.<root> instead of rotating on every launch.
type state struct {
	RootDomain string `json:"root_domain"`
}

func loadState(path string) state {
	var s state
	data, err := os.ReadFile(path)
	if err != nil {
		return s // absent or unreadable state is not an error; we just re-pick
	}
	_ = json.Unmarshal(data, &s)
	return s
}

func saveState(path string, s state) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
