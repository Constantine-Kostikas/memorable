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
		"Shell completion",
		"would ask: Also show relevant memories on every prompt",
		"unrecognized shell",
		"dry run: nothing was changed",
	} {
		assert.Contains(t, out, want)
	}
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "dry run must not create files")
}

func TestSetup_DryRunShellCompletion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		shell  string
		goos   string
		want   func(home string) []string
		absent []string
	}{
		{
			name: "zsh on linux", shell: "/bin/zsh", goos: "linux",
			want: func(home string) []string {
				return []string{filepath.Join(home, ".zshrc"), filepath.Join(home, ".local/share/memorable/completion.zsh"), "compinit"}
			},
		},
		{
			name: "bash on macOS", shell: "/bin/bash", goos: "darwin",
			want: func(home string) []string {
				return []string{filepath.Join(home, ".bash_profile"), "_get_comp_words_by_ref"}
			},
		},
		{
			name: "fish on linux", shell: "/usr/bin/fish", goos: "linux",
			want: func(home string) []string {
				return []string{filepath.Join(home, ".config/fish/completions/memorable.fish")}
			},
			absent: []string{".bashrc", ".zshrc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			d := setupDeps(t, home)
			base := d.getenv
			d.getenv = func(k string) string {
				if k == "SHELL" {
					return tt.shell
				}
				return base(k)
			}
			d.goos = tt.goos
			cfgPath := filepath.Join(home, ".config", "memorable", "config.toml")
			out, err := runRoot(t, d, "", "--config", cfgPath, "setup", "--dry-run")
			require.NoError(t, err, out)
			for _, want := range tt.want(home) {
				assert.Contains(t, out, want)
			}
			for _, absent := range tt.absent {
				assert.NotContains(t, out, absent)
			}
			entries, err := os.ReadDir(home)
			require.NoError(t, err)
			assert.Empty(t, entries, "dry run must not create files")
		})
	}
}

func TestWriteAtomic_KeepsContentAndMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "a", "b", "file")
	require.NoError(t, writeAtomic(path, "hello\n", 0o600))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(got))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the temp file must be renamed away")
}

func TestSetup_NoTTY(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	_, err := runRoot(t, setupDeps(t, home), "", "setup")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal")
}
