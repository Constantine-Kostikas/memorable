package scope

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitInit(t *testing.T, remote string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q")
	if remote != "" {
		run("remote", "add", "origin", remote)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	return dir
}

func TestProjectID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		remote string
		want   string
	}{
		{name: "https", remote: "https://GitHub.com/A/B.git", want: "github.com/A/B"},
		{name: "scp", remote: "git@github.com:A/B.git", want: "github.com/A/B"},
		{name: "ssh url with user and port", remote: "ssh://git@gitlab.example.com:2222/team/repo", want: "gitlab.example.com/team/repo"},
		{name: "https with credentials", remote: "https://user:token@github.com/A/B", want: "github.com/A/B"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := gitInit(t, tt.remote)
			got, err := ProjectID(filepath.Join(dir, "sub"))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProjectID_NoRemoteUsesRepoRoot(t *testing.T) {
	t.Parallel()
	dir := gitInit(t, "")
	got, err := ProjectID(filepath.Join(dir, "sub"))
	require.NoError(t, err)
	assert.Equal(t, dir, got)
}

func TestProjectID_PlainDir(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	got, err := ProjectID(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, got)
}
