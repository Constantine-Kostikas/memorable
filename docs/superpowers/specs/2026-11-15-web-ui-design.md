# memorable web UI Design

Status: implemented
Date: 2026-11-15
Amends: `docs/design.md` (binding authority); the amendments are listed in section 9 and land with the implementation.

## 1. Intent

memorable is pre-alpha, and the author cannot currently see what it stores without stitching together `search`, `list`, `get`, `review`, and `stats`. The web UI gives a human one place to see every memory in a context and act on it. It is a permanent feature, not an alpha debugging aid: it ships to colleagues and must survive into v2 (multi-user), where the launch token will be replaced by per-user auth.

The UI serves the human. It does not change the agent path (MCP over stdio).

## 2. Goals, non-goals, anti-goals

### Goals

- Browse, filter, and search all memories of one context at a time, across all of its projects plus context-global and profile.
- Memory detail: full content, metadata, status, embedding state, versions, links, feedback, and audit entries.
- Search that explains its ranking: per-list ranks (vector, full text, trigram), cosine similarity, RRF score, each boost, and why an item passed the relevance cutoff.
- Every memory action available in the CLI, from the browser:
  - App role (always): edit, flag via feedback (`useful`, `wrong`, `outdated`), soft delete, restore (undelete or prior version), link and unlink, supersede.
  - Admin role (only with `--admin`): approve quarantined, purge, prune, profile add, edit, and delete.
- A review queue view matching `memory_review`, plus the quarantined list with full content when `--admin` is unlocked.
- A stats view matching `memorable stats`.

### Non-goals

- Operational commands stay CLI-only: `setup`, `migrate`, `doctor`, `backup`, `reembed`, `export`, `import`, `update`, `context add`.
- Cross-context views. One context is open at a time.
- Remote access, TLS, or binding to anything except loopback (v1).
- Per-user accounts (v2).
- A JSON API for third-party clients.

### Anti-goals (failure even if it works)

- Any change that lets `serve` open a listener.
- Any path by which an agent can obtain admin rights without a human typing the password in a terminal.
- Mixing contexts in one page, query, or response.
- A runtime dependency on the internet (CDN scripts, fonts, or styles).
- A Node toolchain anywhere in build, CI, or release.

## 3. Constraints

- Single Go binary; all templates and assets embedded with `embed.FS`.
- Business rules stay in `internal/memory`; the HTTP layer is an adapter like `mcpserver` and the CLI. Admin operations go through `internal/store.Admin` exactly as the CLI admin commands do.
- Same database roles as today. No new grants for the app role.
- Writes from the UI go through the same validation, secret scan, injection scan, re-embedding, versioning, and audit as MCP and CLI writes.
- Logs: slog JSON on stderr; the terminal shows the URL on stderr.
- Linux and macOS.

## 4. Approaches considered

| Approach | Trade-offs | Outcome |
|---|---|---|
| Go `html/template` + htmx, assets embedded | One binary, no Node, minimal pinned dependencies, templates familiar from PHP (Blade, Twig). Rich client-side interactions are harder. | **Chosen** by the author, with Tailwind for styling. |
| SPA (Svelte or React via Vite) + JSON API | Richest UI; API reusable in v2. Adds Node to CI and release, an npm tree to audit, a second language. | Rejected: conflicts with the minimal-dependency rule; nothing in scope needs it. |
| Terminal UI (bubbletea), no listener | No new attack surface. Not a web UI; weak for graphs and long content. | Rejected: the author wants a browser UI. |

Admin handling was a separate decision:

| Option | Outcome |
|---|---|
| App-role actions only; admin via CLI | Initially chosen, then replaced. |
| `--admin` unlock at launch, password prompted in the terminal | **Chosen** by the author. |
| Admin password per action, typed in the browser | Rejected: the password would cross HTTP and break "read only from a terminal". |

## 5. Decided design

### Command

`memorable ui [--admin] [--context <name>] [--no-browser] [--port <n>]`

