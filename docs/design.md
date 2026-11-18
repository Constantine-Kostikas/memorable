# memorable v1 Design

Status: draft for confirmation (grilled, rounds 1 to 5)
Date: 2026-11-14

## 1. Purpose

memorable is a persistent memory service for Claude Code. Claude stores and retrieves memories through MCP tools instead of reading files. Retrieval combines semantic (vector) search and keyword search, ranked by relevance with small usage-based boosts.

### Goals

- Store, retrieve, update, and delete memories through MCP tools.
- Hybrid search (pgvector + Postgres full-text + trigram) with relevance cutoffs.
- Token-efficient access: two-step retrieval, readable keys, automatic scoping.
- Strong defenses against memory poisoning and secret leakage.
- Hard isolation between contexts (work, personal, clients).
- A polished CLI and a guided `memorable setup` so colleagues can run their own instance on Linux or macOS.

### Non-goals (v1)

- Multi-user or shared servers, HTTP transport, per-user auth (v2).
- Metrics endpoints, tracing, and pprof endpoints (`serve` opens no listeners; only `memorable ui` listens, on loopback).
- Column-level encryption (full-disk encryption is the at-rest control).
- Windows support.

### v2 readiness

Every table carries an `owner` column (defaults to the OS username), the MCP transport sits behind an interface, and every embedding records its model and dimension. v2 must decide between Postgres row-level security and owner filtering in Go (the Go database skill discourages row-level security).

## 2. Architecture

```
Claude Code ──stdio──> memorable serve ──TCP 127.0.0.1:5434──> Docker: pgvector/pgvector (Postgres 18)
                            │                                     schemas: mem_<context>, mem_profile
                            └──HTTP 127.0.0.1:11434──> Ollama (native install)

You ──> memorable <command>        (same binary: CLI, setup, admin operations)
You ──> memorable ui ──HTTP 127.0.0.1:<random port>──> your browser   (web UI; one pool per open context)
Claude Code SessionStart hook ──> memorable hook session-start   (opt-in)
```

One Go binary provides the MCP server, the CLI, the setup wizard, and the hook command.

### Style

- Pragmatic layering: the `memory` package holds all business rules; the store, embedder, scanners, MCP server, and CLI are adapters around it.
- Interfaces are defined in `memory`, where they are consumed. Concrete types are returned from constructors.
- Manual constructor injection wired in `cmd/memorable`. Functional options only for constructors with optional settings. No DI library, no `init()` for setup, no mutable globals.
- Domain enums (status, type, source, feedback kind) are Go types whose zero value is `Unknown`. The database stores text with CHECK constraints; conversion lives in one place.

### Stack

| Concern | Choice |
|---|---|
| Language | Go 1.25 |
| Module path | `github.com/constantine-kostikas/memorable` |
| MCP | `github.com/modelcontextprotocol/go-sdk` |
| Database driver | `github.com/jackc/pgx/v5` + `github.com/pgvector/pgvector-go` |
| Migrations | `github.com/pressly/goose/v3`, embedded; Go migrations where SQL is not enough |
| CLI framework | `github.com/spf13/cobra` + `github.com/charmbracelet/fang` (styled help, errors, version, completions, man pages) |
| CLI presentation | `github.com/charmbracelet/lipgloss` (tables, panels), `github.com/charmbracelet/huh` (forms, spinners) |
| Config | TOML via `github.com/BurntSushi/toml` + explicit env overrides (no Viper) |
| Secrets | `github.com/zalando/go-keyring` with a 0600 file fallback |
| Secret scanning | gitleaks default rule file (embedded, evaluated in-house) + entropy check; the gitleaks module itself is not imported because of its dependency tree |
| Logging | `log/slog`, JSON, **stderr only** |
| Self-update | `github.com/ProtonMail/go-crypto/openpgp` (detached signature check; `golang.org/x/crypto/openpgp` is deprecated) + `golang.org/x/mod/semver` |
| Tests | `testing`, testify, testcontainers-go, `go.uber.org/goleak`, `testing/synctest` |
| Database | PostgreSQL 18 + pgvector 0.8.7 (image `pgvector/pgvector:0.8.7-pg18-trixie`) + pg_trgm |
| Embeddings | Ollama `/api/embed` (batch input, L2-normalized output); default model `embeddinggemma` (768 dimensions) |

### Package layout

