package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultPath returns the platform config path.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating config dir: %w", err)
	}
	return filepath.Join(dir, "memorable", "config.toml"), nil
}

// Load reads the config at path (defaults when missing), applies env overrides, normalizes paths, and validates.
func Load(path string, getenv func(string) string) (*Config, error) {
	c := Default()
	if _, err := toml.DecodeFile(path, &c); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	if c.Contexts == nil {
		c.Contexts = map[string]Context{}
	}
	if err := applyEnv(&c, getenv); err != nil {
		return nil, err
	}
	if err := normalizePaths(&c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func applyEnv(c *Config, getenv func(string) string) error {
	if v := getenv("MEMORABLE_DB_HOST"); v != "" {
		c.Database.Host = v
	}
	if v := getenv("MEMORABLE_DB_NAME"); v != "" {
		c.Database.Name = v
	}
	if v := getenv("MEMORABLE_DB_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("MEMORABLE_DB_PORT %q is not a number: %w", v, err)
		}
		c.Database.Port = port
	}
	return nil
}

func normalizePaths(c *Config) error {
	for name, ctx := range c.Contexts {
		paths := make([]string, 0, len(ctx.Paths))
		for _, p := range ctx.Paths {
			expanded, err := ExpandPath(p)
			if err != nil {
				return err
			}
			paths = append(paths, expanded)
		}
		c.Contexts[name] = Context{Paths: paths}
	}
	return nil
}

// ExpandPath expands a leading ~ and cleans the path; relative paths stay relative.
func ExpandPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding %q: %w", p, err)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Clean(p), nil
}