- Binds `127.0.0.1` only. The bind address is not configurable in v1.
- Port: random free port by default; `--port` for a fixed one.
- Prints the launch URL to stderr and opens the browser unless `--no-browser`. The browser is never given the token URL as a command-line argument (arguments are world-readable through `/proc/<pid>/cmdline`): `memorable ui` writes a 0600 HTML redirect file into a private 0700 temp directory and opens that file path with `xdg-open` (Linux) or `open` (macOS), as Jupyter does. The file is deleted on shutdown.
- Runs until Ctrl-C or SIGTERM; shuts down gracefully (in-flight requests finish within 5 seconds, pools closed, embedding worker stopped).
- Initial context: `--context`, else resolved from the working directory as today, else the first configured context. The UI can switch contexts afterwards.
- Runs the embedding worker in-process like `serve`, so UI edits get embedded.

### Styling

Tailwind CSS generated by the Tailwind standalone CLI binary (author decision). The binary's version is pinned in the Makefile and downloaded with checksum verification by `make css`. The generated CSS is committed and embedded, so `go build` needs nothing extra. CI regenerates it and fails on a diff. htmx is vendored as one pinned file.

### Pages

```
[personal v]  [search.............................]  Memories | Review (3) | Profile | Stats
------------------------------------------------------------------------------------------
Filters: project [all v] type [all v] status [active v] tags [...]
TYPE     TITLE                          PROJECT    STATUS   SCORE   UPDATED
project  v2 multi-user scope            memorable  active   2.41    2026-11-14
user     Go experience                  (global)   active   1.10    2026-11-12
------------------------------------------------------------------------------------------
Detail: content | versions | links | feedback | audit        [Edit] [Feedback] [Delete] ...
```

- **Memories:** paginated list with filters (project, type, status, tags), sorted by updated time or score. Search box swaps in ranked results with an expandable score breakdown per row.
- **Detail:** content, metadata, and tabs for versions (with diff to the previous version and restore), links (add, remove, navigate), feedback, and audit entries. Supersede starts from a memory and picks the replacement.
- **Review:** flagged, expired, stale, unverified, conflict links, matching `memory_review`. Quarantined items (my call): without `--admin`, the count only, as `memory_review` does today; with `--admin`, full content via `store.Admin.QuarantinedList`, marked as quarantined and untrusted, with Approve and Purge. This keeps quarantined content behind the admin role and adds no app-role read path.
- **Profile:** list; add, edit, delete only with `--admin`.
- **Stats:** counts per status, type, project; embedding coverage; pending jobs.

Content is always rendered as escaped text (html/template auto-escaping), never as HTML or Markdown-to-HTML, because memory content is untrusted.

### Security controls

Author decisions: loopback only, `--admin` with the password prompted in the terminal. Everything else in this table, plus the random default port and the 5-second shutdown, is my call and open to change.

| Threat | Control |
|---|---|
| Other local processes or users | Per-launch 256-bit random token in the launch URL; exchanged on first request for an `HttpOnly`, `SameSite=Strict` session cookie, then the token is invalidated and the browser redirected to a token-free URL. |
| Cross-site request forgery | Every state-changing request is POST with a per-session CSRF token (htmx sends it as a header); `Origin` must match the server origin. |
| DNS rebinding | `Host` must be `127.0.0.1:<port>` or `localhost:<port>`; anything else gets 421. |
| Clickjacking, script injection | `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'` (no inline scripts), `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`. |
| Agent obtaining admin | `--admin` reuses `promptAdminPassword`, which refuses a non-TTY stdin. No flag, env var, or config holds the password. |
| Long-lived admin session | Admin unlock expires after 15 idle minutes; afterwards the UI is app-role until restarted with `--admin`. |
| Destructive admin actions | Purge requires typing the memory's slug (or id when it has no slug). Prune shows a dry-run count first. |
| Forensics | UI actions write audit entries like the CLI, with `process` identifying `memorable ui` and `details` carrying `"via": "ui"`. |
| Agent bypassing MCP "Ask" rules | An agent with Bash could run `memorable ui --no-browser`, read the URL from stderr, and soft delete or restore without the MCP permission prompt. Add the Bash deny rule `Bash(memorable ui:*)` to the recommended permissions and to what `memorable setup` writes into `settings.json`. |