```
cmd/memorable/          main.go plus one thin file per command; wiring only
internal/config/        TOML loading, env overrides, validation
internal/scope/         context, fallback, and project resolution
internal/memory/        service layer, domain types, sentinel errors, consumer interfaces
internal/store/         Postgres repository; embedded migrations
internal/embed/         Ollama embedder (remote providers behind allow_remote)
internal/scan/          secret and injection scanners
internal/rank/          RRF fusion, boosts, decay math
internal/worker/        async embedding job runner
internal/mcpserver/     tools, resources, transport interface
internal/secret/        keyring with file fallback
internal/dockerpg/      container lifecycle for setup and backup
internal/claudecfg/     MCP registration, settings.json, CLAUDE.md, hook edits (diff + confirm)
internal/backup/        pg_dump via the container, rotation
internal/webui/          web UI: handlers, middleware, templates, embedded htmx and Tailwind CSS
internal/ui/            lipgloss styles, TTY and NO_COLOR detection, output formats
internal/update/        self-update: release selection, GitHub API client, GPG and checksum verification, atomic replace
internal/hook/          SessionStart output
testdata/eval/          search evaluation set
```

## 3. Infrastructure

### PostgreSQL (Docker on every platform)

```sh
read -rs "PGPW?memorable admin password: " && echo
docker run -d --name memorable-postgres --restart unless-stopped \
  -p 127.0.0.1:5434:5432 \
  -e POSTGRES_USER=memorable_admin \
  -e POSTGRES_PASSWORD="$PGPW" \
  -e POSTGRES_DB=memorable \
  -v memorable-pgdata:/var/lib/postgresql \
  pgvector/pgvector:0.8.7-pg18-trixie
unset PGPW
```

- Bound to `127.0.0.1` only. Port 5434 by default (5432 and 5433 are commonly taken); override in config or `MEMORABLE_DB_PORT`.
- Postgres 18 images mount the volume at `/var/lib/postgresql`, not `/var/lib/postgresql/data`.
- pgvector 0.8+ is required: filtered HNSW queries use `SET hnsw.iterative_scan = relaxed_order` so scope and status filters still return k results.
- `memorable setup` runs this for colleagues, generating passwords so no interactive shell prompt is involved.
- Status on the author's machine: running, `vector` 0.8.7 and `pg_trgm` 1.6 enabled.

### Ollama (native on every platform)

- Linux: official install script (systemd service on `127.0.0.1:11434`). macOS: native app (Metal acceleration; Docker on macOS has no GPU access).
- Local models only. Cloud models would send memory content off the machine.
- `memorable setup` pulls the configured model.

## 4. Contexts and scoping

### Contexts

A context is the hard isolation boundary. Each context has its own schema `mem_<context>`.

- The server resolves the context once at startup and sets `search_path` on every pooled connection (`AfterConnect`). No tool parameter can choose a context.
- Every pooled session also sets `statement_timeout = 30s`, `lock_timeout = 5s`, and `idle_in_transaction_session_timeout = 60s`, so a stuck query or held lock cannot exhaust the 4-connection pool. Timeouts surface as `ErrUnavailable`. Migrations use their own connection and are not bounded.
- Resolution order: MCP client roots, then process working directory, then path-prefix match in config.
- **Unmatched directory:** writes are refused with `ErrReadOnly`. Reads use `fallback_context` if configured (read-only); otherwise every tool returns a clear "no context for this directory" error that names `memorable context add`.
- Author's machine: one context, `personal` = `~/development/projects`, `~/development/OBSIDIAN`; `fallback_context = "personal"`. Colleagues: `fallback_context` unset by default.

### Profile schema

`mem_profile` holds memories about the user that apply everywhere (preferences, conventions).

- Readable from every context; included in search, top, and preload with `scope="profile"`.
- Writable **only through the CLI or `memorable ui --admin`**, both as the admin role (`memorable profile add/edit/delete`). The app role used by `serve` has SELECT only on `mem_profile`. Claude can never write there.

### Projects

Within a context, a memory is project-scoped or context-global (`project IS NULL`).

- Project identity: normalized git remote `origin` URL, else git repository root path, else working directory.
- Search defaults to current project + context-global + profile, with a boost for current-project matches. `all_projects: true` widens it.

## 5. Data model

Tables in each `mem_<context>` schema (and the same `memories`/`memory_versions` shape in `mem_profile`). Every table has `owner text NOT NULL`. No triggers, views, or stored procedures. Generated columns are allowed.

### memories

