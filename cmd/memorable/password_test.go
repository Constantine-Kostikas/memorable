package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPassword_RejectsRoleBeforePrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing role", args: []string{"password"}, want: "--role app"},
		{name: "admin role", args: []string{"password", "--role", "admin"}, want: "only the app role"},
		{name: "unknown role", args: []string{"password", "--role", "root"}, want: "unknown role"},
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
