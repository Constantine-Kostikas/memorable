# memorable

Persistent, searchable memory for Claude Code.

Claude Code forgets everything between sessions, and its own memory is plain Markdown files with no search. memorable gives it a real memory store over MCP: Claude saves decisions, preferences, and project facts as it works, and finds them again with hybrid semantic and keyword search. An optional prompt hook goes further and surfaces relevant memories before Claude answers, each with a calibrated relevance percentage, so you stop repeating yourself.

Everything runs on your machine. Postgres with pgvector in Docker stores the memories, a local Ollama computes the embeddings, and both listen on `127.0.0.1` only.

- **Proactive recall:** relevant memories show up on their own at session start and with each prompt.
- **Good search:** semantic plus keyword ranking, readable slugs like `infra/db-port`, tags, and links between memories.
- **Scoped:** separate contexts (say, `work` and `personal`) per directory, and per-project memories inside each.
- **Safe by default:** secrets are rejected, prompt injection is quarantined, every change is versioned, and nothing is ever hard-deleted without you.
- **A CLI and a web UI** to search, review, and edit what Claude remembers.

## Quick start

You need macOS or Linux, [Docker](https://docs.docker.com/get-docker/), [Ollama](https://ollama.com/download) (installed natively so it can use your GPU), and [Claude Code](https://claude.com/claude-code).

```sh
curl -fsSL https://raw.githubusercontent.com/constantine-kostikas/memorable/main/install.sh | sh
memorable setup
```

Then start a new Claude Code session. That's it.

The installer downloads the newest release for your platform, checks its sha256, and puts the binary in `~/.local/bin`. Set `MEMORABLE_INSTALL_DIR` to install elsewhere, or `MEMORABLE_VERSION=v0.1.0` to pin a release. `memorable setup` creates the database container, pulls the embedding model, and registers memorable with Claude Code, asking before each change ([details](#what-setup-does)). If anything looks off later, run `memorable doctor`.

Other ways to install:

- **Manually:** download `memorable_<OS>_<arch>.tar.gz` from [Releases](https://github.com/constantine-kostikas/memorable/releases), check it against `checksums.txt`, and put `memorable` on your `PATH`.
- **From source** (Go 1.26): `go install github.com/constantine-kostikas/memorable/cmd/memorable@latest`, or clone and run `make build`. Source builds report version `dev`, so `memorable update` will not manage them.

## How it works

1. `memorable serve` runs as a stdio MCP server that Claude Code starts on demand. It exposes tools such as `memory_search`, `memory_get`, `memory_write`, and `memory_update`.
2. A short block in `~/.claude/CLAUDE.md` tells Claude when to search and when to save.
3. A SessionStart hook preloads your pinned and most used memories, and a UserPromptSubmit hook adds pointers to memories that match the prompt.
4. Each memory is embedded by Ollama and stored in a Postgres schema for its context, with full-text and vector indexes.

## What setup does

`memorable setup --dry-run` previews every change. Setup walks through each step, shows what it will change (as a diff for files), and asks before applying it:

1. Checks Docker and Ollama.
2. Creates the `memorable-postgres` container on `127.0.0.1:5434` with a generated admin password. **The admin password is shown once. Store it in your password manager**; admin commands ask for it.
3. Maps directories to contexts (for example `personal` and `work`) and writes the config.
4. Creates the database role and schemas, and stores the app password in your OS keyring.
5. Pulls the embedding model.
6. Registers the MCP server with Claude Code (`claude mcp add --scope user`).
7. Adds a short instructions block to `~/.claude/CLAUDE.md`.
8. Adds permission rules to `~/.claude/settings.json`, and optionally two hooks: SessionStart (your top memories at session start) and UserPromptSubmit (prompt recall, below).
9. Optionally installs a daily backup job (systemd user timer or launchd agent).
10. Optionally installs tab completion for your login shell (bash, zsh, or fish).
11. Runs `memorable doctor`.

### Shell completion

`memorable setup` installs completion for your login shell. To do it by hand, write the script that `memorable completion <shell>` prints:

- **fish:** `memorable completion fish > ~/.config/fish/completions/memorable.fish`
- **bash on Linux:** `memorable completion bash > ~/.local/share/bash-completion/completions/memorable` (needs the `bash-completion` package).
- **bash on macOS:** install `bash-completion@2` with Homebrew (or `bash-completion` for the stock `/bin/bash` 3.2), then source the script from `~/.bash_profile`.
- **zsh on Linux and macOS:** write `memorable completion zsh` to a file and `source` it at the end of `~/.zshrc`, after `compinit`.

The scripts call back into the binary, so `memorable update` never requires reinstalling them. Completion is static: it never opens the database or the keyring, and ids, slugs, and tags complete to nothing. Context names come from `config.toml`. Release archives include the three scripts under `completions/`.

### Manual setup

If you prefer to do it by hand:

```sh
read -rs "PGPW?admin password: " && echo        # bash: read -rsp "admin password: " PGPW
docker run -d --name memorable-postgres --restart unless-stopped \
  -p 127.0.0.1:5434:5432 \
  -e POSTGRES_USER=memorable_admin -e POSTGRES_PASSWORD="$PGPW" -e POSTGRES_DB=memorable \
  -v memorable-pgdata:/var/lib/postgresql \
  pgvector/pgvector:0.8.7-pg18-trixie
unset PGPW
ollama pull embeddinggemma
memorable context add ~/code personal      # repeat per directory or context
memorable migrate                          # asks for the admin password
claude mcp add --scope user memorable -- "$(command -v memorable)" serve
```

Then append [`docs/claude-md-snippet.md`](docs/claude-md-snippet.md) to `~/.claude/CLAUDE.md` and add the permission rules listed under [Security](#security).

## Configuration

The config lives at `~/.config/memorable/config.toml` on Linux and `~/Library/Application Support/memorable/config.toml` on macOS. Override it with `--config` or `MEMORABLE_CONFIG`; `MEMORABLE_DB_HOST`, `MEMORABLE_DB_PORT`, and `MEMORABLE_DB_NAME` override the database settings.

```toml
fallback_context = "personal"   # read-only access from unmapped directories; omit to disable

[contexts.personal]
paths = ["~/code", "~/notes"]

[contexts.work]
paths = ["~/work"]
```

Each context is a separate Postgres schema, and a Claude Code session can only see the context of its directory. Writes from directories that match no context are refused. Memories in the `profile` context, such as your writing preferences, are readable from every context, and only you can write them, with `memorable profile add` or `memorable ui --admin`.

## Daily use

Claude uses the MCP tools on its own once the CLAUDE.md block is in place. You can use the same operations from the terminal:

| Command | What it does |
|---|---|
| `memorable search "database port"` | Hybrid search for the current directory's context and project |
| `memorable get infra/ports` | Show memories by slug or id |
| `memorable list --status flagged` | Browse with filters |
| `memorable top` | Pinned and most used memories |
| `memorable edit infra/ports` | Edit in `$EDITOR`; rescanned and re-embedded |
| `memorable review` | Work through flagged, expired, stale, and unverified memories |
| `memorable stats` | Counts, embedding coverage, and prompt recall activity |
| `memorable calibrate --dry-run` | How well recall percentages match what you actually fetch |
| `memorable context` | Which context and project this directory uses |
| `memorable profile add --title "Spelling" --content "Use US spelling."` | Memories that apply everywhere (admin) |
| `memorable approve --list` | Review memories quarantined by the injection scanner (admin) |

Every read command supports `-o json`. Output is plain text when piped.

## Prompt recall

With the UserPromptSubmit hook installed, every prompt is searched against your memories before Claude sees it. When something matches well, Claude gets up to three short pointers, each with a relevance percentage:

```
<memory-recall search_id="..." count="1">
- 86% id=... slug=memorable/db-port | Memorable database port: The memorable Postgres container listens on 127.0.0.1:5434...
</memory-recall>
```

Claude fetches the ones it needs with `memory_get`. Most prompts get nothing: recall only shows memories at 70% relevance or more, skips prompts shorter than three words and slash commands, never repeats a memory already shown in the session, and gives up silently after 1.5 seconds. Tune it under `[recall]` in the config (`min_relevance`, `max_items`, `min_words`, `deadline_ms`).

The percentage is calibrated, though not perfectly yet. On the evaluation set's prompt-shaped queries, results scored 70 to 80% were relevant 59% of the time (17 results), and results scored 80 to 90% were relevant every time (18 results). If the 70% cutoff feels noisy, raise `min_relevance` to 0.8. It starts from a built-in fit for `embeddinggemma` and learns from your own use, since a pointer Claude fetches counts as relevant and one it ignores for a day counts as irrelevant. Run `memorable calibrate --dry-run` to see what it has learned, and `memorable calibrate` to save a fit once there are at least 20 of each. `memorable stats` shows how many prompts got pointers and how many were fetched; `memorable doctor` shows which calibration is active.

To add the hook by hand, put this under `hooks` in `~/.claude/settings.json`:

```json
"UserPromptSubmit": [{"hooks": [{"type": "command", "command": "memorable hook prompt-submit"}]}]
```

## Web UI

`memorable ui` opens a local web interface for one context at a time: browse and filter memories, search with a per-result score breakdown, and edit, flag, delete, restore, link, or supersede from the browser. Memories is a two-pane workspace: the list on the left, and on the right the selected memory in an editor that is always open (Ctrl S saves, F expands it to the full window). Ctrl K opens a palette to jump to any memory or page, j and k move through the list, and `?` lists every shortcut. Review and stats pages match `memorable review` and `memorable stats`. `memorable ui --admin` prompts for the admin password in the terminal (it refuses a non-TTY) and adds approve, purge, prune, and profile writes; the unlock expires after 15 idle minutes (only admin actions reset it, including opening Review, which reads the quarantine list as admin), and purge requires typing the slug or id. Options: `--context <name>`, `--port <n>` (random free port by default), and `--no-browser`. `memorable setup` adds the Claude Code deny rule `Bash(memorable ui:*)`, so an agent cannot launch it.

The server binds `127.0.0.1` only. The launch URL on stderr carries a single-use token that is exchanged for an `HttpOnly`, `SameSite=Strict` cookie, and the browser is opened on a private 0600 redirect file so the token never appears in a process argument list. Requests are checked for CSRF token, `Host`, and cross-origin headers, a strict Content-Security-Policy applies, and htmx, `app.js`, fonts, and the Tailwind CSS are embedded with no CDN. Limits: a link previewer or scanner that fetches the launch URL first consumes the token, so restart `memorable ui` for a new link. While `--admin` is unlocked, anyone holding the session cookie has admin rights in the open contexts, and cookies are not scoped by port.

## Backups and restore

`memorable backup` runs `pg_dump` inside the container and keeps the newest 14 dumps in `~/.local/share/memorable/backups`. `memorable export` writes a portable JSON export (`--format md --out dir` for Markdown).

Restore a dump:

```sh
docker exec -i memorable-postgres pg_restore -U memorable_admin -d memorable --clean --if-exists \
  < ~/.local/share/memorable/backups/memorable-YYYYMMDD-HHMMSS.dump
```

## Importing Claude Code memory files

```sh
memorable import ~/.claude/projects/<project>/memory --dry-run
memorable import ~/.claude/projects/<project>/memory
```

Imports go through the same validation, secret scan, injection scan, and duplicate checks as anything Claude writes.

## Update

```sh
memorable update --check   # report what would change
memorable update
```

`memorable update` finds the newest release on GitHub, checks the GPG signature on its `checksums.txt` against the release key built into the binary, checks the archive's sha256, and only then replaces the binary in place. Any failed check aborts and leaves the installed binary untouched.

- **Token:** none needed. If `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token` provides one, update uses it to raise the GitHub API rate limit.
- **Pre-releases:** a stable install only moves to stable releases unless you pass `--pre`. A pre-release install also sees newer pre-releases.
- **Pin a version:** `memorable update --version v0.2.0`, which can also downgrade.
- **Root-owned directories:** if the binary's directory (for example `/usr/local/bin`) is not writable, update stages the verified binary in a private temp directory and runs `sudo install -m 0755 <staged> <target>` on a terminal, or prints that command otherwise. Do not run `sudo memorable update`.
- **Not handled:** development builds (`make build` without a tag, or `go install`) are refused unless you pass `--force`, and Homebrew installs are refused in favor of `brew upgrade`.

Afterwards, restart your Claude Code sessions, because running `memorable serve` processes keep the old binary. Then run `memorable doctor`, and `memorable migrate` if it reports schema versions behind.

## Troubleshooting

Run `memorable doctor`. It checks the config, keyring, container, port, database login, extensions, schema versions, Ollama, the model, and the embedding dimensions, and prints a fix for each failure.

- **Tools say "memory store unavailable"**: the container is stopped (`docker start memorable-postgres`) or the app password is missing (`memorable migrate`).
- **Search ignores meaning, matches keywords only**: Ollama is down or the model is missing; pending embeddings are computed later, or now with `memorable reembed`.
- **After changing the embedding model or dimensions**: `memorable reembed --all`.

## Security

- **Secrets** are rejected on every write, using the gitleaks rule set plus an entropy check.
- **Prompt injection**: text that looks like instructions to an agent is quarantined until you approve it, and quarantined content is never returned to Claude. Memories from tool output are marked unverified and never preloaded.
- **Isolation**: a session is bound to one context schema and cannot choose another.
- **Recoverable**: the app role cannot delete rows. Deletes are soft, every change is versioned, and only `memorable purge` (admin, from the CLI or `memorable ui --admin`) erases data.
- **Local only**: Postgres and Ollama listen on `127.0.0.1`, and remote embedding providers need `allow_remote = true`.
- **Updates are verified**: `memorable update` downloads over HTTPS from `api.github.com` (following redirects only to `*.githubusercontent.com`), checks a detached GPG signature by key `D4A4DEE111ABA5ACBE1F49773B21B7770B4334A4` before trusting any checksum, and fails closed.
- **Docker is a soft boundary**: anyone who can run `docker exec` has full database access. These Claude Code deny rules keep Claude out (setup adds them):

```json
"deny": [
  "Bash(memorable purge:*)", "Bash(memorable approve:*)", "Bash(memorable profile:*)", "Bash(memorable ui:*)",
  "Bash(docker exec memorable-postgres:*)", "Bash(docker inspect memorable-postgres:*)", "Bash(psql:*)",
  "Read(~/.config/memorable/credentials.json)"
]
```

## Development

```sh
make test               # unit tests
make test-integration   # needs Docker
make test-eval          # search quality against local Ollama
make lint vuln sec
```

See [`docs/design.md`](docs/design.md) for the design. Bug reports and pull requests are welcome.

## License

[MIT](LICENSE)