| Column | Type | Notes |
|---|---|---|
| id | uuid PK | UUIDv7, generated in Go |
| owner | text | OS username |
| slug | text NULL | `[a-z0-9][a-z0-9/-]{1,79}`; partial unique on `(owner, slug) WHERE status <> 'deleted'` |
| project | text NULL | NULL = context-global |
| type | text | `user`, `feedback`, `project`, `reference` |
| title | text | max 120 chars |
| content | text | max 2,000 chars; one memory, one vector, no chunking |
| tags | text[] | max 10, each `[a-z0-9-]{1,40}`; GIN index; normalized on write (lowercase, spaces and underscores to hyphens, deduped) |
| source | text | `user_stated`, `claude_inferred`, `tool_output`, `imported` |
| status | text | lifecycle below |
| pinned | bool | always in preload |
| expires_at | timestamptz NULL | expired memories drop out of search |
| version | int | increments on every change |
| content_hash | bytea | sha256 of normalized title + content |
| fts | tsvector | generated from title (weight A), tags (weight A), and content (weight B); GIN index |
| embedding | vector(768) NULL | HNSW, cosine ops |
| embedding_model | text NULL | |
| embedded_at | timestamptz NULL | |
| access_count | bigint | lifetime counted accesses |
| access_score | float8 | decayed score at `score_updated_at` |
| score_updated_at | timestamptz | |
| rank_key | float8 | `ln(access_score) + λ·epoch(score_updated_at)`; B-tree index |
| created_at, updated_at, last_accessed_at | timestamptz | |
| deleted_at | timestamptz NULL | |

Trigram GIN index on `title`.

`rank_key` makes "top by decayed score" indexable: effective score is `s·e^(−λ(now−t))`, so `ln(effective) = ln s + λt − λ·now`; the last term is shared by all rows, so ordering by `ln s + λt` equals ordering by effective score at any moment.

### Status lifecycle

| Status | Searchable | Preload | Meaning |
|---|---|---|---|
| `active` | yes | yes, unless source is `tool_output` | normal |
| `unverified` | yes, labeled | no | written with source `tool_output` |
| `flagged` | only with `include_flagged` | no | feedback said wrong or outdated |
| `quarantined` | no; content never returned over MCP | no | injection scanner hit; approve or purge as admin (CLI or `ui --admin`) |
| `superseded` | only with `include_superseded` | no | replaced by a newer memory |
| `deleted` | no | no | soft delete; restorable |

### Supporting tables

- **memory_versions** `(memory_id, version)` PK: title, content, tags, type, change kind (`create`, `update`, `supersede`, `delete`, `restore`), changed_at.
- **memory_links** `(from_id, to_id, kind)` PK: `related`, `supersedes`, `contradicts`; soft removal via `removed_at`.
- **memory_project_access** `(memory_id, project)` PK: count, decayed score, score_updated_at, rank_key.
- **memory_feedback**: memory_id, kind (`useful`, `wrong`, `outdated`), note, project, created_at.
- **access_events**: kind (`search`, `get`, `inject`, `preload`), query (NULL when the prompt held a secret), project, returned ids, fetched id, search_id, session_id, details jsonb (origin, hook source, embedding model and dimensions, and per-result retrieval signals), created_at. 90-day retention.
- **calibrations**: embedding model, dimensions, coefficients b0 to b4, relevant and irrelevant label counts, source, fitted_at. Append-only for the app role.
- **audit_log**: append-only; action, memory_id, content hash (never content), cwd, process id + start time, details jsonb.
- **embedding_jobs**: memory_id, target version, attempts, next_attempt_at, last_error, done_at.

### Migrations

Written by Claude and verified by integration tests (author waived manual review): schema applies cleanly on an empty database and on the previous version, grants are correct, and `EXPLAIN` on a 10,000-row seeded dataset confirms the search queries use the HNSW, GIN, and B-tree indexes. Applied only by `memorable migrate` (or setup), never automatically on `serve` startup.

## 6. MCP interface

### Output format

Every returned memory is wrapped and labeled as data:

```
<memory id="0193..." slug="memorable/db-choice" type="project" source="user_stated" status="active" scope="project">
...content...
</memory>
```

Results also carry structured JSON. Total tool description budget: under 1,500 tokens.

### Tools

| Tool | Purpose |
|---|---|
| `memory_write` | Create 1 to 20 memories: title, content, type, tags, slug, source, project (`current` or `global`), pinned, expires_at, supersedes, `on_similar` (`ask` default, or `keep_both`). |
| `memory_get` | Fetch by ids or slugs (up to 20); optional `search_id`. Counted access. Returns related link ids. |
| `memory_update` | Change title, content, tags, type, pinned, expires_at; optional `expected_version`. Rescans and re-embeds. |
| `memory_delete` | Soft delete. |
| `memory_restore` | Undelete, or restore a prior version. |
| `memory_history` | List versions. |
| `memory_search` | Hybrid search: query, k (default 5, max 20), tags (all of), tags_any (at least one), types, all_projects, include_flagged, include_superseded. Returns `search_id` and compact results (id, slug, title, 200-char snippet, type, tags, scope, status, score). |
| `memory_list` | Browse by tags (all of), tags_any (at least one), type, project, or status; paginated; compact. |
| `memory_tags` | Tags in use with counts, for the current project, context-global, and profile memories; `all_projects` widens it. |
| `memory_top` | Top by decayed score for current project + context-global + profile; compact. |
| `memory_feedback` | `useful`, `wrong`, or `outdated`, optional note. |
| `memory_link` | Add or remove a link. |
| `memory_review` | Flagged, expired, stale, unverified, and conflict links; quarantined count only. |

