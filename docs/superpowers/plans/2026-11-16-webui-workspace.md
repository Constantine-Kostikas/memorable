# memorable web UI workspace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Before any Go work, load `samber/cc-skills-golang@golang-how-to` (project CLAUDE.md).

**Goal:** Turn `memorable ui` into a two-pane, always-editable workspace with the "Index cards" visual identity, keyboard navigation, and practical inputs.

**Architecture:** Server stays Go `html/template` + htmx; the list and the selected memory render in one shell, with the detail pane swapped by htmx and reflected in the URL. All client behavior is progressive enhancement in one hand-written embedded `static/app.js`. One new read endpoint (`/lookup`) and one new `memory.Summary` field (`UpdatedAt`) are the only non-template server changes besides routing.

**Tech Stack:** Go 1.25+, `net/http`, `html/template`, `embed`, htmx 2.0.10 (vendored), Tailwind CSS v4.3.3 standalone CLI, plain ES2020, testify, `httptest`.

**Spec:** `docs/superpowers/specs/2026-11-16-webui-workspace-design.md` (and `docs/superpowers/specs/2026-11-15-web-ui-design.md`, `docs/design.md`).

## Global Constraints

- CSP unchanged: `default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`. No `style=""` attributes, no inline `<script>` bodies, no `hx-on`, no `js:` values, `allowEval:false` stays.
- No Node, no JS framework, no new Go dependencies, no CDN or runtime internet access.
- Memory content renders only as escaped text; never `template.HTML`, never Markdown.
- Admin gating, CSRF, Host and Origin checks, and the typed purge confirmation are unchanged.
- Without JS, list, detail, and Save work as plain links and form posts.
- Fonts: Atkinson Hyperlegible regular and bold woff2, SIL OFL 1.1, license shipped beside them, sha256 pinned in the Makefile; served as `font/woff2`.
- Tokens, type colors, and type scale exactly as spec section 5. Sentence case; no all-caps labels.
- Single-key shortcuts are ignored while focus is in `input`, `textarea`, or `select`.
- `localStorage` access wrapped in try/catch; the UI works when it throws.
- Templates use full literal Tailwind class names. Regenerate `static/app.css` with `make css` in every task that touches templates or `input.css`; `make css-check` must pass.
- Each PR of spec section 7 maps to the tasks marked with it below; open one PR per group, stacked in order.
- Commits: conventional style (`feat(webui): ...`), no Claude attribution.

## Review Focus

1. **Agent updates the memory while the user has unsaved edits.** Save returns 409; the pane re-renders with the user's submitted values intact, an inline error, and a "Load latest version" control; nothing is overwritten. Test in Task 5.
2. **Expiry with a time component, date field untouched.** Saving other fields must not change `expires_at`. Test in Task 7.
3. **Read-only memory or context.** Deleted, superseded, and read-only-context memories render disabled fields and no Save; a forged POST is still rejected server-side as today. Test in Task 5.
4. **Lookup crossing boundaries.** `/lookup` never returns memories from another context or profile memories, and never logs access or searches. Test in Task 7.
5. **Deep link or history restore to `/memories/{id}?q=...`.** Returns the full shell with the filtered list and the memory, not a bare fragment. Test in Task 4.

---

## PR 1: Foundation

### Task 1: Embedded fonts, app.js skeleton, and template hygiene test

**Files:**
- Create: `internal/webui/static/atkinson-hyperlegible-regular.woff2`, `internal/webui/static/atkinson-hyperlegible-bold.woff2`, `internal/webui/static/OFL.txt`, `internal/webui/static/app.js`
- Modify: `internal/webui/server.go` (`handleStatic`), `internal/webui/templates/layout.html`, `docs/design.md` (Web UI script-injection row: "htmx, app.js, fonts, and Tailwind CSS are embedded"), `Makefile` (`fonts-check` target, add to CI next to `htmx-check`), `.github/workflows/ci.yml`
- Test: `internal/webui/assets_test.go`

**Interfaces:**
- Produces: `/static/app.js` loaded with `defer` after htmx in `layout.html`; `app.js` exposes nothing global and registers handlers on `DOMContentLoaded` and `htmx:load` so later tasks add modules as functions inside it.

