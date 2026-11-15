# memorable web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Before any Go work, load `samber/cc-skills-golang@golang-how-to` (project CLAUDE.md).

**Goal:** Add `memorable ui`, a loopback web interface to browse, search, and act on memories, with admin actions behind `--admin`.

**Architecture:** A new `internal/webui` package (stdlib `net/http`, `html/template`, htmx, Tailwind CSS) is an adapter over `memory.Service` and a new `internal/adminops` package, exactly like `mcpserver` and the CLI. `cmd/memorable/ui.go` wires one service per opened context, runs the embedding worker, listens on 127.0.0.1, and holds the admin password in memory only with `--admin`. Small additions to `memory`, `rank`, and `store` supply what the UI needs (passive explained search, activity reads, supersede of an existing memory, audit details, prune count).

**Tech Stack:** Go 1.25+, `net/http` (1.22+ method patterns, 1.25 `http.CrossOriginProtection`), `html/template`, `embed`, htmx 2.0.x (vendored), Tailwind CSS v4 standalone CLI, pgx/v5, testify, testcontainers-go, goleak, `testing/synctest`.

**Spec:** `docs/superpowers/specs/2026-11-15-web-ui-design.md` (and `docs/design.md`, binding authority).

## Global Constraints

- `serve` opens no listeners. Only `memorable ui` listens, on `127.0.0.1` only; the bind address is not configurable.
- No Node toolchain anywhere. No CDN or other runtime internet dependency. No router or web framework dependency: stdlib `http.ServeMux` only.
- All templates and assets embedded with `embed.FS`; `go build` needs nothing beyond Go.
- Tailwind: standalone CLI v4, version pinned in the Makefile, downloaded by `make css` and verified against per-platform sha256 values recorded in the Makefile. Generated CSS committed at `internal/webui/static/app.css`; CI regenerates and fails on diff. Templates use only full literal class names (never concatenated), and `input.css` has an `@source` for the templates directory.
- htmx 2.0.x vendored at `internal/webui/static/htmx.min.js`, sha256 recorded in the Makefile; config via `<meta name="htmx-config" content='{"includeIndicatorStyles":false,"allowEval":false}'>`; no `hx-on` attributes, no `js:` values.
- Response headers on every response: `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`.
- `http.Server` sets `ReadHeaderTimeout: 5s`, `ReadTimeout: 30s`, `WriteTimeout: 30s`, `IdleTimeout: 120s` (gosec G112). Graceful shutdown within 5 seconds.
- Memory content is rendered only as escaped text in `<pre class="whitespace-pre-wrap break-words">`; never `template.HTML`, never Markdown rendering.
- Business rules stay in `internal/memory`; admin rules in `internal/adminops`. Handlers only translate HTTP to calls.
- Admin idle expiry: 15 minutes. Purge confirmation: the slug, or the id when there is no slug.
- Audit: UI actions carry `details.via = "ui"`; `process` is `ui:<pid>@<start RFC3339>`.
- Deny rule string: `Bash(memorable ui:*)`.
- slog JSON on stderr only. Errors cross the HTTP boundary as short safe messages (reuse `mcpserver`'s mapping style: sentinel to message), logged once there.
- Commits: conventional style (`feat(webui): ...`), no Claude attribution.

## Review Focus

1. **Admin unlock expires between page load and click.** Approve or Purge after expiry returns a clear "admin locked; restart with `memorable ui --admin`" fragment with 403, never a 500 or a silent no-op. Test in Task 9.
2. **Concurrent edit.** The edit form carries the version it was loaded at; if an agent updated the memory meanwhile, the save shows a conflict message with a reload link and does not overwrite. Test in Task 7.
3. **Edit rejected by a scanner.** A secret in an edit returns the rule name inline and keeps the submitted form; an injection pattern reports that the memory is now quarantined. Test in Task 7.
4. **Ollama down or one context broken.** Lists and detail still render; search falls back to text results with a "vector search unavailable" note; a context that fails to open shows an error page for that context only, and other contexts keep working. Tests in Tasks 6 and 10.
5. **Reopening the launch URL after the token was used.** With a valid cookie it redirects to the context home; without one it shows a 403 page telling the user to restart `memorable ui`. Test in Task 5.

---

### Task 1: Passive, explained, project-filtered search

**Files:**
- Modify: `internal/rank/rank.go` (the file defining `Boost`), `internal/rank/rank_test.go`
- Modify: `internal/memory/search.go`, `internal/memory/read.go` (`ListRequest`), `internal/memory/read_test.go`, `internal/memory/search_test.go` (create if absent; follow existing test style in `read_test.go`)

**Interfaces:**
- Produces:
  - `rank.Parts{Project, Pinned, Usage, Multiplier, Final float64}` and `func BoostParts(rrf float64, projectMatch, pinned bool, effectiveScore float64) Parts`; `Boost` returns `BoostParts(...).Final`.
  - `memory.SearchRequest` gains `Passive bool` (no `LogSearch`, `SearchID` is `uuid.Nil`), `Explain bool`, `Project *string`.
  - `memory.ListRequest` gains `Project *string`.
  - `memory.Explanation{VectorRank, FTSRank, TrigramRank int; Similarity, RRF float64; Textual bool; Boost rank.Parts}` (ranks 1-based, 0 when absent).
  - `memory.SearchResponse` gains `Explain map[uuid.UUID]Explanation` (nil unless `Explain`).
- Semantics of `Project` on both requests: when non-nil it overrides the session project and forces `AllProjects=false`, so results are that project plus context-global (the existing `project IS NULL OR project = $p` filter). Profile behavior unchanged.

- [ ] **Step 1: Write failing tests**
  - `TestBoostPartsMatchesBoost`: for the four `TestBoost` cases, `BoostParts(...).Final == Boost(...)`; `BoostParts(0.5, true, true, 0)` has `Project 0.20`, `Pinned 0.10`, `Multiplier 1.30`.
  - `TestSearchPassiveDoesNotLog`: with `memorytest.Repo`, `Search(Passive: true)` returns `SearchID == uuid.Nil` and the repo records no search event.
  - `TestSearchExplain`: a memory hit only by full text has `FTSRank 1`, `VectorRank 0`, `Textual true`, `RRF == 1.0/61`, and `Explain[id].Boost.Final == Results[0].Score`.
  - `TestSearchProjectFilter` and `TestListProjectFilter`: with memories in projects `a`, `b`, and global, `Project: &"a"` returns `a` and global, never `b`, even when the session project is `b`.
- [ ] **Step 2: Run** `go test -race ./internal/rank/ ./internal/memory/ -run 'BoostParts|SearchPassive|SearchExplain|ProjectFilter'`. Expected: FAIL (undefined fields and functions).
- [ ] **Step 3: Implement.** Build the explanation from the three candidate lists already computed in `Search`; `rankResults` calls `BoostParts`. Do not change MCP output (`mcpserver` ignores `Explain`).
- [ ] **Step 4: Run** the same command plus `go test -race ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(memory): add passive, explained, and project-filtered search`.

### Task 2: Activity reads, supersede existing, audit details

**Files:**
- Modify: `internal/memory/repository.go`, `internal/memory/service.go`, `internal/memory/mutate.go`, `internal/memory/read.go`, `internal/memory/mutate_test.go`, `internal/memory/read_test.go`
- Modify: `internal/memory/memorytest/fakes.go` (keep the `var _ memory.Repository` assertion compiling)
- Create: `internal/store/activity.go`; Modify: `internal/store/memories.go`; Test: `internal/store/activity_integration_test.go`

**Interfaces:**
- Produces:
  - `memory.FeedbackEntry{Kind FeedbackKind; Note string; Project *string; CreatedAt time.Time}`
  - `memory.AuditRecord{Action, Process, Cwd string; Details map[string]any; CreatedAt time.Time}`
  - `memory.Activity{Feedback []FeedbackEntry; Audit []AuditRecord}` (both newest first)
  - `Repository.Activity(ctx context.Context, id uuid.UUID) (Activity, error)`
  - `Repository.MarkSuperseded(ctx context.Context, old, replacement uuid.UUID, a AuditEntry) error`: one transaction; sets `old` to `superseded` with change kind `supersede` and adds link `{From: replacement, To: old, Kind: supersedes}`, same SQL pattern as `InsertSuperseding`.
  - `(*Service) Activity(ctx context.Context, id uuid.UUID) (Activity, error)`
  - `(*Service) Supersede(ctx context.Context, old, replacement uuid.UUID) error`: writable check; both must exist, belong to the owner, differ, and `replacement` must be `active`; `old` must not be quarantined.
  - `memory.Settings` gains `AuditDetails map[string]any`; `(*Service).audit` copies it into `AuditEntry.Details` (nil for MCP and CLI, unchanged behavior).

- [ ] **Step 1: Write failing tests**
  - `TestActivityReturnsFeedbackAndAudit`: after `Feedback(useful, "note")` and `Update`, `Activity` has one feedback entry with note `"note"` and audit actions including `"update"`.
  - `TestSupersedeExisting`: old becomes `StatusSuperseded`; a `supersedes` link from replacement to old exists. `TestSupersedeRejects`: same id, missing id, quarantined old, and non-active replacement each return `ErrValidation` or `ErrNotFound`.
  - `TestAuditDetailsPropagate`: a service with `AuditDetails{"via": "ui"}` writes audits whose `Details["via"] == "ui"`.
  - Integration `TestStoreActivityAndMarkSuperseded`: same assertions against Postgres, and `MarkSuperseded` rolls back fully when the link insert fails (use a nonexistent replacement id).
- [ ] **Step 2: Run** `go test -race ./internal/memory/... -run 'Activity|Supersede|AuditDetails'`. Expected: FAIL.
- [ ] **Step 3: Implement** in memory, the fake (store `FeedbackEntry` values instead of bare kinds; record `AuditRecord`s alongside `Audits`), and the store (`SELECT ... FROM memory_feedback WHERE memory_id = $1 ORDER BY created_at DESC`, same for `audit_log`).
- [ ] **Step 4: Run** `make test && make test-integration`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(memory): read activity, supersede an existing memory, and tag audit entries`.

### Task 3: `internal/adminops` and admin audit details

**Files:**
- Create: `internal/adminops/ops.go`, `internal/adminops/ops_test.go`
- Modify: `internal/store/admin.go` (options, `PruneCount`), `cmd/memorable/profile.go`, `cmd/memorable/admin.go`, `cmd/memorable/maintain.go`
- Test: `internal/store/admin_integration_test.go` (extend existing admin integration tests)

**Interfaces:**
- Produces:
  - `store.AdminOption`; `store.WithAuditDetails(d map[string]any) AdminOption`; `store.OpenAdmin(ctx, cfg, password, schema, owner string, opts ...AdminOption)` (variadic, existing callers unchanged). Every audit entry `Admin` writes carries the details.
  - `(*store.Admin) PruneCount(ctx context.Context, before time.Time) (int64, error)`
  - `adminops.Admin` interface (consumer-side, satisfied by `*store.Admin`): `QuarantinedList`, `Approve`, `Purge`, `PruneCount`, `Prune`, `ProfileInsert`, `ProfileUpdate`, `ProfileDelete`, `ProfileList`, with the existing `store.Admin` signatures.
  - `adminops.ProfileInput{Title, Content string; Tags []string; Type memory.Type; Pinned bool}`
  - `adminops.New(a Admin, emb memory.Embedder, sec memory.SecretScanner, retentionDays int, now func() time.Time) *Ops` (`emb` may be nil: no embedding, text search still works)
  - `*Ops` methods: `QuarantinedList(ctx) ([]memory.Memory, error)`, `Approve(ctx, id) error`, `Purge(ctx, id) error`, `PruneCount(ctx) (int64, error)`, `Prune(ctx) (int64, error)`, `ProfileList(ctx) ([]memory.Memory, error)`, `ProfileAdd(ctx, ProfileInput) (memory.Memory, error)`, `ProfileEdit(ctx, uuid.UUID, ProfileInput) (memory.Memory, error)`, `ProfileDelete(ctx, uuid.UUID) error`.
- `ProfileAdd` and `ProfileEdit` move the logic of `profileEmbedding` and `scanProfileText` out of `cmd/memorable/profile.go` unchanged in behavior (secret scan over title, content, and tags; reject with `ErrSecretDetected` and the rule name; embed `memory.DocumentText`, ignore embed failures). The CLI profile, approve, purge, and prune commands call `Ops`.

- [ ] **Step 1: Write failing tests** with a fake `Admin` in `ops_test.go`: `TestProfileAddRejectsSecret` (no insert called, error wraps `ErrSecretDetected`), `TestProfileAddWithoutEmbedder` (insert with nil vector), `TestPruneUsesRetention` (with `retentionDays 90` and fixed `now`, `before == now - 90 days`), integration `TestAdminPruneCountAndAuditDetails` (count equals rows `Prune` then deletes; approve audit has `details.via`).
- [ ] **Step 2: Run** `go test -race ./internal/adminops/`. Expected: FAIL.
- [ ] **Step 3: Implement**, then refactor the CLI commands to use `Ops`.
- [ ] **Step 4: Run** `make test && make test-integration && make lint`. Expected: PASS; existing CLI admin tests unchanged and passing.
- [ ] **Step 5: Commit** `refactor(admin): move admin operations into adminops and tag admin audits`.

### Task 4: Asset pipeline and page skeleton

**Files:**
- Create: `internal/webui/assets.go` (`//go:embed templates static`), `internal/webui/templates/layout.html`, `internal/webui/templates/error.html`, `internal/webui/input.css`, `internal/webui/static/app.css` (generated), `internal/webui/static/htmx.min.js` (vendored)
- Modify: `Makefile` (targets `css`, `css-check`, `htmx-check`), `.gitignore` (`bin/tailwindcss`), `.github/workflows/ci.yml` (lint job runs `make css-check htmx-check`)
- Test: `internal/webui/assets_test.go`

**Interfaces:**
- Produces: `webui.templates` parsed once at package init time is forbidden (no `init()`); expose `func parseTemplates() (*template.Template, error)` used by `New` (Task 5). Layout defines blocks `title` and `content`, renders the context picker, nav (Memories, Review with count, Profile, Stats), `<meta name="csrf-token">` placeholder, the htmx-config meta, and `<body hx-headers='{"X-CSRF-Token":"{{.CSRF}}"}'>`.
- Makefile: `TAILWIND_VERSION`, `TAILWIND_SHA256_linux_x64`, `TAILWIND_SHA256_linux_arm64`, `TAILWIND_SHA256_macos_x64`, `TAILWIND_SHA256_macos_arm64`, `HTMX_SHA256`. `make css` downloads `bin/tailwindcss` if missing, verifies sha256, runs `bin/tailwindcss -i internal/webui/input.css -o internal/webui/static/app.css --minify`. `css-check` runs `css` then `git diff --exit-code internal/webui/static/app.css`.

- [ ] **Step 1: Write failing test** `TestEmbeddedAssets`: `static/app.css` and `static/htmx.min.js` exist and are non-empty; `parseTemplates()` succeeds; rendering `layout.html` with a hostile title `<script>x</script>` produces escaped output.
- [ ] **Step 2: Run** `go test -race ./internal/webui/`. Expected: FAIL.
- [ ] **Step 3: Implement**: pin the latest Tailwind v4 and htmx 2.0.x releases at implementation time and record their sha256 values; run `make css`.
- [ ] **Step 4: Run** `go test -race ./internal/webui/ && make css-check htmx-check`. Expected: PASS, no diff.
- [ ] **Step 5: Commit** `build(webui): embed templates, vendored htmx, and generated Tailwind CSS`.

### Task 5: Server, session, and security middleware

**Files:**
- Create: `internal/webui/server.go`, `internal/webui/security.go`, `internal/webui/session.go`
- Test: `internal/webui/security_test.go`, `internal/webui/main_test.go` (`goleak.VerifyTestMain`)

**Interfaces:**
- Produces:
  - `webui.Memories` interface (consumer-side, satisfied by `*memory.Service`): `List`, `Search`, `Get`, `History`, `Activity`, `Update`, `Delete`, `Restore`, `Feedback`, `Link`, `Supersede`, `Activate`, `Review`, with `memory.Service` signatures.
  - `webui.StoreReads` interface (satisfied by `*store.Store`): `Stats(ctx context.Context) (store.Stats, error)` and `ProfileList(ctx context.Context) ([]memory.Memory, error)` (the latter added in Task 8).
  - `webui.AdminOps` interface: the `*adminops.Ops` method set from Task 3.
  - `webui.Backend{Memories Memories; Store StoreReads}`
  - `webui.Config{Contexts []string; Initial string; Backend func(ctx context.Context, name string) (Backend, error); Admin func(ctx context.Context, name string) (AdminOps, error) /* nil without --admin */; AdminIdle time.Duration; Now func() time.Time; Logger *slog.Logger}`
  - `webui.New(cfg Config) (*Server, error)`; `(*Server) Handler(port int) http.Handler`; `(*Server) LaunchURL(port int) string` (`http://127.0.0.1:<port>/?token=<t>`); `(*Server) Close()`.
- Routes (all under `/c/{context}/...` so the context is explicit per request; unknown context is 404): `GET /` (token exchange, then redirect to `/c/<initial>/memories`), `GET /static/{file}`. Later tasks add their routes through `func (s *Server) routes(mux *http.ServeMux)`.
- Token: 32 bytes from `crypto/rand`, base64url; single use, compared in constant time. Session cookie `memorable_session` (`HttpOnly`, `SameSite=Strict`, `Path=/`), value 32 random bytes; one CSRF token per session, 32 random bytes.
- Middleware order: security headers, Host check (`127.0.0.1:<port>` or `localhost:<port>`, else 421), session check (every route except `GET /?token=` and `/static/`; missing or bad cookie gives 403 page "restart memorable ui to get a new link"), `http.NewCrossOriginProtection()` on all non-GET/HEAD, CSRF check on POST (`X-CSRF-Token` header or `csrf` form field, constant-time), method enforcement via ServeMux patterns (GET on a POST route is 405).

- [ ] **Step 1: Write failing tests** (`httptest`, fake `Backend`):
  - `TestTokenExchange`: first `GET /?token=<t>` sets the cookie and 303s to `/c/personal/memories`; second use of the same token without a cookie is 403; with the cookie it 303s to the context home (Review Focus 5).
  - `TestRejectsBadHost` (`Host: evil.example:port` gives 421), `TestRejectsMissingSession` (403), `TestRejectsCrossOriginPost` (`Origin: http://evil.example`, `Sec-Fetch-Site: cross-site` gives 403), `TestRejectsMissingCSRF` (403), `TestRejectsGetOnWriteRoute` (405), `TestSecurityHeaders` (exact CSP string from Global Constraints on every response, including errors), `TestUnknownContext404`.
- [ ] **Step 2: Run** `go test -race ./internal/webui/ -run 'Token|Rejects|SecurityHeaders|UnknownContext'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(webui): add server with token session, CSRF, origin, and host checks`.

### Task 6: Memories list and search

**Files:**
- Create: `internal/webui/memories.go`, `internal/webui/templates/memories.html`, `internal/webui/templates/memory_rows.html`
- Test: `internal/webui/memories_test.go`

**Interfaces:**
- Consumes: `Memories.List`, `Memories.Search` (Task 1 fields), `StoreReads.Stats` (`ByProject` keys fill the project filter).
- Routes: `GET /c/{context}/memories?q=&project=&type=&status=&tag=&cursor=`. With `HX-Request`, returns only `memory_rows.html`; otherwise the full page. No `q`: `List` with `AllProjects: true` unless `project` is set, default statuses `active`, page size 50, "next" link from `ListResponse.Next`. With `q`: `Search{Passive: true, Explain: true, K: 20, AllProjects: true unless project set}`; each row expands to show the explanation fields.
- Search box: `hx-get` with `hx-trigger="input changed delay:300ms, search"` targeting the rows container.

- [ ] **Step 1: Write failing tests:** `TestListPage` (rows show title, project or `(global)`, status, score), `TestListFiltersPassed` (query params reach `ListRequest`), `TestSearchUsesPassiveExplain` (fake records `Passive && Explain`), `TestSearchFragmentForHtmx` (no `<html>` in body), `TestSearchVectorUnavailableNote` (fake returns results with every `VectorRank == 0` and a flag in the response: render "vector search unavailable" note; add `VectorUnavailable bool` to `memory.SearchResponse` in this task, set when embedding or the vector query failed) (Review Focus 4), `TestHostileContentEscaped` (title and snippet `<img src=x onerror=alert(1)>` appear escaped).
- [ ] **Step 2: Run** `go test -race ./internal/webui/ -run 'ListPage|ListFilters|Search|Hostile'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same command and `go test -race ./internal/memory/`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(webui): list, filter, and search memories with ranking explanations`.

### Task 7: Memory detail and app-role actions

**Files:**
- Create: `internal/webui/detail.go`, `internal/webui/actions.go`, `internal/webui/templates/detail.html`, `internal/webui/templates/edit_form.html`
- Test: `internal/webui/detail_test.go`, `internal/webui/actions_test.go`

**Interfaces:**
- Consumes: `Get{IDs, Passive: true}`, `History`, `Activity`, and every mutation on `Memories`.
- Routes: `GET /c/{context}/memories/{id}` (tabs: content, versions, links, feedback, audit; embedding state from `EmbeddingModel` and version), `GET /c/{context}/memories/{id}/edit`, and POST routes `/c/{context}/memories/{id}/{update,feedback,delete,restore,activate,link,unlink,supersede}`.
  - `update`: fields `title`, `content`, `tags` (comma-separated), `type`, `pinned`, `expires_at` (empty clears), `version` (sent as `expected`).
  - `feedback`: `kind` (`useful|wrong|outdated`), `note`. `restore`: optional `version`. `link`/`unlink`: `to` (id or slug), `kind`. `supersede`: `replacement` (id or slug).
  - Each POST returns the re-rendered detail fragment, or an inline error fragment with status 409 (conflict), 422 (validation, secret), or 404.
- Error messages: `ErrConflict` gives "This memory changed since you opened it." plus a reload link; `ErrSecretDetected` gives "Rejected: looks like a secret (<rule>)."; an update that quarantines gives "Saved, but quarantined: it needs approval (memorable approve or ui --admin)."

- [ ] **Step 1: Write failing tests:** `TestDetailTabs` (versions, links, feedback, audit rendered from the fake), `TestDetailIsPassive` (fake records `Passive: true`), `TestDetailQuarantinedHidden` (Get returns `ErrQuarantined`: page shows status only, no content), one test per action asserting the call and arguments the fake received, `TestUpdateConflict` (fake returns `ErrConflict`: 409 with the message, Review Focus 2), `TestUpdateSecretKeepsForm` (422, submitted text still in the form, Review Focus 3), `TestUpdateQuarantinedMessage`, `TestSupersedeBySlug`.
- [ ] **Step 2: Run** `go test -race ./internal/webui/ -run 'Detail|Update|Feedback|Delete|Restore|Activate|Link|Supersede'`. Expected: FAIL.
- [ ] **Step 3: Implement.** Resolve slugs through `Get{Slugs, Passive: true}`.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(webui): show memory detail and run app-role actions`.

### Task 8: Review and stats pages

**Files:**
- Create: `internal/webui/review.go`, `internal/webui/stats.go`, `internal/webui/profile.go`, `internal/webui/templates/review.html`, `internal/webui/templates/stats.html`, `internal/webui/templates/profile.html`
- Modify: `internal/store/admin.go` or a new `internal/store/profile.go`: add `(*Store) ProfileList(ctx) ([]memory.Memory, error)` for the app role (same query as `Admin.ProfileList`; the app role has SELECT on `mem_profile`)
- Test: `internal/webui/review_test.go`, extend the store integration tests

**Interfaces:**
- Routes: `GET /c/{context}/profile` (read-only list for everyone; Task 9 adds the forms when admin is unlocked), `GET /c/{context}/review` (sections flagged, expired, stale, unverified, conflicts, each linking to detail; quarantined: count only unless admin is unlocked, then Task 9 fills the list), `GET /c/{context}/stats` (totals, embedded, pending jobs, by status, type, project).
- The nav Review count is `len(flagged)+len(expired)+len(stale)+len(unverified)+len(conflicts)+QuarantinedCount`.

- [ ] **Step 1: Write failing tests:** `TestReviewSections`, `TestReviewQuarantinedCountOnly` (no admin: count shown, no titles or content), `TestStatsPage`, `TestProfileReadOnly` (list rendered, no forms), integration `TestStoreProfileListAppRole`.
- [ ] **Step 2: Run** `go test -race ./internal/webui/ -run 'Review|Stats|Profile'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(webui): add review queue and stats pages`.

### Task 9: Admin unlock and admin actions

**Files:**
- Create: `internal/webui/admin.go`, `internal/webui/templates/quarantined.html`
- Modify: `internal/webui/profile.go`, `internal/webui/templates/profile.html`
- Test: `internal/webui/admin_test.go`

**Interfaces:**
- Consumes: `Config.Admin`, `Config.AdminIdle`, `Config.Now`, `AdminOps`.
- `adminLock`: caches one `AdminOps` per context; every admin request checks `now - lastUse < AdminIdle`, then updates `lastUse`. On expiry it drops the cached ops and sets the factory to nil (the closure holding the password becomes unreachable); there is no re-unlock without restart. `(*Server).Close` releases it.
- Routes (registered only when `Config.Admin != nil`; otherwise 404): `GET /c/{context}/review` gains the quarantined list with full content labeled "quarantined, untrusted"; POST `/c/{context}/memories/{id}/approve`, POST `/c/{context}/memories/{id}/purge` (field `confirm` must equal the slug or, without a slug, the id; else 422), `GET /c/{context}/prune` (shows `PruneCount`), POST `/c/{context}/prune`, the profile page gains add, edit, and delete forms, POST `/c/{context}/profile`, POST `/c/{context}/profile/{id}/update`, POST `/c/{context}/profile/{id}/delete`.
- An "Admin unlocked, locks in N min" badge in the nav when unlocked.

- [ ] **Step 1: Write failing tests:** `TestAdminRoutesAbsentWithoutAdmin` (404 for every admin route), `TestApprove`, `TestPurgeRequiresConfirm` (wrong confirm: 422, `Purge` not called; correct slug: called), `TestPruneShowsCount`, `TestProfileAddSecretRejected`, `TestAdminIdleExpiry` using `testing/synctest`: unlock, advance 15 minutes, POST approve gives 403 with "admin locked; restart with memorable ui --admin" (Review Focus 1) and the factory is not called again.
- [ ] **Step 2: Run** `go test -race ./internal/webui/ -run 'Admin|Approve|Purge|Prune|Profile'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/webui/`. Expected: PASS, goleak clean.
- [ ] **Step 5: Commit** `feat(webui): unlock admin actions with --admin and expire after idle`.

### Task 10: `memorable ui` command

**Files:**
- Create: `cmd/memorable/ui.go`, `cmd/memorable/ui_test.go`
- Modify: `cmd/memorable/app.go` (`buildAppFor`, `deps.openBrowser`), `cmd/memorable/root.go` (register `newUICmd`)

**Interfaces:**
- Consumes: `webui.New`, `webui.Config`, `adminops.New`, `store.OpenAdmin(..., store.WithAuditDetails(...))`, `promptAdminPassword`, `worker.New`.
- Produces:
  - `func buildAppFor(ctx context.Context, d deps, cfgPath, contextName string, logger *slog.Logger, details map[string]any) (*app, error)`: like `buildApp` but with `scope.Resolution{Context: contextName}` (read-write, no project) and `Settings.AuditDetails = details`, `Settings.Process = "ui:<pid>@<start>"`. Shares code with `buildApp` instead of duplicating it.
  - `deps.openBrowser func(path string) error`: runs `xdg-open` on Linux, `open` on macOS, through `d.run`.
  - `newUICmd(d deps, flags *rootFlags) *cobra.Command`: `memorable ui [--admin] [--context <name>] [--no-browser] [--port <n>]`, group `groupMemory`.
- Behavior:
  - `--admin` prompts first via `d.adminPassword(cmd)` (refuses non-TTY), then verifies the password by opening and pinging one admin connection; the admin factory opens `store.OpenAdmin` per context with `WithAuditDetails({"via":"ui"})` and wraps it in `adminops.New`.
  - Backends are opened lazily per context and cached; each starts `RequeueMissing` and a `worker.New(...).Run(ctx)` goroutine; a failed open is returned as an error for that context only and retried on the next request.
  - Listen `net.Listen("tcp", "127.0.0.1:<port or 0>")`; `http.Server` with the timeouts from Global Constraints.
  - Browser: write `<meta http-equiv="refresh" content="0;url=<launch URL>">` to `launch.html` (0600) in an `os.MkdirTemp` directory (0700), open the file path; remove the directory on shutdown. Print the launch URL to stderr in every case.
  - SIGINT or SIGTERM: `Shutdown` with 5-second timeout, cancel workers, wait, close apps, `Server.Close()`.

- [ ] **Step 1: Write failing tests** with fake `deps`: `TestUIRefusesAdminWithoutTTY`, `TestUIBindsLoopbackOnly` (listener address host is `127.0.0.1`), `TestUILaunchFileNotInArgv` (the fake `openBrowser` receives a file path, never a string containing `token=`; the file mode is 0600 and contains the URL), `TestUIContextFailureIsolated` (backend factory failing for `work` still serves `personal`, Review Focus 4), `TestUIShutdown` (cancel the command context: returns within 5 seconds, temp dir removed, goleak clean).
- [ ] **Step 2: Run** `go test -race ./cmd/memorable/ -run UI`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test && make lint`. Expected: PASS, no gosec findings.
- [ ] **Step 5: Commit** `feat(cli): add memorable ui`.

### Task 11: Integration and listener guards

**Files:**
- Create: `cmd/memorable/ui_integration_test.go`, `cmd/memorable/listener_guard_test.go`

**Interfaces:**
- Consumes: everything above, the existing testcontainers setup used by `serve_integration_test.go`.

- [ ] **Step 1: Write tests:**
  - `TestUIIntegration` (`//go:build integration`): seed two contexts; exchange the token; list, search, edit (version increments, audit has `via: ui`), feedback, supersede; switching to the second context never shows the first context's memories; an app-role backend calling `Approve` through any route is impossible without `--admin` (404).
  - `TestUIAdminIntegration`: admin factory with the real admin password: approve a quarantined memory, purge with confirmation; audit rows carry `via: ui`.
  - `TestServeHasNoListener`: `go list -deps ./internal/mcpserver` output does not contain `internal/webui`; scanning `cmd/memorable/*.go` and `internal/**/*.go` (excluding tests) for `net.Listen` and `http.Server{` finds them only in `cmd/memorable/ui.go` and `internal/webui/`.
- [ ] **Step 2: Run** `make test-integration` and `go test -race ./cmd/memorable/ -run ServeHasNoListener`. Expected: PASS (these tests guard behavior built in earlier tasks; if one fails, fix the owning task's code).
- [ ] **Step 3: Commit** `test(webui): cover the ui end to end and guard serve against listeners`.

### Task 12: Permissions and documentation

**Files:**
- Modify: `internal/claudecfg/claudecfg.go` (`Rules`), `internal/claudecfg/claudecfg_test.go`, `docs/design.md`, `README.md`, `docs/superpowers/specs/2026-11-15-web-ui-design.md` (status: implemented)

- [ ] **Step 1: Write failing test** `TestRulesDenyUI`: `Rules(...)` deny contains `Bash(memorable ui:*)`.
- [ ] **Step 2: Run** `go test -race ./internal/claudecfg/`. Expected: FAIL.
- [ ] **Step 3: Implement** the rule; apply every amendment listed in spec section 9 to `docs/design.md`; add a README section "Web UI" covering `memorable ui`, `--admin`, the deny rule, and the security model in two short paragraphs.
- [ ] **Step 4: Run** `make test && make lint`. Expected: PASS.
- [ ] **Step 5: Commit** `docs: document memorable ui and deny agents from launching it`.