### Resources

- `memorable://top`: the preload set.
- `memorable://memory/{slug}`: one memory by slug.

### Errors

Sentinel errors in `memory`: `ErrNotFound`, `ErrConflict`, `ErrValidation`, `ErrSecretDetected`, `ErrQuarantined`, `ErrReadOnly`, `ErrNoContext`, `ErrSchemaBehind` (a missing column or table: the binary is newer than the schema; search still returns results without a `search_id` until `memorable migrate` runs). Inner layers wrap with `%w`. The MCP boundary maps them to tool errors with short safe messages (database internals never reach Claude) and logs each error once, there.

### Recommended Claude Code permissions

- Allow: `memory_search`, `memory_get`, `memory_list`, `memory_tags`, `memory_top`, `memory_history`, `memory_review`, `memory_write`, `memory_update`, `memory_feedback`, `memory_link`.
- Ask: `memory_delete`, `memory_restore`.
- Deny (Bash): `memorable purge*`, `memorable approve*`, `memorable profile*`, `memorable ui*`, `docker exec memorable-postgres*`, `docker inspect memorable-postgres*`, `psql*`.
- Deny (Read): the credential fallback file.

## 7. Core flows

### Write

1. Validate sizes, formats, slug uniqueness, batch limits.
2. Secret scan: reject the item with the rule name, never echoing the secret.
3. Injection scan: store as `quarantined`; tell Claude it needs human approval.
4. Exact duplicate by content_hash: return the existing memory.
5. Embed synchronously (2-second timeout); on failure write anyway and queue a job.
6. Similarity check (vector, else trigram on content): ≥ 0.95 duplicate (return existing); 0.75 to 0.95 with `on_similar: ask` returns candidates with `needs_decision` and writes nothing.
7. Insert memory, version 1, and audit entry in one transaction. Source `tool_output` sets `unverified`.
8. `supersedes`: mark the old memory `superseded` and link, same transaction.

### Search

1. Candidate lists (50 each), filtered by status, expiry, scope: vector (HNSW with iterative scan), full text (`websearch_to_tsquery`, `ts_rank_cd`), title trigram.
2. Reciprocal rank fusion: `rrf = Σ 1/(60 + rank_i)`.
3. `final = rrf × (1 + 0.20·project_match + 0.10·pinned + min(0.15, 0.05·ln(1 + effective_score)))`; per-project score when present, else global.
4. Relevance cutoff: drop results with cosine similarity below `min_similarity` (default 0.30) and no full-text match. Empty results are valid.
5. Return top k with a new `search_id`; log an access event.

### Access counting

- Counted access: `score = score·2^(−Δt/half_life) + weight` (half-life 30 days) on global and per-project scores; update `rank_key`.
- `memory_get`: weight 1. `memory_feedback useful`: weight 2.
- Search hits, preload, and resource reads are never counted.

### Feedback

- `useful`: counted access, weight 2.
- `wrong`: `flagged`; out of preload and default search; into review.
- `outdated`: `flagged`; review suggests update, supersede, or expiry.

### Preload (hook and `memorable://top`)

Pinned first, then highest `rank_key` among `active` memories whose source is not `tool_output`, across current project, context-global, and profile. Budget: 10 items, 4,000 characters. Titles, slugs, and snippets only. One extra line, `Tags in use: a (12), b (7)`, lists the top 20 tags by count; it sits outside the character budget and is omitted when no tags exist. The SessionStart hook bounds all of its work with a 3-second deadline and prints nothing on any failure, so a stopped database delays session start by seconds at most.

### Prompt recall (opt-in UserPromptSubmit hook)

`memorable hook prompt-submit` reads the prompt from the hook input and prints pointers to at most `max_items` (3) memories whose calibrated relevance is at least `min_relevance` (70%), wrapped in `<memory-recall search_id="...">` as stored data. Each pointer shows the percentage, id, slug, title, and snippet; Claude fetches the ones it needs with `memory_get`, passing the `search_id`. It skips prompts under `min_words` (3) words and slash commands, truncates the query to 500 characters, shows nothing when the vector search is unavailable or no calibration exists, never shows `tool_output` or non-active memories, and never repeats a memory already preloaded or injected in the session since its last `startup`, `clear`, `compact`, or `fork`. Each injection is logged as an `inject` event with the session id; a prompt holding a secret is logged with a NULL query. The whole hook runs under `deadline_ms` (1.5 s), starting before the database connection, and prints nothing on any failure. Injections, like preload, never count as accesses.