- [ ] **Step 1: Write failing tests**
  - `TestEmbeddedAssets` also requires the two woff2 files, `OFL.txt`, and `app.js`.
  - `TestStaticFontContentType`: `GET /static/atkinson-hyperlegible-regular.woff2` with a session cookie → 200, `Content-Type: font/woff2`.
  - `TestTemplatesHaveNoInlineStyleOrScript`: for every file in `templates/*.html`, assert no match for `\sstyle\s*=` or `\shx-on`, and that every `<script[^>]*>` tag has a `src=` attribute.
- [ ] **Step 2: Run** `go test ./internal/webui/ -run 'TestEmbeddedAssets|TestStaticFontContentType|TestTemplatesHaveNoInlineStyleOrScript'` → FAIL.
- [ ] **Step 3: Implement.** Add the files (fonts from the upstream Braille Institute release; record sha256 in `Makefile` as `FONT_SHA256_REGULAR`/`FONT_SHA256_BOLD`, `fonts-check` verifies like `htmx-check`). In `handleStatic`, set `Content-Type: font/woff2` for `.woff2` before `ServeFileFS`. `app.js` skeleton: an IIFE with `onReady(fn)` that runs `fn(document)` on load and `fn(evt.target)` on `htmx:load`.
- [ ] **Step 4: Run** the same tests plus `make test lint fonts-check htmx-check` → PASS.
- [ ] **Step 5: Commit** `feat(webui): embed fonts and app.js and guard templates against inline code`.

### Task 2: Tokens, dark mode, theme toggle, restyle

**Files:**
- Modify: `internal/webui/input.css`, every file in `internal/webui/templates/`, `internal/webui/static/app.js`, `internal/webui/static/app.css` (generated)
- Test: `internal/webui/assets_test.go`

**Interfaces:**
- Produces: Tailwind theme colors `paper`, `sheet`, `rail`, `ink`, `ink-muted`, `line`, `rule`, `accent`, `danger`, `type-project`, `type-user`, `type-feedback`, `type-reference` (each with a `-soft` tint for chips); component classes `.sheet-ruled`, `.chip`, `.btn`, `.btn-primary`, `.btn-danger`, `.type-tab-{project,user,feedback,reference,other}`; template func `typeClass(memory.Type) string` returning one of the literal suffixes (`project`, `user`, `feedback`, `reference`, `other`). `<html data-theme>` attribute set by `app.js` (`system`, `light`, `dark`).

- [ ] **Step 1: Write failing tests**
  - `TestLayoutHasThemeToggle`: rendered layout contains a button with `data-theme-toggle` and `<html lang="en"` without a `data-theme` attribute (JS sets it).
  - `TestTypeClass`: table test, `project`→`project`, `user`→`user`, `feedback`→`feedback`, `reference`→`reference`, `"custom"`→`other`.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.** In `input.css`: `@font-face` for both weights (`font-display: swap`), tokens as CSS custom properties on `:root` with dark values under `@media (prefers-color-scheme: dark) { :root:not([data-theme=light]) {...} }` and `:root[data-theme=dark]`; `@theme` maps them to colors; `@custom-variant dark` matching the same two conditions; component classes via `@layer components`. `.sheet-ruled` uses a `repeating-linear-gradient` of `sheet`/`rule` sized to the content line height. Register `typeClass` in `parseTemplates` with `template.FuncMap`. Restyle every template to tokens with no layout change. `app.js`: theme module reads `localStorage["memorable.theme"]`, applies `data-theme` (omits it for `system`), toggle cycles system → light → dark.
- [ ] **Step 4: Run** `make css test lint css-check` → PASS. Check contrast of every text/background and chip pair from spec section 5 against WCAG AA; adjust `ink-muted` and chip tints if needed and record the measured ratios in the PR description.
- [ ] **Step 5: Commit** `feat(webui): add the index cards design tokens, dark mode, and theme toggle`.

---

## PR 2: Workspace shell and list

### Task 3: `UpdatedAt` on list and search summaries

**Files:**
- Modify: `internal/memory/types.go` (`Summary`), the store queries that build summaries (find with `rg -n "Summary{" internal/`), their tests
- Test: existing store integration tests and `internal/memory` unit tests that construct `Summary`

**Interfaces:**
- Produces: `memory.Summary.UpdatedAt time.Time`, filled by `List` and `Search`.

