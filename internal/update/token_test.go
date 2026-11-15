package update

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToken(t *testing.T) {
	t.Parallel()
	ghOK := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "gh" && len(args) == 2 && args[0] == "auth" && args[1] == "token" {
			return []byte("gho_fromcli\n"), nil
		}
		return nil, errors.New("unexpected command")
	}
	ghFails := func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("not logged in")
	}
	tests := []struct {
		name string
		env  map[string]string
		run  func(context.Context, string, ...string) ([]byte, error)
		want string
	}{
		{name: "GH_TOKEN wins", env: map[string]string{"GH_TOKEN": "a", "GITHUB_TOKEN": "b"}, run: ghOK, want: "a"},
		{name: "GITHUB_TOKEN next", env: map[string]string{"GITHUB_TOKEN": "b"}, run: ghOK, want: "b"},
		{name: "gh auth token next", env: map[string]string{}, run: ghOK, want: "gho_fromcli"},
		{name: "gh fails means no token", env: map[string]string{}, run: ghFails, want: ""},
		{name: "gh missing means no token", env: map[string]string{}, run: nil, want: ""},
		{name: "blank env ignored", env: map[string]string{"GH_TOKEN": "  "}, run: ghFails, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(k string) string { return tt.env[k] }
			assert.Equal(t, tt.want, Token(t.Context(), getenv, tt.run))
		})
	}
}