### Relevance calibration

Relevance is `sigmoid(b0 + b1·similarity + b2·fts_hit + b3·trigram_hit + b4/vector_rank)`, where `similarity` is the vector cosine (0 when absent), the hits are 1 when the result was in that candidate list, and the last term is 0 when the result is absent from the vector list. The vector rank term was added after the three-signal fit proved miscalibrated on the evaluation set: five-fold cross-validated log loss fell from 0.50 to 0.26.

- **Default:** fitted from the evaluation set by `make test-eval` and compiled in per embedding model (`embeddinggemma`/768 today). Another model has no default, so recall shows nothing and `doctor` says so.
- **Usage fit:** `memorable calibrate` learns from MCP searches and injections. A shown result fetched with that event's `search_id` is relevant; one not fetched within 24 hours is irrelevant. CLI searches and profile memories are excluded. The fit is regularized toward the default (worth 10 samples) and saved only with at least 20 relevant and 20 irrelevant labels; the newest saved fit for the configured model wins. `--dry-run` prints the fit and a reliability table without saving.

### Embedding worker

Goroutine inside each `serve` process; claims jobs with `SELECT ... FOR UPDATE SKIP LOCKED`; exponential backoff, max 10 attempts; leftovers handled by the next session or `memorable reembed --pending`. Stops cleanly on context cancellation (goleak-verified).

## 8. Security

### Threat model

Primary: memory poisoning and secret leakage. Secondary: context leakage, destructive actions by a misbehaving agent, local network exposure.

### Controls

| Threat | Control |
|---|---|
| Poisoning | Provenance, `unverified` status for tool output, injection scanner with quarantine, quarantined content never sent over MCP, data-labeled output, preload limited to trusted sources, profile writable only as admin (CLI or `ui --admin`), CLAUDE.md rule that memory is data. |
| Secret leakage | Secret scan on write, update, import; reject with rule name. Embeddings treated as sensitive. |
| Context leakage | Schema per context, context fixed per connection pool (`memorable ui` holds one pool per open context and never mixes contexts in a response), no context parameter, fallback context read-only and opt-in. |
| Destructive actions | App role has no DELETE; soft delete; full history; purge only as admin, via CLI or `ui --admin` with typed confirmation. |
| Agent escalating through Docker | **Soft boundary (accepted):** anyone who can run `docker` has superuser access through `docker exec`, and `docker inspect` shows the admin password. Mitigated by Claude Code deny rules, soft deletes, history, and daily backups. |
| Credentials | App password generated by setup, stored in the OS keyring (Secret Service, Keychain); 0600 file fallback with a Read deny rule. Admin password shown once at setup and never stored by memorable. `memorable ui --admin` holds the admin password in process memory for its lifetime; never persisted. |
| Network | stdio for `serve`; `memorable ui` on `127.0.0.1` with token, CSRF, Host, and Origin checks; Postgres and Ollama on `127.0.0.1`; remote embedding providers require `allow_remote = true`. |
| SQL injection | Parameterized queries only; `websearch_to_tsquery`; identifiers via `pgx.Identifier`. |
| Runaway agent | Content 2,000 chars, title 120, 10 tags, batch 20, query 500 chars, k 20, 60 writes per minute per process. |
| Forensics | Append-only audit log with content hashes. |
| Dependencies | Minimal set, pinned; `govulncheck` and `gosec` via `tool` directives and CI. GitHub Actions pinned by commit SHA; Dependabot opens weekly update PRs for Go modules and Actions. |
| Web UI: other local processes | `memorable ui` binds `127.0.0.1` only, on a random free port by default. A per-launch 256-bit single-use token in the launch URL is exchanged for an `HttpOnly`, `SameSite=Strict` session cookie. The browser is opened on a 0600 redirect file in a private 0700 temp directory, so the token never appears in a process argument list. |
| Web UI: cross-site and rebinding | State changes are POST with a per-session CSRF token; cross-origin requests are refused (`Origin` and `Sec-Fetch-Site`); a `Host` other than `127.0.0.1:<port>` or `localhost:<port>` gets 421. |
| Web UI: script injection | Memory content renders only as escaped text. `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`. htmx, app.js, fonts, and Tailwind CSS are embedded: no CDN, no Node at build or runtime. |
| Web UI: agent obtaining admin | `--admin` prompts for the password in the terminal and refuses a non-TTY stdin; no flag, environment variable, or config holds it. The unlock expires after 15 idle minutes (only admin actions count as use), after which the UI is app-role until restarted. Purge requires the typed slug (or id); prune shows a dry-run count first. |
| Web UI: forensics | UI actions, app-role and admin alike, write audit entries with `details.via = "ui"` and `process` as `ui:<pid>@<start>`. Quarantined memories show as counts to the app role; the conflict list can show ids of memories linked by `contradicts`, quarantined ones included, as `memory_review` does. Full quarantined content appears only with `--admin`. |
| Web UI: agent bypassing MCP "Ask" rules | An agent with Bash could run `memorable ui --no-browser`, read the URL from stderr, and delete or restore without the MCP permission prompt. The Bash deny rule `Bash(memorable ui:*)` is part of the recommended permissions and of what `memorable setup` writes. |
| Web UI: **soft boundary (accepted)** | While `memorable ui --admin` runs and the unlock has not expired, whoever holds the session cookie has admin rights in the open contexts. Cookies are not scoped by port, so another web server on 127.0.0.1 that the browser visits during the session receives the session cookie. Sits beside the Docker soft boundary. |
| Tampered update | `memorable update` talks HTTPS to `api.github.com` only and follows at most 3 redirects, each to HTTPS `*.githubusercontent.com` (the token is dropped on the cross-host hop). Size caps on every download (64 KiB signed files, 100 MiB archive, 200 MiB binary) and connection timeouts. The detached GPG signature on `checksums.txt` must verify against the public key embedded at build time (`D4A4DEE111ABA5ACBE1F49773B21B7770B4334A4`) before the archive's sha256 is trusted. Only the top-level `memorable` regular file is extracted, and any absolute or escaping entry fails the archive. Fail closed: the installed binary is untouched until every check passes. |
| Privilege during update | The new binary is written beside the target through `os.Root` and renamed over it. When the directory is root-owned, the verified binary is staged in a private 0700 temp directory and installed with `sudo install -m 0755 <staged> <target>` (separate arguments, no shell); memorable itself never runs under sudo. Homebrew installs are refused. |

