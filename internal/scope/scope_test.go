package scope

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
)

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
}

func testConfig(root string, fallback string) *config.Config {
	c := config.Default()
	c.Contexts = map[string]config.Context{
		"personal": {Paths: []string{filepath.Join(root, "projects")}},
		"client":   {Paths: []string{filepath.Join(root, "projects", "client")}},
	}
	c.FallbackContext = fallback
	return &c
}

func TestResolve(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	mkdirs(t, root, "projects/x", "projects2/y", "projects/client/z", "elsewhere")
	require.NoError(t, os.Symlink(filepath.Join(root, "projects", "x"), filepath.Join(root, "elsewhere", "link")))

	tests := []struct {
		name     string
		fallback string
		dirs     []string
		want     string
		readOnly bool
		wantErr  error
	}{
		{name: "inside personal", dirs: []string{filepath.Join(root, "projects/x")}, want: "personal"},
		{name: "context root itself", dirs: []string{filepath.Join(root, "projects")}, want: "personal"},
		{name: "prefix without boundary", fallback: "personal", dirs: []string{filepath.Join(root, "projects2/y")}, want: "personal", readOnly: true},
		{name: "nested more specific wins", dirs: []string{filepath.Join(root, "projects/client/z")}, want: "client"},
		{name: "unmatched no fallback", dirs: []string{filepath.Join(root, "elsewhere")}, wantErr: ErrNoContext},
		{name: "symlink into context", dirs: []string{filepath.Join(root, "elsewhere/link")}, want: "personal"},
		{name: "roots before cwd", dirs: []string{filepath.Join(root, "projects/client/z"), filepath.Join(root, "projects/x")}, want: "client"},
		{name: "first unmatched then matched", dirs: []string{filepath.Join(root, "elsewhere"), filepath.Join(root, "projects/x")}, want: "personal"},
		{name: "no dirs", dirs: nil, wantErr: ErrNoContext},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res, err := Resolve(testConfig(root, tt.fallback), tt.dirs)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, res.Context)
			assert.Equal(t, tt.readOnly, res.ReadOnly)
		})
	}
}

func TestResolve_ProjectFromFirstDir(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	mkdirs(t, root, "projects/x")
	res, err := Resolve(testConfig(root, ""), []string{filepath.Join(root, "projects/x")})
	require.NoError(t, err)
	require.NotNil(t, res.Project)
	assert.Equal(t, filepath.Join(root, "projects/x"), *res.Project)
}
