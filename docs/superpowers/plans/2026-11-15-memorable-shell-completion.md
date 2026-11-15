# Shell Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make tab completion for `memorable` correct and installed on Linux and macOS for bash, zsh, and fish.

**Architecture:** Cobra and fang already register `memorable completion bash|zsh|fish|powershell` and the hidden `__complete` command, so command names complete today. This plan fixes what tab returns (enum flag values, paths, and "nothing" instead of filenames for IDs and free text), adds an optional `memorable setup` step that installs the script for the user's login shell, and ships pre-generated scripts in release archives. The generated scripts call `memorable __complete` at runtime, so they never go stale after `memorable update`, and no refresh logic is needed.

**Tech Stack:** Go 1.26, `github.com/spf13/cobra` v1.10.2 (`cobra.Completion`, `cobra.CompletionFunc`, `cobra.FixedCompletions`, `Command.GetFlagCompletionFunc`, `Command.SetCompletionCommandGroupID`), `github.com/charmbracelet/fang`, testify, GoReleaser v2.

**Spec:** `docs/design.md` sections 9 (CLI, `memorable setup`) and 13 (releases). Task 4 amends both sections; the amendment is part of this work.

## Global Constraints

- Static completion only (user decision, 2026-11-15): tab never opens a database connection and never reads the OS keyring. Memory IDs, slugs, and tags complete to nothing. The one dynamic source is context names, read from `config.toml` only.
- Supported shells: bash, zsh, fish on `linux` and `darwin`. PowerShell stays available through cobra but is not installed or documented.
- Setup asks before every change and shows a diff for rc files (design.md section 9). `--dry-run` changes nothing.
- Setup never edits an rc file for fish or for bash on Linux; those shells autoload from a directory.
- Exit codes, output, and lint rules stay as in design.md section 9 and `.golangci.yml`.
- English, US spelling, no em dashes in code, docs, and commit messages. No Claude attribution in commits or PRs.

## Review Focus

1. **macOS zsh with no `compinit`.** Stock macOS `/etc/zshrc` does not run `compinit`, so a sourced cobra script fails with `compdef: command not found` on every new shell. The rc block runs `compinit` only when `compdef` is undefined. Pinned in Task 2 (`TestPlan` asserts the guard, `TestRCBlocks_Syntax` runs `zsh -n`).
2. **oh-my-zsh or a plugin manager runs `compinit` later in `.zshrc`.** An fpath install would need ordering and a `~/.zcompdump` reset. The block is appended at the end of `.zshrc` and sources the script directly, which sidesteps both. Pinned in Task 2 (`TestUpsertBlock` appends after existing content).
3. **Running `memorable setup` twice.** The second run must report "already up to date" for the script and rc file, with no duplicated block. Pinned in Task 2 (`TestUpsertBlock` idempotence) and Task 3 (`writeGenerated` no-op on identical content).
4. **`$SHELL` empty or unrecognized** (nushell, `/bin/sh`, unset in a container). The step warns and skips without failing setup. Pinned in Task 2 (`TestDetect`) and Task 3 (the existing dry-run test, where `SHELL` is empty).
5. **macOS `/bin/bash` 3.2 without Homebrew bash-completion.** The cobra V2 script needs `_get_comp_words_by_ref` from the bash-completion package; without it every tab prints an error. The `.bash_profile` block sources the script only when that function exists. Pinned in Task 2 (`TestPlan` asserts the `declare -F` guard, `TestRCBlocks_Syntax` runs `bash -n`).

---

### Task 1: Correct completion results for every command and flag

**Files:**
- Create: `cmd/memorable/completion.go`
- Create: `cmd/memorable/completion_test.go`
- Modify: `cmd/memorable/root.go` (register `--output`, `--config`; call the defaults walker; set the completion command group)
- Modify: `cmd/memorable/read.go`, `cmd/memorable/profile.go`, `cmd/memorable/transfer.go`, `cmd/memorable/maintain.go`, `cmd/memorable/context.go` (register completions next to the flag or `Args` they belong to)