### Database roles

| Role | Auth | Privileges |
|---|---|---|
| `memorable_admin` | password (user-held) | superuser in the container; migrations, purge, prune, approve, profile writes |
| `memorable_app` | password (keyring) | SELECT, INSERT, UPDATE on context memory tables; INSERT and SELECT on versions, feedback, access_events, audit_log; SELECT only on `mem_profile`; no DELETE anywhere |

### Injection heuristics (initial set)

Imperatives aimed at the agent ("ignore previous", "you must always", "system prompt"), command patterns (`curl ... | sh`, `rm -rf`, base64 piped to a shell), URLs combined with commands, hidden or zero-width Unicode. False positives are expected; approval is one CLI command (`memorable approve`) or one click in `memorable ui --admin`.

## 9. CLI

### Presentation

- `fang` styles help, usage, errors, and version; `memorable --help` shows grouped commands with examples.
- `lipgloss` tables and panels for lists, search results, stats, doctor checks; `huh` forms and spinners for setup and slow steps.
- Plain text automatically when stdout is not a terminal (including when Claude runs the CLI) or `NO_COLOR` is set. `--output text|json` on every read command.
- Tab completion is static: it never opens the database or the keyring. Enum flag values complete, ids, slugs, and tags complete to nothing, and context names come from the config file.
- Exit codes: 0 success, 1 runtime error, 2 usage error. Logs on stderr; program output on stdout; commands write through `cmd.OutOrStdout()` for testability.

### Commands