- [ ] **Step 1: Write failing tests.** Extend the integration test for `List` and `Search` to assert `UpdatedAt` equals the row's `updated_at` (non-zero) for a seeded memory.
- [ ] **Step 2: Run** `make test-integration` (filter to the store package) → FAIL.
- [ ] **Step 3: Implement.** Select `updated_at` in each summary query and scan it. MCP output is unchanged (do not add the field to MCP responses).
- [ ] **Step 4: Run** `make test test-integration` → PASS.
- [ ] **Step 5: Commit** `feat(memory): carry updated_at on summaries`.

### Task 4: Two-pane shell, routing, list rows

**Files:**
- Create: `internal/webui/templates/workspace.html`, `internal/webui/templates/memory_row.html`
- Modify: `internal/webui/memories.go`, `internal/webui/detail.go` (`handleDetail`, `handleEdit`, `respondDetail`), `internal/webui/assets.go` (`pageData`), `internal/webui/templates/layout.html`, `internal/webui/templates/memories.html`, `internal/webui/templates/memory_rows.html`
- Test: `internal/webui/memories_test.go`, `internal/webui/detail_test.go`

**Interfaces:**
- Consumes: `Summary.UpdatedAt` (Task 3), `typeClass` (Task 2).
- Produces:
  - `memoriesView` gains `Selected *detailView` and `SelectedID string`; `memoryRow` gains `TypeClass string`, `Age string`, `Current bool`.
  - `func relativeAge(now, t time.Time) string`: `"now"` under 1 minute, then `"Nm"`, `"Nh"`, `"Nd"` up to 30 days, then `2006-01-02`.
  - The detail pane root element is `<section id="detail-pane">`; htmx selections target `#detail-pane` with `hx-swap="outerHTML"`.
  - Row links carry the current filter query string so the full-page URL reproduces the list.

- [ ] **Step 1: Write failing tests**
  - `TestRelativeAge`: table with 30s→`now`, 5m→`5m`, 3h→`3h`, 2d→`2d`, 45d→date.
  - `TestDetailFullPageRendersShell`: `GET /c/personal/memories/{id}?q=go` without `HX-Request` → body contains `id="memory-rows"`, the row for the memory with `aria-current="true"`, and `id="detail-pane"`; the list was requested with query `go`.
  - `TestDetailFragmentForPane`: with `HX-Request: true` and `HX-Target: detail-pane` → body starts with `<section id="detail-pane"` and contains no `id="memory-rows"`.
  - `TestHistoryRestoreGetsFullPage`: with `HX-Request` and `HX-History-Restore-Request: true` → full shell.
  - `TestEditURLRedirects`: `GET .../{id}/edit` → 303 to `.../{id}`.
  - `TestMemoriesEmptyPane`: `GET /c/personal/memories` → contains `Select a memory, or press Ctrl K to jump to one.`
  - `TestLoadMoreAppends`: next-page link has `hx-get`, `hx-target="#memory-rows"`, `hx-swap="beforeend"` and a plain `href`.
- [ ] **Step 2: Run** `go test ./internal/webui/ -run 'TestRelativeAge|TestDetail|TestHistoryRestore|TestEditURL|TestMemoriesEmptyPane|TestLoadMore'` → FAIL.
- [ ] **Step 3: Implement.** `handleDetail` full page: build the list via the existing `listRows`/`searchRows` from the request query, load the detail, render `layout.html` with `Page: "memories"` and `Memories.Selected` set. Fragment only when `isFragmentRequest(r)` and `HX-Target` is `detail-pane`; other htmx targets keep today's behavior. `handleEdit` → `http.Redirect(..., 303)`. `workspace.html` renders the filter bar (search plus a `<details>` "Filters" disclosure holding project, type, status, tag), `#memory-rows`, and `#detail-pane`. Row markup in `memory_row.html` per spec section 6 (type tab, title, `type · project · age`, status chip only when not `active`, "Why" disclosure when explained). `app.js` moves `aria-current="true"` to the clicked row after an htmx selection. Grid layout: list column 340px, panes scroll independently, stacked below 900px with a Back link in the pane.
- [ ] **Step 4: Run** `make css test lint css-check` → PASS.
- [ ] **Step 5: Commit** `feat(webui): two-pane workspace with the memory list beside the selected memory`.

---

## PR 3: Editor

