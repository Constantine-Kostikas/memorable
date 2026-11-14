package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Save writes the config as TOML with owner-only permissions.
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replacing config: %w", err)
	}
	return nil
}