| Command | Purpose |
|---|---|
| `memorable setup` | Guided setup (below); `--dry-run` previews everything |
| `memorable serve` | MCP server over stdio |
| `memorable doctor` | Checks config, keyring, container, port, roles, extensions, schema versions, Ollama, model, dimension match |
| `memorable context` / `context add <path> <name>` | Show resolution for the current directory; map a path |
| `memorable migrate` | Apply migrations (admin) |
| `memorable search / get / list / top` | Human versions of the MCP tools; `search` and `list` take `--tag` (all of) and `--any-tag` (at least one) |
| `memorable tags [--all-projects] [--limit N]` | Tags in use with counts |
| `memorable edit <id or slug>` | Edit in `$EDITOR`; rescans and re-embeds |
| `memorable review` | Interactive review queue |
| `memorable approve <id>` | Release from quarantine (admin) |
| `memorable purge <id>` | Irreversible erase across all tables (admin) |
| `memorable profile add/edit/list/delete` | Manage profile memories (admin) |
| `memorable prune` | Apply retention (admin) |
| `memorable reembed [--pending] [--all]` | Process jobs or re-embed after a model change; handles dimension changes |
| `memorable backup` | `pg_dump -Fc` inside the container; keeps the last 14 in `~/.local/share/memorable/backups`. A dump that does not start with the custom-format header is discarded before rotation, so a failed dump never deletes a good one |
| `memorable export [--format json or md]` / `import <path>` | Portable export; import JSON or Claude Code `memory/*.md` files with full scanning |
| `memorable hook session-start` | Reads hook JSON on stdin; prints the preload set and logs it for recall dedup |
| `memorable hook prompt-submit` | Reads UserPromptSubmit hook JSON on stdin; prints calibrated recall pointers (section 7) |
| `memorable calibrate [--dry-run]` | Fit recall relevance to usage; prints label counts, coefficients, and a reliability table |
| `memorable stats` | Counts per status, type, project; embedding coverage |
| `memorable completion bash\|zsh\|fish` | Print the completion script (cobra); setup installs it |
| `memorable version` | Version, commit, build date (ldflags) |
| `memorable ui [--admin] [--context <name>] [--no-browser] [--port <n>]` | Local web UI on loopback: browse, search with score breakdown, and act on memories; `--admin` unlocks approve, purge, prune, and profile writes after a terminal password prompt |
| `memorable update [--check] [--pre] [--force] [--version vX.Y.Z]` | Replace the running binary with the newest verified release (below) |

### `memorable update`

1. **Select.** The newest published release newer than the running version, compared as semver. Pre-releases count when the running version is a pre-release, when `--pre` is passed, or for a `--force`d development build. `--version` pins a tag (and may downgrade). `--check` reports and touches nothing. GitHub's "latest release" endpoint skips pre-releases, so update lists releases itself.
2. **Refuse** development builds (`dev` or any non-semver version) unless `--force`, and any binary under a Homebrew `Cellar` or `Caskroom`.
3. **Token** from `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token` when present, to raise the API rate limit; the public repository needs none.
4. **Verify** as in the security controls, then **replace** atomically (running `serve` processes keep the old inode), or stage and `sudo install`.
5. **Report** old and new versions, and tell the user to restart Claude Code sessions and run `doctor` (and `migrate` if schema versions are behind). `doctor` does not check for updates in v1, to stay offline.

### `memorable setup`

Interactive only (refuses without a TTY). Each step shows what will change, as a diff for file edits, and asks before applying:

1. Check Docker and Ollama are installed (Ollama install stays manual: it needs sudo).
2. Start `memorable-postgres` with generated passwords; show the admin password once; store the app password in the keyring.
3. Create roles, schemas, and run migrations.
4. Pull the embedding model.
5. Define contexts and an optional fallback context; write the config.
6. Register the MCP server: `claude mcp add --scope user memorable -- memorable serve`.
7. Append the CLAUDE.md snippet to `~/.claude/CLAUDE.md`.
8. Add permission rules to `~/.claude/settings.json`, including the Bash deny rule `Bash(memorable ui:*)`.
9. Optional: SessionStart hook.
10. Optional: daily backup via systemd user timer (Linux) or launchd agent (macOS).
11. Optional: tab completion for the login shell (a script file, plus a marked block in `.zshrc`, or in `.bash_profile` for bash on macOS).
12. Run `doctor` and show the result.

## 10. Configuration

Location: `$XDG_CONFIG_HOME/memorable/config.toml` (Linux) or `~/Library/Application Support/memorable/config.toml` (macOS), overridable by `--config` or `MEMORABLE_CONFIG`. Env overrides: `MEMORABLE_CONTEXT`, `MEMORABLE_DB_HOST`, `MEMORABLE_DB_PORT`, `MEMORABLE_DB_NAME`.

```toml
fallback_context = "personal"   # read-only; omit to disable

[database]
host = "127.0.0.1"
port = 5434
name = "memorable"
app_user = "memorable_app"
admin_user = "memorable_admin"
container = "memorable-postgres"

[embedding]
provider = "ollama"
url = "http://127.0.0.1:11434"
model = "embeddinggemma"
dimensions = 768
allow_remote = false

[contexts.personal]
paths = ["~/development/projects", "~/development/OBSIDIAN"]

[search]
min_similarity = 0.30
duplicate_threshold = 0.95
conflict_threshold = 0.75
half_life_days = 30

[preload]
max_items = 10
max_chars = 4000

[limits]
writes_per_minute = 60

[retention]
access_events_days = 90
backups_keep = 14

[recall]
min_relevance = 0.70   # calibrated relevance a memory needs to be shown, (0, 1)
max_items = 3          # 1 to 10
min_words = 3          # shorter prompts are skipped
deadline_ms = 1500     # 200 to 5000
```

