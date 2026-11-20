package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/update"
)

func updateDeps(res update.Result, err error, got *update.Options) deps {
	d := defaultDeps()
	d.selfUpdate = func(_ *cobra.Command, _ string, opts update.Options, _ update.Reporter) (update.Result, error) {
		if got != nil {
			*got = opts
		}
		return res, err
	}
	return d
}

func TestUpdate_PassesFlags(t *testing.T) {
	t.Parallel()
	var got update.Options
	_, err := runRoot(t, updateDeps(update.Result{Status: update.StatusUpToDate}, nil, &got), "",
		"update", "--check", "--pre", "--force", "--version", "v0.2.0")
	require.NoError(t, err)
	assert.Equal(t, update.Options{Check: true, Pre: true, Force: true, Version: "v0.2.0"}, got)
}

func TestUpdate_Updated(t *testing.T) {
	t.Parallel()
	res := update.Result{Status: update.StatusUpdated, Current: "v0.1.0", Target: "v0.2.0", Path: "/usr/local/bin/memorable"}
	out, err := runRoot(t, updateDeps(res, nil, nil), "", "update")
	require.NoError(t, err)
	for _, want := range []string{"v0.1.0", "v0.2.0", "/usr/local/bin/memorable", "Restart", "Claude Code", "memorable doctor", "memorable migrate"} {
		assert.Contains(t, out, want)
	}
}

func TestUpdate_UpToDate(t *testing.T) {
	t.Parallel()
	res := update.Result{Status: update.StatusUpToDate, Current: "v0.2.0", Path: "/usr/local/bin/memorable"}
	out, err := runRoot(t, updateDeps(res, nil, nil), "", "update")
	require.NoError(t, err)
	assert.Contains(t, out, "v0.2.0 is up to date")
}

func TestUpdate_CheckAvailable(t *testing.T) {
	t.Parallel()
	res := update.Result{Status: update.StatusAvailable, Current: "v0.1.0", Target: "v0.2.0", Path: "/usr/local/bin/memorable"}
	out, err := runRoot(t, updateDeps(res, nil, nil), "", "update", "--check")
	require.NoError(t, err)
	assert.Contains(t, out, "v0.1.0 -> v0.2.0")
}

func TestUpdate_NeedsRootPrintsCommandAndFails(t *testing.T) {
	t.Parallel()
	res := update.Result{
		Status: update.StatusNeedsRoot, Current: "v0.1.0", Target: "v0.2.0", Path: "/usr/local/bin/memorable",
		Command: []string{"sudo", "install", "-m", "0755", "/tmp/memorable update-1/memorable", "/usr/local/bin/memorable"},
	}
	out, err := runRoot(t, updateDeps(res, nil, nil), "", "update")
	require.Error(t, err)
	assert.Equal(t, 1, exitCode(err))
	assert.Contains(t, out, "sudo install -m 0755 '/tmp/memorable update-1/memorable' /usr/local/bin/memorable")
}

func TestUpdate_JSON(t *testing.T) {
	t.Parallel()
	res := update.Result{Status: update.StatusUpdated, Current: "v0.1.0", Target: "v0.2.0", Path: "/usr/local/bin/memorable"}
	out, err := runRoot(t, updateDeps(res, nil, nil), "", "update", "-o", "json")
	require.NoError(t, err)
	var got update.Result
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, res, got)
}

func TestUpdate_ExitCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid version is a usage error", err: update.ErrInvalidVersion, want: 2},
		{name: "dev build", err: update.ErrDevBuild, want: 1},
		{name: "homebrew", err: update.ErrHomebrew, want: 1},
		{name: "verification", err: update.ErrVerification, want: 1},
		{name: "network", err: errors.New("dial tcp: timeout"), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := runRoot(t, updateDeps(update.Result{}, tt.err, nil), "", "update")
			require.ErrorIs(t, err, tt.err)
			assert.Equal(t, tt.want, exitCode(err))
		})
	}
}

func TestUpdate_RejectsArgs(t *testing.T) {
	t.Parallel()
	_, err := runRoot(t, updateDeps(update.Result{}, nil, nil), "", "update", "v0.2.0")
	require.Error(t, err)
	assert.Equal(t, 2, exitCode(err))
}

func TestShellQuote(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "sudo install /a/b", shellQuote([]string{"sudo", "install", "/a/b"}))
	assert.Equal(t, `'/a b' 'it'\''s'`, shellQuote([]string{"/a b", "it's"}))
}