**Accepted soft boundary:** while `memorable ui --admin` runs and the unlock has not expired, whoever holds the session cookie has admin rights in the open contexts. The admin password is held in process memory for the session so admin pools can be opened per context; it is never written to disk, config, or logs. This sits beside the existing Docker soft boundary. Cookies are not scoped by port, so any other web server on 127.0.0.1 that the browser visits during the session receives the session cookie; accepted for v1.

### htmx under the CSP

The CSP forbids inline scripts and styles, so: set htmx `includeIndicatorStyles` to `false` (indicator styles come from Tailwind), use no `hx-on` attributes, and load htmx config from a `<meta name="htmx-config">` tag. Verify early with a browser smoke test.

### Contexts in the UI

- One context per page. Switching contexts opens that context's app pool (and admin pool, if unlocked) with `search_path` set as today; nothing is cached across contexts in a way that can leak into another context's response.
- Every configured context opens read-write in the UI, including the one named as `fallback_context`: read-only fallback exists for unmatched working directories, and a context picked explicitly in the UI is not a fallback.

### Package layout

- `internal/webui/`: HTTP handlers, middleware (token, cookie, CSRF, Host, Origin, headers), templates, embedded assets.
- `cmd/memorable/ui.go`: flags and wiring only.
- New service capability for the ranking breakdown (for example a `Search` option that returns per-candidate components). The exact shape is left to the builder.

## 6. Testing

- Unit (no Docker): every middleware control (missing or wrong token, replayed token, bad Host, bad Origin, missing CSRF, GET on a write route), template escaping of hostile content, admin routes hidden and refused without `--admin`, idle expiry with `testing/synctest`, purge confirmation.
- Handler tests with `httptest` against `memorytest` fakes for every page and action.
- Integration (`//go:build integration`): the UI against the real Postgres container, including context switching isolation and app-role refusal of admin operations.
- `goleak` in `internal/webui`.
- A test that `serve` still opens no listener.

## 7. Documentation

- README section: `memorable ui`, `--admin`, the security model in two short paragraphs.
- `docs/design.md` amendments (section 9).

## 8. Left to the builder

- Route layout, template structure, and htmx interaction details.
- Pagination size and sorting defaults.
- Version diff rendering.
- How the score breakdown is surfaced from `memory.Service` without changing MCP output.
- The exact Tailwind CLI version and the visual design within the constraints above.

## 9. Amendments to `docs/design.md`

- Section 1 non-goals: remove "A web UI"; replace "a local stdio tool must not open listeners" with "`serve` opens no listeners; only `memorable ui` listens, on loopback".
- Section 2: add `memorable ui` to the architecture diagram.
- Section 2 package layout: add `internal/webui/`.
- Section 4 Profile schema: "writable only through the CLI" becomes "writable only through the CLI or `memorable ui --admin`, both as the admin role".
- Section 6 Recommended permissions: add Bash deny `Bash(memorable ui:*)`.
- Section 8 controls:
  - Network: "stdio for `serve`; `memorable ui` on 127.0.0.1 with token, CSRF, Host, and Origin checks".
  - Context leakage: "context fixed per process" becomes "context fixed per connection pool; `memorable ui` holds one pool per open context and never mixes contexts in a response".
  - Poisoning: "profile writable only via CLI" becomes "profile writable only as admin (CLI or `ui --admin`)".
  - Destructive actions: "purge only via CLI as admin" becomes "purge only as admin, via CLI or `ui --admin` with typed confirmation".
  - Credentials: add "`memorable ui --admin` holds the admin password in process memory for its lifetime; never persisted".
  - Add the web UI rows and the accepted soft boundary from section 5.
- Section 9 commands: add `memorable ui [--admin]`; `memorable setup` adds the `Bash(memorable ui:*)` deny rule.
- Section 11: update "no listeners" accordingly.