## 11. Observability

- slog JSON to stderr only; a test asserts `serve` writes nothing to stdout except protocol frames.
- No metrics, tracing, or pprof endpoints in v1. `serve` opens no listeners; only `memorable ui` listens, on loopback.
- `memorable stats`, `doctor`, and the access_events table cover runtime health and tuning.

## 12. Testing

- **Unit** (default `go test`, no Docker): scope resolution, scanners, RRF and boost math, decay and `rank_key` ordering, validation, config, CLI rendering in TTY and plain modes.
- **Fuzz:** scanners, slug and tag validation, config loading, and both import parsers (JSON export and Markdown front matter).
- **Time:** decay and backoff with `testing/synctest`.
- **Goroutines:** `goleak.VerifyTestMain` in the worker, mcpserver, and webui packages; `goleak.VerifyNone` around the `memorable ui` shutdown test.
- **Panics:** an MCP receiving middleware recovers handler panics, logs the stack, and returns `internal error`, so one bad request cannot end the `serve` session.
- **Integration** (`//go:build integration`): testcontainers with the same pgvector image; every MCP tool, status transitions, grants (app role cannot DELETE or write profile), context isolation, migrations up from empty and from the previous version, `EXPLAIN` index usage on 10,000 seeded rows, session timeouts, and a backup restored into a fresh database.
- **MCP end-to-end:** go-sdk in-memory transport; output wrapping; quarantined content never appears.
- **Search evaluation** (`//go:build eval`): about 50 synthetic memories and 100 queries authored by Claude, real cases added later; runs against real Ollama; reports recall@5 and MRR; compares `embeddinggemma` and `qwen3-embedding:0.6b` before the default is frozen.

## 13. Repository, CI, and releases

- Public GitHub repository `constantine-kostikas/memorable`, MIT licensed.
- Root files: `Makefile`, `.golangci.yml`, `.gitignore`, `CLAUDE.md` (with the always-load directive for the `golang-how-to` skill), `README.md`, `LICENSE`, `install.sh`.
- **CI** (`.github/workflows/ci.yml`, push and PR to main): build, `go test -race`, golangci-lint, govulncheck, gosec, integration tests.
- **Release** (`.github/workflows/release.yml`, on a pushed `v*` tag or the "Run workflow" button, which tags the head of main with the entered version and releases it in the same run): GoReleaser v2, `CGO_ENABLED=0`, archives that include `completions/memorable.{bash,zsh,fish}`, linux and darwin on amd64 and arm64, archive names matching `uname`, GPG-signed checksums (`GPG_PRIVATE_KEY`, `GPG_PASSPHRASE` secrets), GitHub-native release notes grouped by PR label (`.github/release.yml`, labels applied from branch prefixes by `.github/labeler.yml`), version via ldflags. Mirrors GitQuick's setup without its push-to-main trigger.
- **Homebrew tap:** configured in `.goreleaser.yaml` but commented out until the `homebrew-tap` repository and its token exist.
- **Install:** `install.sh` picks the highest semver release tag (pre-releases included, since the list API is not in version order), downloads the archive for `uname -s`/`uname -m`, checks its sha256 against `checksums.txt`, and installs to `~/.local/bin` (`MEMORABLE_INSTALL_DIR`, `MEMORABLE_VERSION` override). GPG verification is left to `memorable update`.
- **Update:** `memorable update` (see section 9).

## 14. Deliverables

- The binary with every command above.
- `README.md`: prerequisites (Docker, Ollama), install, `memorable setup`, manual setup equivalent, CLAUDE.md snippet, permissions, hook, backups and restore, importing existing memory files, troubleshooting with `doctor`.
- `docs/claude-md-snippet.md`.

## 15. Open items

| Item | Resolution |
|---|---|
| Claude Code MCP working directory and roots support | Verify during implementation |
| Whether MCP requests carry a Claude Code session id | Verify; fall back to process id + start time |
| `embeddinggemma` query and document prompt prefixes | 768 dimensions and L2-normalized output verified locally (Ollama 0.40.16); prefixes taken from the model card, effect measured by the evaluation set |
| Gatekeeper behavior for `gh release download` on macOS | Verify on a colleague's Mac before relying on it |
| Threshold defaults | Recall uses calibrated relevance (section 7); the default fit comes from the evaluation set, refined by `memorable calibrate` |
| Whether compaction keeps the Claude Code session id | Not documented; recall dedup is correct either way |
