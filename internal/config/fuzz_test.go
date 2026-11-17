package config

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzLoad(f *testing.F) {
	f.Add("[database]\nport = 5434\n[contexts.personal]\npaths = [\"~/p\"]\n")
	f.Add("[embedding]\ndimensions = -1\n")
	f.Add("[[[")
	f.Add("")
	f.Fuzz(func(t *testing.T, content string) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path, func(string) string { return "" })
		if err != nil {
			return
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("Load returned a config that fails Validate: %v", err)
		}
	})
}