**Interfaces:**
- Consumes: `deps`, `rootFlags`, `resolveConfigPath(flag string, getenv func(string) string) (string, error)`, `config.Load(path string, getenv func(string) string) (*config.Config, error)`, `(*config.Config).ContextNames() []string`, `runRoot` from `hook_test.go`.
- Produces:
  - `var memoryTypeCompletions = []cobra.Completion{"user", "feedback", "project", "reference"}`
  - `var listStatusCompletions = []cobra.Completion{"active", "unverified", "flagged", "superseded", "deleted"}` (matches the `list --status` help text; `quarantined` is reached through `approve --list`)
  - `func completeContextNames(d deps, flags *rootFlags) cobra.CompletionFunc`
  - `func applyCompletionDefaults(cmd *cobra.Command) error`
  - `func generateCompletion(root *cobra.Command, shell string, w io.Writer) error` (`shell` is `"bash"`, `"zsh"`, or `"fish"`; bash uses `GenBashCompletionV2(w, true)`, zsh `GenZshCompletion(w)`, fish `GenFishCompletion(w, true)`; any other value returns an error). Task 3 and Task 4 use it.

**Completion map** (directive numbers are cobra's: 4 = NoFileComp, 0 = Default (files), 8 = FilterFileExt, 16 = FilterDirs):

| Target | Result |
|---|---|
| `--output` / `-o` (root, persistent) | `text`, `json`; 4 |
| `--type` on `search`, `list`, `profile add` | `memoryTypeCompletions`; 4 |
| `--status` on `list` | `listStatusCompletions`; 4 |
| `--format` on `export` | `json`, `md`; 4 |
| `--context` on `approve`, `purge`, `prune`, and persistent on `profile` | `completeContextNames`; 4 |
| `--config` (root, persistent) | `MarkPersistentFlagFilename("config", "toml")`; 8 |
| `--dir` on `backup` | `MarkFlagDirname("dir")`; 16 |
| `--out` on `export` | `MarkFlagFilename("out")` (file or directory by format); 0 |
| `import <path>` | arg 0: files (0); later args: 4 |
| `context add <path> <name>` | arg 0: directories (16); arg 1: 4 |
| ID, slug, and query args (`get`, `edit`, `purge`, `approve`, `profile edit`, `profile delete`, `search`) | nothing; 4 |
| Every other command arg and every other non-bool flag (`--tag`, `--cursor`, `--title`, `--content`, `--version`, `-k`, `--limit`, ...) | nothing; 4, via `applyCompletionDefaults` |

- [ ] **Step 1: Write the failing tests** in `cmd/memorable/completion_test.go`

Add a helper `complete(t *testing.T, d deps, args ...string) (values []string, directive string)` that calls `runRoot(t, d, "", append([]string{"__complete"}, args...)...)`, collects lines up to the first line starting with `:`, strips any `\t<description>` suffix, and returns that `:N` line as `directive`. Tests (table-driven where shown, all `t.Parallel()`, deps from `defaultDeps()` with `getenv` returning `""` so no real config or keyring is touched):

```go
func TestComplete_FixedValues(t *testing.T) // table:
//  {"list", "--output", ""}         -> ["text","json"], ":4"
//  {"search", "--type", ""}         -> ["user","feedback","project","reference"], ":4"
//  {"list", "--type", ""}           -> same, ":4"
//  {"profile", "add", "--type", ""} -> same, ":4"
//  {"list", "--status", ""}         -> ["active","unverified","flagged","superseded","deleted"], ":4"
//  {"export", "--format", ""}       -> ["json","md"], ":4"

func TestComplete_NothingForIDsAndFreeText(t *testing.T) // table, each -> no values, ":4":
//  get "", edit "", purge "", approve "", profile edit "", profile delete "", search "",
//  stats "", doctor "", list --tag "", list --cursor "", search -k "", update --version ""

func TestComplete_Paths(t *testing.T) // table:
//  {"--config", ""}                 -> ["toml"], ":8"
//  {"backup", "--dir", ""}          -> [], ":16"
//  {"import", ""}                   -> [], ":0"
//  {"import", "a.json", ""}         -> [], ":4"
//  {"context", "add", ""}           -> [], ":16"
//  {"context", "add", "/tmp", ""}   -> [], ":4"

func TestComplete_ContextNamesFromConfig(t *testing.T)
// Writes a config with contexts "personal" and "work" to t.TempDir() (build it the way setup
// does: config.Load on a missing path for defaults, set Contexts, encodeConfig, os.WriteFile).
// {"--config", path, "prune", "--context", ""} -> ["personal","work"], ":4"
// {"--config", path, "profile", "list", "--context", ""} -> ["personal","work"], ":4"

func TestComplete_ContextNamesBrokenConfig(t *testing.T)
// Config file containing "not toml [". {"--config", path, "prune", "--context", ""} -> [], ":4", no error.

func TestCompletionValues_ParseAsEnums(t *testing.T)
// Every memoryTypeCompletions entry passes memory.ParseType; every listStatusCompletions entry passes memory.ParseStatus.

func TestApplyCompletionDefaults_CoversEveryCommand(t *testing.T)
// Walks newRootCmdWith(buildInfo{}, defaultDeps()). For every command: ValidArgsFunction != nil
// or len(ValidArgs) > 0. For every flag defined on it (LocalNonPersistentFlags and
// PersistentFlags) whose Value.Type() != "bool": GetFlagCompletionFunc finds one, or the flag
// carries the cobra.BashCompFilenameExt or cobra.BashCompSubdirsInDir annotation.

func TestCompletionScripts_ParseInRealShells(t *testing.T)
// For bash, zsh, fish: generateCompletion into a temp file, then run `bash -n`, `zsh -n`,
// `fish --no-execute` on it. t.Skip per shell when exec.LookPath fails.
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./cmd/memorable/ -run 'TestComplete|TestCompletion|TestApplyCompletionDefaults' -v`
Expected: compile failure (`memoryTypeCompletions`, `applyCompletionDefaults`, `generateCompletion`, `completeContextNames` undefined).

- [ ] **Step 3: Implement `cmd/memorable/completion.go`**

- The two value slices and `generateCompletion` as in Interfaces.
- `completeContextNames(d deps, flags *rootFlags) cobra.CompletionFunc`: resolve the path with `resolveConfigPath(flags.configPath, d.getenv)`, `config.Load`, return `ContextNames()` with `ShellCompDirectiveNoFileComp`. Any error returns `nil, ShellCompDirectiveNoFileComp`. It must not call anything that opens a store or the keyring.
- `applyCompletionDefaults(cmd *cobra.Command) error`: recursive over `cmd.Commands()`. Set `ValidArgsFunction = cobra.NoFileCompletions` where it is nil and `ValidArgs` is empty. For each non-bool flag in `LocalNonPersistentFlags()` and `PersistentFlags()` with no `GetFlagCompletionFunc` and no filename or dirname annotation, `RegisterFlagCompletionFunc(name, cobra.NoFileCompletions)`. Return the first registration error.

- [ ] **Step 4: Register the explicit completions from the map**

Register each one next to the flag or `Args` it belongs to in its constructor (`read.go`, `profile.go`, `transfer.go`, `maintain.go`, `context.go`). Use `cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp)` for fixed sets and `cobra.NoFileCompletions` for ID args. In `newRootCmdWith`, register `--output`, mark `--config`, call `root.SetCompletionCommandGroupID(groupSetup)`, and call `applyCompletionDefaults(root)` last. Its error can only come from a programming mistake that the walker test catches, so `panic` on it. `import` and `context add` need small `ValidArgsFunction`s that switch on `len(args)`.

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `go test -race ./cmd/memorable/`
Expected: PASS, including the existing `TestSetup_*` and `root_test.go` tests.

- [ ] **Step 6: Check help and lint**

Run: `go run ./cmd/memorable --help` and `make lint`
Expected: `completion` is listed in the Setup group; lint reports nothing.

- [ ] **Step 7: Commit**

```bash
git add cmd/memorable/
git commit -m "feat(cli): complete flag values and stop suggesting files for ids"
```

### Task 2: Per-shell install planning (`internal/shellcomp`)

**Files:**
- Create: `internal/shellcomp/shellcomp.go`
- Create: `internal/shellcomp/shellcomp_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (pure functions, no cobra import).
- Produces:

```go
type Shell string

const (
	Bash Shell = "bash"
	Zsh  Shell = "zsh"
	Fish Shell = "fish"
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

func Detect(shellEnv string) (Shell, bool)       // basename of $SHELL; false for anything else
func Plan(sh Shell, env Env) Install
func UpsertBlock(existing, block string) string  // markers "# >>> memorable completion >>>" / "# <<< memorable completion <<<"
```

**Plan rules** (`data` = `$XDG_DATA_HOME` or `<home>/.local/share`; `cfg` = `$XDG_CONFIG_HOME` or `<home>/.config`):

| Shell | GOOS | ScriptPath | RCPath | RCBlock |
|---|---|---|---|---|
| bash | linux | `$BASH_COMPLETION_USER_DIR/completions/memorable`, else `<data>/bash-completion/completions/memorable` | none | none |
| bash | darwin | same as linux | `<home>/.bash_profile` | source the script only when `declare -F _get_comp_words_by_ref >/dev/null` and the file is readable |
| zsh | both | `<data>/memorable/completion.zsh` | `$ZDOTDIR/.zshrc`, else `<home>/.zshrc` | if the file is readable: `(( $+functions[compdef] )) \|\| { autoload -Uz compinit && compinit; }`, then `source` it |
| fish | both | `<cfg>/fish/completions/memorable.fish` | none | none |

Paths in a block are absolute and double-quoted. Notes: bash on linux says it needs the `bash-completion` package; bash on darwin says it needs Homebrew `bash-completion` (bash 3.2) or `bash-completion@2` (bash 4.2+) loaded earlier in `~/.bash_profile`; every shell says "open a new shell to use it".

UpsertBlock follows `claudecfg.UpsertClaudeMD`: replace an existing marked block in place, otherwise append after one blank line (or return the block plus a newline for empty input). It appends at the end on purpose, so the block runs after oh-my-zsh or any other `compinit` in the file.

- [ ] **Step 1: Write the failing tests**

```go
func TestDetect(t *testing.T) // table:
//  "/bin/zsh" -> Zsh,true; "/opt/homebrew/bin/fish" -> Fish,true; "/usr/bin/bash" -> Bash,true;
//  "" -> false; "/usr/bin/nu" -> false; "/bin/sh" -> false

func TestPlan(t *testing.T) // table over every row above, plus:
//  bash linux with BASH_COMPLETION_USER_DIR=/x -> ScriptPath "/x/completions/memorable"
//  zsh with ZDOTDIR=/z -> RCPath "/z/.zshrc"
//  fish with XDG_CONFIG_HOME=/c -> ScriptPath "/c/fish/completions/memorable.fish"
//  zsh block contains "$+functions[compdef]", "compinit", and the quoted ScriptPath
//  bash darwin block contains "declare -F _get_comp_words_by_ref" and the quoted ScriptPath
//  bash linux and fish: RCPath == "" and RCBlock == ""

func TestUpsertBlock(t *testing.T) // table:
//  "" -> markers + block + "\n"
//  "export A=1\n" -> "export A=1\n\n" + marked block + "\n"
//  existing content with an old marked block in the middle -> block replaced, surrounding text intact
//  UpsertBlock(UpsertBlock(x, b), b) == UpsertBlock(x, b)

func TestRCBlocks_Syntax(t *testing.T)
// Writes Plan(Zsh, ...) block to a file and runs `zsh -n`; Plan(Bash, darwin) block with `bash -n`.
// t.Skip per shell when exec.LookPath fails.
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/shellcomp/ -v`
Expected: compile failure (package has no Go files or undefined symbols).

- [ ] **Step 3: Implement `internal/shellcomp/shellcomp.go`** with the signatures and rules above.

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test -race ./internal/shellcomp/ && make lint`
Expected: PASS; lint clean.

- [ ] **Step 5: Commit**

```bash
git add internal/shellcomp/
git commit -m "feat(shellcomp): plan completion installs for bash, zsh, and fish"
```

### Task 3: `memorable setup` installs completion

**Files:**
- Create: `cmd/memorable/setup_completion.go`
- Modify: `cmd/memorable/setup.go` (extract the atomic write from `writeFile`; add the step to the list)
- Modify: `cmd/memorable/setup_test.go`

**Interfaces:**
- Consumes: `generateCompletion` (Task 1); `shellcomp.Detect`, `shellcomp.Plan`, `shellcomp.Env`, `shellcomp.UpsertBlock` (Task 2); `setupRun.heading`, `confirm`, `writeFile`, `readOptional`.
- Produces:
  - `func writeAtomic(path, content string, perm fs.FileMode) error`: the mkdir, temp file, and rename from today's `writeFile`, which now calls it.
  - `func (s *setupRun) writeGenerated(path, content, what string) error`: like `writeFile`, but prints `  <what>: <path>` instead of a diff. Prints "already up to date" when the content is identical. Asks "Write <path>?". Writes nothing in dry-run. New files get mode `0o644`.
  - `func (s *setupRun) shellCompletion() error`

**Step behavior:** heading "Shell completion". `Detect(s.d.getenv("SHELL"))`; when it fails, `Warn` "skipped: unrecognized shell; see `memorable completion --help`" and return nil. `confirm("Install tab completion for <shell>?", true)`; on no, return nil. Generate the script from `s.cmd.Root()`, then `writeGenerated(plan.ScriptPath, script, "<shell> completion script")`. When `RCPath != ""`, `writeFile(RCPath, UpsertBlock(before, RCBlock))` so the user sees the rc diff. Print each note with `p.Muted`. It runs after `backupTimer` and before `check`.

- [ ] **Step 1: Write the failing tests**

```go
// TestSetup_DryRunAppliesNothing stays unchanged (SHELL is ""); add to it:
//   assert.Contains(t, out, "Shell completion"); assert.Contains(t, out, "unrecognized shell")

func TestSetup_DryRunShellCompletion(t *testing.T) // table; wraps setupDeps' getenv to also return SHELL:
//  {shell "/bin/zsh", goos "linux"}  -> out contains home+"/.zshrc", home+"/.local/share/memorable/completion.zsh", "compinit"
//  {shell "/bin/bash", goos "darwin"} -> out contains home+"/.bash_profile", "_get_comp_words_by_ref"
//  {shell "/usr/bin/fish", goos "linux"} -> out contains home+"/.config/fish/completions/memorable.fish", no ".bashrc"/".zshrc"
// Every case: os.ReadDir(home) is empty after the run (dry run wrote nothing).

func TestWriteAtomic_KeepsContentAndMode(t *testing.T)
// Writes into a nested missing directory; file content matches; mode == the perm passed.
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./cmd/memorable/ -run 'TestSetup|TestWriteAtomic' -v`
Expected: FAIL (no "Shell completion" heading; `writeAtomic` undefined).

- [ ] **Step 3: Implement** `writeAtomic`, `writeGenerated`, and `shellCompletion` as specified, and insert `s.shellCompletion` into the step list in `newSetupCmd` between `s.backupTimer` and the `check` closure.

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test -race ./cmd/memorable/ && make lint`
Expected: PASS; lint clean.

- [ ] **Step 5: Manual check on this Linux machine**

Run: `make build && HOME=$(mktemp -d) SHELL=/bin/zsh ./bin/memorable setup --dry-run`
Expected: the Shell completion step lists the script path and a `.zshrc` diff, and "dry run: nothing was changed".

- [ ] **Step 6: Commit**

```bash
git add cmd/memorable/
git commit -m "feat(setup): install shell completion for the login shell"
```

### Task 4: Release archives, docs, and spec

**Files:**
- Modify: `Makefile` (add `completions` to `.PHONY` and a target)
- Modify: `.goreleaser.yaml` (`before.hooks`, `archives.files`, commented `homebrew_casks` block)
- Modify: `.gitignore` (`/completions/`)
- Modify: `README.md` (setup step list; new "Shell completion" section)
- Modify: `docs/design.md` (section 9 commands table and setup steps; section 13 archive contents)

**Interfaces:**
- Consumes: `memorable completion <shell>` (cobra built-in, backed by the same generators as `generateCompletion`).
- Produces: `completions/memorable.bash`, `completions/memorable.zsh`, and `completions/memorable.fish` in every release archive.

- [ ] **Step 1: Add the Makefile target**

`completions:` creates `completions/` and writes `go run ./cmd/memorable completion <shell> > completions/memorable.<shell>` for bash, zsh, and fish.

- [ ] **Step 2: Wire GoReleaser**

Append `make completions` to `before.hooks` after `go mod tidy`. Add `completions/*` to `archives[0].files` next to `README.md`. In the commented `homebrew_casks` block, add the cask's completion fields pointing at the three files, but only after confirming the field names in the GoReleaser v2 `homebrew_casks` schema (`https://goreleaser.com/static/schema.json`). If the schema has no completion field, add a one-line comment saying so instead.

- [ ] **Step 3: Verify the archive contents**

Run: `go run github.com/goreleaser/goreleaser/v2@latest check && go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=sign && tar -tzf dist/memorable_Linux_x86_64.tar.gz`
Expected: `check` passes; the listing contains `completions/memorable.bash`, `completions/memorable.zsh`, and `completions/memorable.fish`. Then `rm -rf dist completions`.

- [ ] **Step 4: Update README.md**

- In "Set up", insert "Optionally installs tab completion for your login shell (bash, zsh, or fish)." as step 10 and renumber "Runs `memorable doctor`." to 11.
- Add a "Shell completion" section that says setup handles it, then gives the manual equivalent per shell:
  - fish: write `memorable completion fish` to `~/.config/fish/completions/memorable.fish`.
  - bash on Linux: write `memorable completion bash` to `~/.local/share/bash-completion/completions/memorable`; requires the `bash-completion` package.
  - bash on macOS: `brew install bash-completion@2` (with Homebrew bash), or `bash-completion` for `/bin/bash` 3.2, then source the script from `~/.bash_profile`.
  - zsh on both: write `memorable completion zsh` to a file and source it at the end of `~/.zshrc`, after `compinit`.
  - Note that the scripts call back into the binary, so `memorable update` never requires reinstalling them.

- [ ] **Step 5: Amend docs/design.md**

- Section 9 commands table: add `memorable completion bash|zsh|fish` with "Print the completion script (cobra); setup installs it".
- Section 9 setup steps: insert "Optional: tab completion for the login shell (script file, plus a marked block in `.zshrc`, or in `.bash_profile` on macOS bash)" before the `doctor` step, and renumber.
- Section 9 Presentation: one line saying completion is static and never touches the database or keyring; context names come from the config file.
- Section 13 Release: archives include `completions/memorable.{bash,zsh,fish}`.

- [ ] **Step 6: Commit**

```bash
git add Makefile .goreleaser.yaml .gitignore README.md docs/design.md
git commit -m "build(release): ship completion scripts and document shell completion"
```

### Task 5: End-to-end verification

**Files:** none.

- [ ] **Step 1: Full test and static checks**

Run: `make test && make lint && make vuln && make sec`
Expected: all pass.

- [ ] **Step 2: Linux, real shells** (install with `HOME` pointing at a temp dir so the real dotfiles stay untouched)

Run `HOME=$tmp SHELL=<shell> ./bin/memorable setup`, accept only the completion step (decline the rest), then start the shell with that `HOME` and confirm:
- `memorable li<TAB>` completes to `list`.
- `memorable list --status <TAB>` offers the five statuses.
- `memorable get <TAB>` offers nothing (no filenames).
- `memorable --config <TAB>` offers `.toml` files and directories.

Repeat for zsh, bash, and fish. Then rerun setup and confirm both the script and the rc file report "already up to date".

- [ ] **Step 3: macOS, real shells** (needs a Mac; record the result in the PR)

Repeat Step 2 with:
- stock `/bin/zsh` and no `.zshrc`, which proves the `compinit` guard;
- zsh with oh-my-zsh;
- `/bin/bash` 3.2 without bash-completion (expect no completion and no errors at startup or on tab);
- Homebrew bash with `bash-completion@2`.
