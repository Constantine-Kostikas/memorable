package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
)

func complete(t *testing.T, d deps, args ...string) ([]string, string) {
	t.Helper()
	out, err := runRoot(t, d, "", append([]string{"__complete"}, args...)...)
	require.NoError(t, err)
	var values []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, ":") {
			return values, line
		}
		if line == "" {
			continue
		}
		value, _, _ := strings.Cut(line, "\t")
		values = append(values, value)
	}
	require.Fail(t, "no directive line in completion output", out)
	return nil, ""
}

func completionDeps() deps {
	d := defaultDeps()
	d.getenv = func(string) string { return "" }
	return d
}

func writeContextConfig(t *testing.T, names ...string) string {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), "missing.toml"), func(string) string { return "" })
	require.NoError(t, err)
	cfg.Contexts = map[string]config.Context{}
	for _, n := range names {
		cfg.Contexts[n] = config.Context{Paths: []string{"/srv/" + n}}
	}
	cfg.FallbackContext = ""
	body, err := encodeConfig(*cfg)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestComplete_FixedValues(t *testing.T) {
	t.Parallel()
	types := []string{"user", "feedback", "project", "reference"}
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"output", []string{"list", "--output", ""}, []string{"text", "json"}},
		{"search type", []string{"search", "--type", ""}, types},
		{"list type", []string{"list", "--type", ""}, types},
		{"profile add type", []string{"profile", "add", "--type", ""}, types},
		{"list status", []string{"list", "--status", ""}, []string{"active", "unverified", "flagged", "superseded", "deleted"}},
		{"export format", []string{"export", "--format", ""}, []string{"json", "md"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, directive := complete(t, completionDeps(), tt.args...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, ":4", directive)
		})
	}
}

func TestComplete_NothingForIDsAndFreeText(t *testing.T) {
	t.Parallel()
	tests := [][]string{
		{"get", ""},
		{"edit", ""},
		{"purge", ""},
		{"approve", ""},
		{"profile", "edit", ""},
		{"profile", "delete", ""},
		{"search", ""},
		{"stats", ""},
		{"doctor", ""},
		{"list", "--tag", ""},
		{"list", "--cursor", ""},
		{"search", "-k", ""},
		{"update", "--version", ""},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			got, directive := complete(t, completionDeps(), args...)
			assert.Empty(t, got)
			assert.Equal(t, ":4", directive)
		})
	}
}

func TestComplete_Paths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		args      []string
		want      []string
		directive string
	}{
		{"config", []string{"--config", ""}, []string{"toml"}, ":8"},
		{"backup dir", []string{"backup", "--dir", ""}, nil, ":16"},
		{"import path", []string{"import", ""}, nil, ":0"},
		{"import extra arg", []string{"import", "a.json", ""}, nil, ":4"},
		{"context add path", []string{"context", "add", ""}, nil, ":16"},
		{"context add name", []string{"context", "add", "/tmp", ""}, nil, ":4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, directive := complete(t, completionDeps(), tt.args...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.directive, directive)
		})
	}
}

func TestComplete_ContextNamesFromConfig(t *testing.T) {
	t.Parallel()
	path := writeContextConfig(t, "personal", "work")
	tests := []struct {
		name string
		args []string
	}{
		{"prune", []string{"--config", path, "prune", "--context", ""}},
		{"approve", []string{"--config", path, "approve", "--context", ""}},
		{"purge", []string{"--config", path, "purge", "--context", ""}},
		{"profile list", []string{"--config", path, "profile", "list", "--context", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, directive := complete(t, completionDeps(), tt.args...)
			assert.Equal(t, []string{"personal", "work"}, got)
			assert.Equal(t, ":4", directive)
		})
	}
}

func TestComplete_ContextNamesBrokenConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("not toml ["), 0o600))
	got, directive := complete(t, completionDeps(), "--config", path, "prune", "--context", "")
	assert.Empty(t, got)
	assert.Equal(t, ":4", directive)
}

func TestCompletionValues_ParseAsEnums(t *testing.T) {
	t.Parallel()
	for _, v := range memoryTypeCompletions {
		_, err := memory.ParseType(v)
		require.NoError(t, err, v)
	}
	for _, v := range listStatusCompletions {
		_, err := memory.ParseStatus(v)
		require.NoError(t, err, v)
	}
}

func walkCommands(cmd *cobra.Command, visit func(*cobra.Command)) {
	visit(cmd)
	for _, c := range cmd.Commands() {
		walkCommands(c, visit)
	}
}

func assertFlagCompletes(t *testing.T, cmd *cobra.Command, f *pflag.Flag) {
	t.Helper()
	if f.Value.Type() == "bool" || f.Name == "help" {
		return
	}
	if fn, _ := cmd.GetFlagCompletionFunc(f.Name); fn != nil {
		return
	}
	_, ext := f.Annotations[cobra.BashCompFilenameExt]
	_, dir := f.Annotations[cobra.BashCompSubdirsInDir]
	assert.True(t, ext || dir, "%s --%s has no completion", cmd.CommandPath(), f.Name)
}

func TestApplyCompletionDefaults_CoversEveryCommand(t *testing.T) {
	t.Parallel()
	root := newRootCmdWith(buildInfo{}, defaultDeps())
	root.InitDefaultCompletionCmd()
	walkCommands(root, func(cmd *cobra.Command) {
		if cmd.Name() == "completion" || (cmd.Parent() != nil && cmd.Parent().Name() == "completion") || cmd.Name() == "help" {
			return
		}
		assert.True(t, cmd.ValidArgsFunction != nil || len(cmd.ValidArgs) > 0, "%s has no arg completion", cmd.CommandPath())
		cmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) { assertFlagCompletes(t, cmd, f) })
		cmd.PersistentFlags().VisitAll(func(f *pflag.Flag) { assertFlagCompletes(t, cmd, f) })
	})
}

func TestCompletionScripts_ParseInRealShells(t *testing.T) {
	t.Parallel()
	root := newRootCmdWith(buildInfo{}, defaultDeps())
	tests := []struct {
		shell string
		check []string
	}{
		{"bash", []string{"bash", "-n"}},
		{"zsh", []string{"zsh", "-n"}},
		{"fish", []string{"fish", "--no-execute"}},
	}
	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			t.Parallel()
			if _, err := exec.LookPath(tt.check[0]); err != nil {
				t.Skipf("%s not installed", tt.check[0])
			}
			var sb strings.Builder
			require.NoError(t, generateCompletion(root, tt.shell, &sb))
			path := filepath.Join(t.TempDir(), "script")
			require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o600))
			out, err := exec.Command(tt.check[0], append(tt.check[1:], path)...).CombinedOutput() //nolint:gosec // fixed shell names
			require.NoError(t, err, string(out))
		})
	}
	assert.Error(t, generateCompletion(root, "powershell", &strings.Builder{}))
}
