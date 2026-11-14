# memorable

Persistent, searchable memory for Claude Code. Claude stores and retrieves memories through MCP tools instead of reading files, with hybrid semantic and keyword search, readable keys, and per-project scoping. Everything runs on your machine: Postgres with pgvector in Docker, embeddings from a local Ollama.

## Requirements

- macOS or Linux
- [Docker](https://docs.docker.com/get-docker/) (Docker Desktop on macOS)
- [Ollama](https://ollama.com/download), installed natively so it can use your GPU
- [Claude Code](https://claude.com/claude-code)
- The [GitHub CLI](https://cli.github.com/) while the repository is private

## Install

The repository is private, so install a release with the GitHub CLI (you need collaborator access):

```sh
gh release download -R constantine-kostikas/memorable -p "memorable_$(uname -s)_$(uname -m).tar.gz" -O - | tar -xz memorable
sudo mv memorable /usr/local/bin/
memorable --version
```

To build from source instead: `make build` (needs Go 1.26), then copy `bin/memorable` onto your `PATH`.

## Set up

```sh
memorable setup --dry-run   # preview every change
memorable setup
```

Setup walks through each step, shows what it will change (as a diff for files), and asks before applying it:

1. Checks Docker and Ollama.
2. Creates the `memorable-postgres` container on `127.0.0.1:5434` with a generated admin password. **The admin password is shown once. Store it in your password manager**; admin commands ask for it.
3. Maps directories to contexts (for example `personal` and `work`) and writes the config.
4. Creates the database role and schemas, and stores the app password in your OS keyring.
5. Pulls the embedding model.
6. Registers the MCP server with Claude Code (`claude mcp add --scope user`).
7. Adds a short instructions block to `~/.claude/CLAUDE.md`.
8. Adds permission rules to `~/.claude/settings.json`, and optionally a SessionStart hook.
9. Optionally installs a daily backup job (systemd user timer or launchd agent).
10. Runs `memorable doctor`.

Start a new Claude Code session afterwards. `memorable doctor` checks everything and tells you how to fix what is broken.

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

Each context is a separate Postgres schema, and a Claude Code session can only see the context of its directory. Writes from directories that match no context are refused. Memories in the `profile` context, such as your writing preferences, are readable from every context, and only you can write them, with `memorable profile add`.

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
| `memorable stats` | Counts and embedding coverage |
| `memorable context` | Which context and project this directory uses |
| `memorable profile add --title "Spelling" --content "Use US spelling."` | Memories that apply everywhere (admin) |
| `memorable approve --list` | Review memories quarantined by the injection scanner (admin) |

Every read command supports `-o json`. Output is plain text when piped.

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

## Troubleshooting

Run `memorable doctor`. It checks the config, keyring, container, port, database login, extensions, schema versions, Ollama, the model, and the embedding dimensions, and prints a fix for each failure.

- **Tools say "memory store unavailable"**: the container is stopped (`docker start memorable-postgres`) or the app password is missing (`memorable migrate`).
- **Search ignores meaning, matches keywords only**: Ollama is down or the model is missing; pending embeddings are computed later, or now with `memorable reembed`.
- **After changing the embedding model or dimensions**: `memorable reembed --all`.

## Security

- **Secrets** are rejected on every write, using the gitleaks rule set plus an entropy check.
- **Prompt injection**: text that looks like instructions to an agent is quarantined until you approve it, and quarantined content is never returned to Claude. Memories from tool output are marked unverified and never preloaded.
- **Isolation**: a session is bound to one context schema and cannot choose another.
- **Recoverable**: the app role cannot delete rows. Deletes are soft, every change is versioned, and only `memorable purge` (admin) erases data.
- **Local only**: Postgres and Ollama listen on `127.0.0.1`, and remote embedding providers need `allow_remote = true`.
- **Docker is a soft boundary**: anyone who can run `docker exec` has full database access. These Claude Code deny rules keep Claude out (setup adds them):

```json
"deny": [
  "Bash(memorable purge:*)", "Bash(memorable approve:*)", "Bash(memorable profile:*)",
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

See [`docs/design.md`](docs/design.md) for the design.