### Task 5: Always-editable detail pane (server side)

**Files:**
- Modify: `internal/webui/templates/detail.html` (becomes the editor pane), `internal/webui/detail.go`, `internal/webui/actions.go` (`handleUpdate`, `action`)
- Delete: `internal/webui/templates/edit_form.html`, `editView` and `respondEdit` once unused
- Create: `internal/webui/templates/toasts.html`
- Test: `internal/webui/detail_test.go`, `internal/webui/actions_test.go`

**Interfaces:**
- Produces:
  - `detailView` gains editable form state: `Form editFields` with `Title, Content, Tags, Type, ExpiresAt string; Pinned bool; Version int`, filled from the memory on load and from the POST on a failed save. `Editable bool` replaces `ReadOnly` for field state (false for deleted, superseded, quarantined, profile, or a read-only context).
  - `Activity []activityEntry` merging feedback and audit, newest first: `activityEntry{At time.Time; When, Kind, Summary string}`.
  - Form ids used by `app.js` (Task 6): `<form id="memory-editor" data-editor data-title="{{.Title}}">`, buttons `data-save` and `data-discard`, chip `data-dirty-marker` (hidden by default), tabs as `<a role="tab">` to `#content`, `#versions`, `#links`, `#activity` sections.
  - Toast region `<div id="toasts" role="status" aria-live="polite">` in `layout.html`; `Notice` and `Error` render into it via `hx-swap-oob="beforeend:#toasts"` on htmx responses and inline on full pages.

- [ ] **Step 1: Write failing tests**
  - `TestUpdateConflictKeepsSubmittedValues`: Update returns `memory.ErrConflict`; POST with title `Mine`, content `typed text` → 409, body has `value="Mine"`, `typed text`, the conflict message, and `Load latest version`.
  - `TestUpdateSecretKeepsSubmittedValues`: Update returns a secret error → 422, rule name shown, submitted content preserved.
  - `TestReadOnlyStatesDisableFields`: for deleted, superseded, and a `ReadOnly` context, the pane has `disabled` on title and content and no `data-save` button.
  - `TestDetailHasEditorWithoutJS`: Save button is a plain submit without `disabled` attribute in markup (JS disables it at runtime).
  - `TestActivityMergesFeedbackAndAudit`: feedback at t2 and audit at t1 and t3 → three entries ordered t3, t2, t1.
  - `TestDangerActionsSeparated`: Delete appears inside the `More actions` `<details>` after a divider, and not in the primary actions row; Purge (admin fixture) appears only in the Activity tab's danger zone with the typed-confirmation field.
  - `TestNoticeIsOutOfBandToast`: htmx POST to `/feedback` → body contains `hx-swap-oob="beforeend:#toasts"` and the notice text.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement** the pane per spec section 6 (header with title input, chips, slug, version, updated; tabs; ruled-sheet content textarea; tags, type, expires, pinned; Save/Discard; More actions menu with Feedback, Activate, Restore, Supersede, divider, Delete). `handleUpdate` re-renders `detail.html` with `Form` from the POST on any error. Existing action routes and their forms keep their URLs and CSRF fields.
- [ ] **Step 4: Run** `make css test lint css-check` → PASS.
- [ ] **Step 5: Commit** `feat(webui): always-editable memory pane with separated destructive actions`.

### Task 6: Editor behavior in app.js (dirty state, save, guard, expand)

**Files:**
- Modify: `internal/webui/static/app.js`, `internal/webui/templates/detail.html` (Expand button `data-expand-toggle`), `internal/webui/input.css` (`body[data-expanded]` rules), `internal/webui/static/app.css`
- Test: `internal/webui/detail_test.go` (markup only); manual checklist

**Interfaces:**
- Consumes: Task 5 data attributes.
- Produces: `document.body.dataset.expanded` (`"true"` or absent); `localStorage["memorable.expanded"]`.

