package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupDeps(t *testing.T, home string) deps {
	t.Helper()
	d := defaultDeps()
	d.getenv = func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	d.cwd = func() (string, error) { return filepath.Join(home, "projects", "demo"), nil }
	d.docker = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("No such object") }
	d.run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("dry run must not execute commands")
		return nil, nil
	}
	d.lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	d.executable = func() (string, error) { return "/usr/local/bin/memorable", nil }
	d.goos = "linux"
	return d
}

func TestSetup_DryRunAppliesNothing(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cfgPath := filepath.Join(home, ".config", "memorable", "config.toml")
	out, err := runRoot(t, setupDeps(t, home), "", "--config", cfgPath, "setup", "--dry-run")
	require.NoError(t, err, out)

	for _, want := range []string{
		"docker run -d --name memorable-postgres",
		"POSTGRES_PASSWORD=<generated>",
		"claude mcp add --scope user memorable -- /usr/local/bin/memorable serve",
		filepath.Join(home, ".claude", "CLAUDE.md"),
		filepath.Join(home, ".claude", "settings.json"),
		"mcp__memorable__memory_search",
		"memorable-backup.timer",
		"[contexts.personal]",
		"dry run: nothing was changed",
	} {
		assert.Contains(t, out, want)
	}
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "dry run must not create files")
}

func TestSetup_NoTTY(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	_, err := runRoot(t, setupDeps(t, home), "", "setup")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal")
}
