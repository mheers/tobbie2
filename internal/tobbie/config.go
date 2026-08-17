package tobbie

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// configPath returns the per-user config file (~/.config/tobbie2/config.json).
func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "tobbie2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Config holds persisted CLI state.
type Config struct {
	// MAC is the last robot address that connected successfully.
	MAC string `json:"mac,omitempty"`
}

// LoadConfig reads the persisted config; a missing file yields an
// empty config.
func LoadConfig() (Config, error) {
	var c Config
	path, err := configPath()
	if err != nil {
		return c, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}

// Save persists the config.
func (c Config) Save() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