- [ ] **Step 1: Write failing test** `TestPaneHasExpandToggle`: pane contains a button with `data-expand-toggle` and `aria-pressed="false"`.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.**
  - Dirty tracking: snapshot field values on `htmx:load` of `[data-editor]`; on `input`/`change` compare; toggle `data-dirty-marker` hidden, Save/Discard `disabled`. Discard restores the snapshot.
  - Ctrl+S / Cmd+S: `preventDefault` when focus is inside the editor; `requestSubmit()` when dirty.
  - Guard: on `htmx:confirm`, if dirty and the request is a GET targeting `detail-pane` or a navigation, `preventDefault` and call `evt.detail.issueRequest()` only after `confirm("Discard unsaved changes to " + title + "?")`. `beforeunload` sets `returnValue` while dirty. Successful save clears dirty state.
  - Expand: `data-expand-toggle` and `F` toggle `body[data-expanded]`, persist to `localStorage`, update `aria-pressed`; Esc collapses. CSS hides the list column and moves metadata fields into a 260px side column when expanded.
- [ ] **Step 4: Run** `make css test lint css-check` → PASS. Run the manual checklist in Firefox and Chromium, light and dark, and paste it ticked into the PR:
  - Typing shows "Unsaved changes" and enables Save; Discard restores and hides it.
  - Ctrl+S saves and shows the "Saved." toast; no browser save dialog.
  - Selecting another row while dirty asks; Cancel keeps edits.
  - Closing the tab while dirty asks.
  - Expand hides the list, Esc restores, the state survives selecting another memory and a reload.
  - With JS disabled, Save works and no Expand button shows.
- [ ] **Step 5: Commit** `feat(webui): track unsaved edits, save with ctrl+s, and expand the editor`.

---

## PR 4: Inputs and navigation

### Task 7: Lookup endpoint, link and supersede pickers, tag chips, date input

**Files:**
- Create: `internal/webui/lookup.go`, `internal/webui/lookup_test.go`, `internal/webui/templates/lookup_results.html`
- Modify: `internal/webui/server.go` (`routes`), `internal/webui/actions.go` (`patchFromEdit`), `internal/webui/templates/detail.html`, `internal/webui/static/app.js`
- Test: `internal/webui/lookup_test.go`, `internal/webui/actions_test.go`

**Interfaces:**
- Produces:
  - `GET /c/{context}/lookup?q=<text>&for=<link|supersede|jump>` → `lookup_results.html` fragment: up to 8 `<li role="option" data-id="..." data-href="...">` with title, type chip, slug. Empty `q` returns no options (Task 8 adds static entries client-side).
  - Hidden fields `to` (link) and `replacement` (supersede) keep their names; the visible lookup input is named `ref_display` and is ignored by the server. When no option was picked, `app.js` copies the typed text into the hidden field, so a pasted id or slug still works.
  - Editor form gains hidden `expires_at_original`; field `expires_at` becomes `type="date"`.

