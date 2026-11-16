// Package shellcomp plans where tab-completion scripts for bash, zsh, and fish are installed.
package shellcomp

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Shell is a shell that memorable can install completion for.
type Shell string

// Supported shells.
const (
	Bash Shell = "bash"
	Zsh  Shell = "zsh"
	Fish Shell = "fish"
)

const (
	startMarker = "# >>> memorable completion >>>"
	endMarker   = "# <<< memorable completion <<<"
	openShell   = "Open a new shell to use it."
)

// Env is what Plan reads from the environment.
type Env struct {
	GOOS   string
	Home   string
	Getenv func(string) string
}

// Install says where the generated script goes and which rc file, if any, sources it.
type Install struct {
	ScriptPath string
	RCPath     string // empty when the shell autoloads ScriptPath
	RCBlock    string // the block between the markers, without the markers
	Notes      []string
}

// Detect maps the basename of $SHELL to a supported shell.
func Detect(shellEnv string) (Shell, bool) {
	switch sh := Shell(filepath.Base(shellEnv)); sh {
	case Bash, Zsh, Fish:
		return sh, true
	default:
		return "", false
	}
}

// Plan decides the script path, the rc file, and the rc block for a shell.
func Plan(sh Shell, env Env) Install {
	switch sh {
	case Bash:
		return planBash(env)
	case Zsh:
		return planZsh(env)
	case Fish:
		return planFish(env)
	default:
		return Install{}
	}
}

func dataHome(env Env) string {
	if v := env.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(env.Home, ".local", "share")
}

func configHome(env Env) string {
	if v := env.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(env.Home, ".config")
}

func planBash(env Env) Install {
	completions := filepath.Join(dataHome(env), "bash-completion", "completions")
	if v := env.Getenv("BASH_COMPLETION_USER_DIR"); v != "" {
		completions = filepath.Join(v, "completions")
	}
	script := filepath.Join(completions, "memorable")
	if env.GOOS != "darwin" {
		return Install{
			ScriptPath: script,
			Notes:      []string{"bash needs the bash-completion package to load it. " + openShell},
		}
	}
	return Install{
		ScriptPath: script,
		RCPath:     filepath.Join(env.Home, ".bash_profile"),
		RCBlock: fmt.Sprintf("if declare -F _get_comp_words_by_ref >/dev/null && [ -r %q ]; then\n  source %q\nfi",
			script, script),
		Notes: []string{
			"bash on macOS needs Homebrew bash-completion (bash 3.2) or bash-completion@2 (bash 4.2+), " +
				"loaded earlier in ~/.bash_profile. " + openShell,
		},
	}
}

func planZsh(env Env) Install {
	script := filepath.Join(dataHome(env), "memorable", "completion.zsh")
	rc := filepath.Join(env.Home, ".zshrc")
	if v := env.Getenv("ZDOTDIR"); v != "" {
		rc = filepath.Join(v, ".zshrc")
	}
	return Install{
		ScriptPath: script,
		RCPath:     rc,
		RCBlock: fmt.Sprintf("if [ -r %q ]; then\n  (( $+functions[compdef] )) || { autoload -Uz compinit && compinit; }\n  source %q\nfi",
			script, script),
		Notes: []string{openShell},
	}
}

func planFish(env Env) Install {
	return Install{
		ScriptPath: filepath.Join(configHome(env), "fish", "completions", "memorable.fish"),
		Notes:      []string{openShell},
	}
}

// UpsertBlock places block between memorable markers, replacing an existing block or appending
// one at the end of the file, so it runs after any compinit that comes earlier in the file.
func UpsertBlock(existing, block string) string {
	marked := startMarker + "\n" + strings.TrimSpace(block) + "\n" + endMarker
	start := strings.Index(existing, startMarker)
	end := strings.Index(existing, endMarker)
	if start >= 0 && end > start {
		return existing[:start] + marked + existing[end+len(endMarker):]
	}
	if strings.TrimSpace(existing) == "" {
		return marked + "\n"
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + marked + "\n"
}
