package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChangePassword_RejectsRoleBeforePrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing role", args: []string{"change-password"}, want: "--role admin or --role app"},
		{name: "unknown role", args: []string{"change-password", "--role", "root"}, want: "unknown role"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := fakeDeps(t)
			prompted := false
			d.adminPassword = func(*cobra.Command) (string, error) {
				prompted = true
				return "", nil
			}
			_, err := runRoot(t, d, "", tt.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, 2, exitCode(err))
			assert.False(t, prompted, "no password prompt for an invalid role")
		})
	}
}

func TestValidateNewAdminPassword(t *testing.T) {
	t.Parallel()
	const current = "current-admin-password"
	tests := []struct {
		name string
		pw   string
		want string
	}{
		{name: "too short", pw: "short", want: "at least 6"},
		{name: "multibyte counted as characters", pw: "αβγδε", want: "at least 6"},
		{name: "six multibyte characters", pw: "αβγδεζ"},
		{name: "same as current", pw: current, want: "matches the current"},
		{name: "valid", pw: "a-brand-new-admin-password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateNewAdminPassword(tt.pw, current)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestPromptNewAdminPassword_NoTTY(t *testing.T) {
	t.Parallel()
	_, err := promptNewAdminPassword(strings.NewReader("new\nnew\n"), &bytes.Buffer{}, false, "current")
	require.ErrorIs(t, err, errNeedsTerminal)
}