- [ ] **Step 1: Write failing tests**
  - `TestLookupUsesPassiveSearchInContext`: `q=web` → `Search` called once with `Passive: true`, `K: 8`, `AllProjects: true`; served by the backend of the URL's context only (use two contexts in the fixture, assert the other recorded no calls).
  - `TestLookupExcludesProfile`: a result with `Scope: memory.ScopeProfile` is not rendered.
  - `TestLookupEscapes`: a title `<b>x</b>` renders escaped.
  - `TestExpiryUnchangedKeepsTime`: memory expires `2026-12-01T15:30:00Z`; POST with `expires_at=2026-12-01` and `expires_at_original=2026-12-01T15:30:00Z` → `Patch.ExpiresAt` is unchanged (no expiry field set in the patch).
  - `TestExpiryChangedUsesDate`: POST `expires_at=2026-12-05`, original as above → patch sets `2026-12-05T00:00:00Z`.
  - `TestExpiryClearedWithEmpty`: POST `expires_at=` → patch clears expiry (today's behavior).
- [ ] **Step 2: Run** `go test ./internal/webui/ -run 'TestLookup|TestExpiry'` → FAIL.
- [ ] **Step 3: Implement.** `handleLookup(w, r, rc)` registered as `GET /c/{context}/lookup` through `inContext`. `patchFromEdit`: if the date part of `expires_at_original` equals `expires_at`, leave expiry untouched. Template: date input plus a "Never" button (`data-clear-expiry`). `app.js`: tag chips over the existing `tags` text field (Enter or comma adds, × removes, the text field stays the source of truth and is hidden only when JS runs); lookup inputs use `hx-get` to `/lookup` with `hx-trigger="input changed delay:150ms"` and `app.js` handles option keyboard selection and fills the hidden field.
- [ ] **Step 4: Run** `make css test lint css-check` → PASS. Manual: chips add and remove and save correctly; link and supersede by picking a title; pasted slug still works.
- [ ] **Step 5: Commit** `feat(webui): look up memories by title and edit tags and expiry with real inputs`.

### Task 8: Command palette and shortcuts

**Files:**
- Create: `internal/webui/templates/palette.html` (the `<dialog>` and the shortcut help `<dialog>`)
- Modify: `internal/webui/templates/layout.html`, `internal/webui/static/app.js`, `internal/webui/input.css`, `internal/webui/static/app.css`
- Test: `internal/webui/assets_test.go`; manual checklist

**Interfaces:**
- Consumes: `/lookup?for=jump` (Task 7), `#detail-pane` targeting (Task 4), the dirty guard (Task 6).
- Produces: `<dialog id="palette">` with static entries rendered as `<li role="option" data-href>` for Review, Profile, Stats, Prune (admin only), and each context; `<dialog id="shortcuts">`.

- [ ] **Step 1: Write failing tests**
  - `TestLayoutHasPaletteAndShortcutDialogs`: layout contains `<dialog id="palette"` and `<dialog id="shortcuts"`, the Prune entry only when admin is unlocked, and one entry per context.
  - `TestTopBarSearchWorksWithoutJS`: the top-bar input is inside a GET form to `/c/{ctx}/memories` with name `q`.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.** Ctrl+K / Cmd+K / `/` open the palette (`showModal()`), focus the input; empty query shows static entries; typing swaps in lookup results; Up/Down move `aria-selected`, Enter opens (memory → htmx GET into `#detail-pane` with push-url, through the dirty guard; page → `location.assign`), Esc closes and restores focus. `j`/`k` move the list selection and load after 150ms without further movement; `e` focuses the content field; `F` Expand (Task 6); `?` opens the shortcuts dialog. Shortcuts ignored inside form fields and while a dialog is open (except Esc).
- [ ] **Step 4: Run** `make css test lint css-check` → PASS. Manual checklist in the PR: palette jump, page entries, `j`/`k` across a "Load more" boundary, `?` help, focus returns after Esc, typing `j` in the content field types a `j`.
- [ ] **Step 5: Commit** `feat(webui): command palette and keyboard navigation`.

---

## PR 5: Review and remaining pages

### Task 9: Side-by-side conflicts and restyled secondary pages

**Files:**
- Modify: `internal/webui/review.go`, `internal/webui/templates/review.html`, `internal/webui/templates/quarantined.html`, `internal/webui/templates/profile.html`, `internal/webui/templates/stats.html`, `internal/webui/templates/prune.html`
- Test: `internal/webui/review_test.go`

**Interfaces:**
- Consumes: `memory_row.html` (Task 4), `typeClass` (Task 2), passive fetch (`fetchPassive`).
- Produces: `conflictView` gains `From, To conflictSide` with `conflictSide{ID, Title, Type, Status, Href, Preview string; TypeClass string; Hidden bool}`. `Preview` is the first 12 lines of content. `Hidden` is true for quarantined sides without admin, which then show only the id and "quarantined, content hidden".

- [ ] **Step 1: Write failing tests**
  - `TestConflictShowsBothTitles`: a `contradicts` link between two active memories → both titles and both previews rendered, each with an "Open" link to the workspace URL.
  - `TestConflictHidesQuarantinedSide`: one side quarantined, no admin → that side shows its id and `quarantined, content hidden`, never its title or content.
  - `TestConflictPreviewTruncates`: 20-line content → 12 lines in the preview.
  - `TestConflictFetchesPassively`: fetching the sides records no access events (Get called with `Passive: true`).
- [ ] **Step 2: Run** `go test ./internal/webui/ -run TestConflict` → FAIL.
- [ ] **Step 3: Implement.** Review sections reuse `memory_row.html`; conflicts render as a two-column grid of ruled-sheet previews with Open, Delete, and "Supersede the other" (posting to the existing `/supersede` of the losing side with the winner as `replacement`). Restyle quarantine cards, Profile, Stats (by-type counts use type colors), and Prune to tokens.
- [ ] **Step 4: Run** `make css test lint css-check` → PASS.
- [ ] **Step 5: Commit** `feat(webui): compare conflicting memories side by side`.
