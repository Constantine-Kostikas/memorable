package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noEnv(string) string { return "" }

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	t.Parallel()
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"), noEnv)
	require.NoError(t, err)
	d := Default()
	assert.Equal(t, d, *c)
	assert.Equal(t, 5434, c.Database.Port)
	assert.Equal(t, "embeddinggemma", c.Embedding.Model)
	assert.Equal(t, 768, c.Embedding.Dimensions)
	assert.InDelta(t, 0.35, c.Search.MinSimilarity, 1e-9)
	assert.Equal(t, 30, c.Search.HalfLifeDays)
	assert.Equal(t, 14, c.Retention.BackupsKeep)
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Parallel()
	p := writeFile(t, "[database]\nport = 7000\nname = \"x\"\n")
	env := map[string]string{"MEMORABLE_DB_PORT": "6000", "MEMORABLE_DB_HOST": "localhost"}
	c, err := Load(p, func(k string) string { return env[k] })
	require.NoError(t, err)
	assert.Equal(t, 6000, c.Database.Port)
	assert.Equal(t, "localhost", c.Database.Host)
	assert.Equal(t, "x", c.Database.Name)
}

func TestLoad_BadEnvPort(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join(t.TempDir(), "x.toml"), func(k string) string {
		if k == "MEMORABLE_DB_PORT" {
			return "abc"
		}
		return ""
	})
	require.Error(t, err)
}

func TestLoad_ExpandsHome(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	p := writeFile(t, "[contexts.personal]\npaths = [\"~/development/projects\", \"/tmp/../tmp/x/\"]\n")
	c, err := Load(p, noEnv)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(home, "development/projects"), "/tmp/x"}, c.Contexts["personal"].Paths)
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{name: "defaults", mutate: func(*Config) {}},
		{name: "uppercase context", mutate: func(c *Config) { c.Contexts = map[string]Context{"Work": {}} }, wantErr: true},
		{name: "reserved profile", mutate: func(c *Config) { c.Contexts = map[string]Context{"profile": {}} }, wantErr: true},
		{name: "fallback undefined", mutate: func(c *Config) { c.FallbackContext = "work" }, wantErr: true},
		{name: "fallback defined", mutate: func(c *Config) {
			c.Contexts = map[string]Context{"work": {Paths: []string{"/w"}}}
			c.FallbackContext = "work"
		}},
		{name: "remote url", mutate: func(c *Config) { c.Embedding.URL = "http://10.0.0.5:11434" }, wantErr: true},
		{name: "remote allowed", mutate: func(c *Config) {
			c.Embedding.URL = "http://10.0.0.5:11434"
			c.Embedding.AllowRemote = true
		}},
		{name: "localhost url", mutate: func(c *Config) { c.Embedding.URL = "http://localhost:11434" }},
		{name: "zero dims", mutate: func(c *Config) { c.Embedding.Dimensions = 0 }, wantErr: true},
		{name: "conflict above duplicate", mutate: func(c *Config) { c.Search.ConflictThreshold = 0.96 }, wantErr: true},
		{name: "similarity out of range", mutate: func(c *Config) { c.Search.MinSimilarity = 1.5 }, wantErr: true},
		{name: "bad port", mutate: func(c *Config) { c.Database.Port = 70000 }, wantErr: true},
		{name: "relative context path", mutate: func(c *Config) { c.Contexts = map[string]Context{"work": {Paths: []string{"rel"}}} }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := Default()
			tt.mutate(&c)
			err := c.Validate()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSave_RoundTripAndPermissions(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	c := Default()
	c.Contexts = map[string]Context{"personal": {Paths: []string{"/home/x/projects"}}}
	c.FallbackContext = "personal"
	require.NoError(t, Save(p, c))

	info, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dir, err := os.Stat(filepath.Dir(p))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm())

	got, err := Load(p, noEnv)
	require.NoError(t, err)
	assert.Equal(t, c, *got)
}

func TestSchemaName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "mem_personal", SchemaName("personal"))
}
