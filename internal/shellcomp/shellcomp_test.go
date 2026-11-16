package shellcomp_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/shellcomp"
)

const (
	startMarker = "# >>> memorable completion >>>"
	endMarker   = "# <<< memorable completion <<<"
)

func env(goos string, vars map[string]string) shellcomp.Env {
	return shellcomp.Env{GOOS: goos, Home: "/home/u", Getenv: func(k string) string { return vars[k] }}
}

func TestDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		shell string
		want  shellcomp.Shell
		ok    bool
	}{
		{"zsh", "/bin/zsh", shellcomp.Zsh, true},
		{"fish", "/opt/homebrew/bin/fish", shellcomp.Fish, true},
		{"bash", "/usr/bin/bash", shellcomp.Bash, true},
		{"empty", "", "", false},
		{"nushell", "/usr/bin/nu", "", false},
		{"posix sh", "/bin/sh", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := shellcomp.Detect(tt.shell)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		shell      shellcomp.Shell
		env        shellcomp.Env
		script     string
		rc         string
		blockHas   []string
		noteHas    string
		noRCBlock  bool
		noteAbsent string
	}{
		{
			name: "bash linux", shell: shellcomp.Bash, env: env("linux", nil),
			script: "/home/u/.local/share/bash-completion/completions/memorable", noRCBlock: true, noteHas: "bash-completion",
		},
		{
			name: "bash linux with user dir", shell: shellcomp.Bash, env: env("linux", map[string]string{"BASH_COMPLETION_USER_DIR": "/x"}),
			script: "/x/completions/memorable", noRCBlock: true,
		},
		{
			name: "bash linux with xdg data", shell: shellcomp.Bash, env: env("linux", map[string]string{"XDG_DATA_HOME": "/d"}),
			script: "/d/bash-completion/completions/memorable", noRCBlock: true,
		},
		{
			name: "bash darwin", shell: shellcomp.Bash, env: env("darwin", nil),
			script: "/home/u/.local/share/bash-completion/completions/memorable", rc: "/home/u/.bash_profile",
			blockHas: []string{"declare -F _get_comp_words_by_ref", `"/home/u/.local/share/bash-completion/completions/memorable"`},
			noteHas:  "Homebrew",
		},
		{
			name: "zsh", shell: shellcomp.Zsh, env: env("linux", nil),
			script: "/home/u/.local/share/memorable/completion.zsh", rc: "/home/u/.zshrc",
			blockHas: []string{"$+functions[compdef]", "compinit", `"/home/u/.local/share/memorable/completion.zsh"`},
		},
		{
			name: "zsh with zdotdir", shell: shellcomp.Zsh, env: env("darwin", map[string]string{"ZDOTDIR": "/z"}),
			script: "/home/u/.local/share/memorable/completion.zsh", rc: "/z/.zshrc",
			blockHas: []string{"compinit"},
		},
		{
			name: "fish", shell: shellcomp.Fish, env: env("linux", nil),
			script: "/home/u/.config/fish/completions/memorable.fish", noRCBlock: true,
		},
		{
			name: "fish with xdg config", shell: shellcomp.Fish, env: env("darwin", map[string]string{"XDG_CONFIG_HOME": "/c"}),
			script: "/c/fish/completions/memorable.fish", noRCBlock: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shellcomp.Plan(tt.shell, tt.env)
			assert.Equal(t, tt.script, got.ScriptPath)
			assert.Equal(t, tt.rc, got.RCPath)
			if tt.noRCBlock {
				assert.Empty(t, got.RCPath)
				assert.Empty(t, got.RCBlock)
			}
			for _, want := range tt.blockHas {
				assert.Contains(t, got.RCBlock, want)
			}
			notes := strings.Join(got.Notes, "\n")
			assert.Contains(t, notes, "Open a new shell")
			if tt.noteHas != "" {
				assert.Contains(t, notes, tt.noteHas)
			}
		})
	}
}

func TestUpsertBlock(t *testing.T) {
	t.Parallel()
	block := "source /x"
	marked := startMarker + "\n" + block + "\n" + endMarker
	tests := []struct {
		name     string
		existing string
		want     string
	}{
		{"empty", "", marked + "\n"},
		{"appends after one blank line", "export A=1\n", "export A=1\n\n" + marked + "\n"},
		{"appends after content with no trailing newline", "export A=1", "export A=1\n\n" + marked + "\n"},
		{
			"replaces in place",
			"before\n" + startMarker + "\nold\n" + endMarker + "\nafter\n",
			"before\n" + marked + "\nafter\n",
		},
		{"keeps existing compinit before the block", "source oh-my-zsh.sh\n", "source oh-my-zsh.sh\n\n" + marked + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, shellcomp.UpsertBlock(tt.existing, block))
		})
	}
	t.Run("idempotent", func(t *testing.T) {
		t.Parallel()
		once := shellcomp.UpsertBlock("export A=1\n", block)
		assert.Equal(t, once, shellcomp.UpsertBlock(once, block))
		assert.Equal(t, 1, strings.Count(shellcomp.UpsertBlock(once, block), startMarker))
	})
}

func TestRCBlocks_Syntax(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		bin   string
		flags []string
		shell shellcomp.Shell
		goos  string
	}{
		{"zsh", "zsh", []string{"-n"}, shellcomp.Zsh, "darwin"},
		{"bash on darwin", "bash", []string{"-n"}, shellcomp.Bash, "darwin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := exec.LookPath(tt.bin); err != nil {
				t.Skipf("%s is not installed", tt.bin)
			}
			plan := shellcomp.Plan(tt.shell, env(tt.goos, nil))
			require.NotEmpty(t, plan.RCBlock)
			path := filepath.Join(t.TempDir(), "rc")
			require.NoError(t, os.WriteFile(path, []byte(shellcomp.UpsertBlock("", plan.RCBlock)), 0o600))
			out, err := exec.Command(tt.bin, append(tt.flags, path)...).CombinedOutput() //nolint:gosec // fixed shell names
			require.NoError(t, err, string(out))
		})
	}
}
