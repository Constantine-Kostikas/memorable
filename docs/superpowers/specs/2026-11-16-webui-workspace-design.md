# memorable web UI workspace redesign

Status: approved
Date: 2026-11-16
Builds on: `docs/superpowers/specs/2026-11-15-web-ui-design.md` (implemented in PR #10). Security and admin rules from that spec and `docs/design.md` are unchanged.

## 1. Intent

The author opens `memorable ui` to check and edit memories. Today every check or edit is a round trip (list, detail page, separate edit page, back), the visual design is unstyled Tailwind defaults with no identity, type and status are indistinguishable, dangerous actions sit next to routine ones, and several inputs ask for raw ids or RFC 3339 strings.

The redesign makes the UI a practical, modern, visually distinct workspace: find a memory fast, read it at a glance, edit it in place.

## 2. Goals, non-goals, anti-goals

### Goals

- Two-pane workspace: memory list on the left, selected memory on the right, selection loaded with htmx and reflected in the URL.
- Always-editable detail pane: fields are inputs from the start; an "Unsaved changes" marker and Save appear on the first change; Ctrl+S saves.
- An Expand control (button and `F`) that gives the editor the full window width and height; Esc or Collapse returns to two panes.
- A distinct visual identity ("Index cards", section 5) in light and dark.
- Practical inputs: tag chips, a native date picker for expiry, title search for link and supersede targets.
- Keyboard: Ctrl+K command palette (jump to a memory by title or slug), `j`/`k` list navigation, `?` shortcut help.
- Destructive actions (Supersede, Delete, admin Purge) separated from Save.
- Review conflicts shown side by side by title, with actions in place.
- Profile, Stats, and Prune restyled to match.

### Non-goals

- New memory operations or new admin capabilities. Every action maps to an existing handler.
- Creating new memories from the UI (not in the current UI either).
- Markdown rendering of memory content.
- Mobile-first design. Narrow screens must work (panes stack), not shine.
- Autosave.

### Anti-goals (failure even if it works)

- Losing an edit silently: navigating away, switching memories, or a concurrent agent update must never discard typed changes without asking.
- Any inline script, inline style, `eval`, `hx-on`, or CDN asset (the CSP stays `default-src 'self'`).
- A Node toolchain or JS framework anywhere.
- Rendering memory content as anything but escaped text.
- Weakening admin gating, CSRF, or the typed purge confirmation.
- A UI that only works with JavaScript for reading: list, detail, and Save must still work as plain links and form posts.

## 3. Constraints

- Go `html/template` + htmx 2.0.x + Tailwind v4 standalone CLI, as today. No new Go dependencies.
- Client behavior lives in one hand-written, embedded `static/app.js` (plain ES2020, no build step), loaded with `defer`. It is enhancement only.
- CSP is unchanged. Because `style-src` falls back to `'self'`, no `style=""` attributes: every visual state is a class in `app.css`.
- Fonts are embedded under `static/` as woff2 (Atkinson Hyperlegible, SIL OFL 1.1, regular and bold). `handleStatic` must serve `font/woff2` explicitly, since Go's built-in MIME table lacks `.woff2` and responses carry `nosniff`. The license file ships next to the fonts.
- Font and htmx files are pinned by sha256 in the Makefile like htmx today; `make css-check` keeps guarding generated CSS.
- Existing URLs keep working: `/c/{ctx}/memories`, `/c/{ctx}/memories/{id}`, `/c/{ctx}/memories/{id}/edit` (redirects to the detail URL, which is now the editor).

## 4. Approaches considered

| Approach | Trade-offs | Outcome |
|---|---|---|
| Polish current pages (tokens, badges, tabs, toasts) | Low risk; keeps list → detail → edit round trips, the main friction. | Rejected. |
| Two-pane workspace with inline editing, palette, keyboard | More work; removes round trips; fits "check and edit". | **Chosen** by the author, delivered as small stacked PRs, plus a full-window editor toggle. |
| JS framework rewrite (Alpine, Preact) | Fights the CSP and the no-Node rule for little gain over htmx. | Dropped. |

Visual direction: three looks were mocked (Index cards, Graphite, Sage archive). The author picked **Index cards**. Editing mode: the author picked **always editable** over read-first.

## 5. Visual design: Index cards

### Tokens (CSS custom properties in `input.css`, mapped into Tailwind v4 `@theme`)

| Token | Light | Dark | Use |
|---|---|---|---|
| `paper` | `#F5F7FA` | `#141A24` | page background |
| `sheet` | `#FFFFFF` | `#1A2230` | editor and card surfaces |
| `rail` | `#EBEFF5` | `#18202C` | list pane, side column |
| `ink` | `#1B2433` | `#DCE3EE` | text |
| `ink-muted` | `#5B6678` | `#8593A8` | metadata |
| `line` | `#D3DAE5` | `#263142` | borders, dividers |
| `rule` | `#EEF2F8` | `#1F2836` | ruled-sheet lines behind content |
| `accent` | `#2F5BEA` | `#6F8FFF` | primary action, focus, selection |
| `danger` | `#B42318` | `#F97066` | destructive actions |

Memory type colors (row tab and chip; chip uses a tinted background with the strong color as text):

| Type | Light | Dark |
|---|---|---|
| project | `#2F5BEA` | `#6F8FFF` |
| user | `#0F8B8D` | `#3FC1C3` |
| feedback | `#7A4FD6` | `#A98BFF` |
| reference | `#C08A1E` | `#E0AE48` |

Any other type value falls back to `ink-muted`. Status chips: active green, unverified amber, deleted and superseded grey, quarantined amber with the existing "untrusted" wording.

All text and chip pairs must meet WCAG AA (4.5:1 body, 3:1 large and UI). Correct the `ink-muted` value if it fails; the table value is a starting point.

### Type

- Atkinson Hyperlegible for all UI and memory content. Scale: 12 (meta), 13 (body, list), 15 (pane title), 18 (page title). Weights 400 and 700 only.
- Monospace (system `ui-monospace`) only for ids and slugs.
- Sentence case everywhere; no all-caps labels.

### Signature element

Spend boldness in one place: the **ruled sheet**. Memory content sits on a white (dark: `sheet`) surface with faint horizontal rules aligned to the content line height, and each list row carries a 4px colored type tab on its left edge. Everything else is quiet: flat surfaces, 1px `line` borders, 4px radius on controls, 6px on surfaces, no shadows except the focus ring.

### Theme

Dark follows `prefers-color-scheme`. A toggle in the top bar cycles system, light, dark; the choice is stored in `localStorage` (wrapped in try/catch) and applied as `data-theme` on `<html>` by `app.js`. Tailwind dark variant: `@custom-variant dark` keyed on `[data-theme=dark]` or the media query when no override is set.

## 6. Layout and behavior

### Shell

```
+---------------------------------------------------------------------+
| memorable  [personal][work]  [Search or jump...  Ctrl K]   Review 3  |
|                                     Profile  Stats  (Prune)  theme  |
+------------------------+--------------------------------------------+
| filter bar             |  Title (input)                    [Expand] |
|------------------------|  [project] [active]  slug  v3  updated 2h  |
|| Web UI decision       |  Content | Versions 3 | Links 1 | Activity |
|| project . memorable 1d|  +--------------------------------------+  |
|------------------------|  | ruled sheet: content <textarea>     |  |
|| User Go experience    |  +--------------------------------------+  |
|| user . 2d             |  Tags [web-ui x][design x] add...          |
| ...                    |  Type [project v]  Expires [date] [] Pinned|
| Load more              |  [Save Ctrl S] [Discard]     More actions v|
+------------------------+--------------------------------------------+
```

- Top bar: brand, context switcher, the palette trigger (also a plain search input without JS), Review with count, Profile, Stats, Prune (admin), admin badge, theme toggle.
- List pane (fixed width about 340px, own scroll): compact filter bar (search, then a "Filters" disclosure for project, type, status, tag), rows with type tab, title, `type · project · relative age`, status chip only when not active, and the search-ranking explanation kept behind a small "Why" disclosure when a query is present. "Next page" becomes "Load more" (htmx append; still a link without JS).
- Detail pane (own scroll): the editor (below), tabs for Content, Versions, Links, Activity (feedback plus audit merged into one timeline, newest first).
- Empty right pane: "Select a memory, or press Ctrl K to jump to one."
- Below 900px the panes stack: list first, detail replaces it on selection with a Back link.

### Routing

- `GET /c/{ctx}/memories` renders the shell with an empty detail pane.
- `GET /c/{ctx}/memories/{id}` renders the shell with the list (filter from the query string) and the memory in the detail pane. With `HX-Request` targeting the pane, it returns only the pane fragment. The list keeps its filter and scroll position across selection.
- `GET /c/{ctx}/memories/{id}/edit` redirects (303) to the detail URL.
- Selection uses `hx-get` + `hx-push-url`; the selected row gets `aria-current="true"`.

### Always-editable editor

- Title, content, tags, type, expires, and pinned are form fields in one `<form>` posting to the existing `/update` with the loaded `version`.
- `app.js` tracks dirtiness by comparing current field values to their initial values. On the first difference it shows the "Unsaved changes" chip and enables Save and Discard (both disabled when clean). Discard restores initial values.
- Ctrl+S (Cmd+S on macOS) submits when dirty; prevents the browser save dialog in all cases while the editor is focused.
- Unsaved-changes guard: while dirty, selecting another memory, using the palette, switching context, or unloading the page asks "Discard unsaved changes to <title>?". Implemented with `htmx:confirm` for htmx navigations and `beforeunload` for full loads.
- Conflict (`409`): the pane re-renders with the submitted values preserved, an inline error, and "Load latest version" which opens the latest content in the Versions tab for comparison; it never overwrites.
- Scanner rejections keep today's behavior (inline secret rule name; quarantine notice), with submitted values preserved.
- Read-only states (deleted, superseded, profile without admin, `ReadOnly` contexts) render the same layout with `readonly`/`disabled` fields and no Save.
- Without JS: the form is a normal form with an always-enabled Save.

### Expand

- Expand button in the pane header and `F` (when focus is not in a text field) toggle `data-expanded` on `<body>`. CSS hides the list pane and widens the detail pane to the full viewport height and width; the metadata fields move into a 260px side column (as mocked). Esc or Collapse restores.
- The state persists in `localStorage` for the session's next selections. Without JS the button is absent.

### Inputs

- Tags: chips with remove buttons plus an input that adds a chip on Enter or comma. The underlying value is the existing comma-separated `tags` field, so the server is unchanged. Without JS it is the plain text field.
- Expires: `<input type="date">` plus a "Never" clear button. The server already accepts a date. Existing timestamps with a time component show the date part and a note that saving keeps the date only (or keep the full value in a hidden field when the date is untouched; builder's call, must not silently move expiry earlier).
- Link and Supersede targets: a lookup input calling a new `GET /c/{ctx}/lookup?q=` endpoint that returns up to 8 matches (title, type, slug, id) as an htmx fragment, using passive search (no access events, no search log) within the request's context only and excluding profile memories, as `resolveRef` does; picking one fills a hidden `to`/`replacement` field with the id. Typing an id or slug directly still works.

### Actions

- Primary row: Save, Discard.
- "More actions" disclosure (a `<details>` styled as a menu): Feedback (useful, wrong, outdated, note), Activate, Restore, Supersede (with lookup), and separated by a divider, Delete (danger). Admin: Purge in a "Danger zone" block at the bottom of the Activity tab with the typed confirmation unchanged.
- Notices become toasts in a fixed live region (`role="status"`), auto-dismissed after 5 seconds; errors (`role="alert"`) stay until dismissed.

### Command palette and shortcuts

- Ctrl+K / Cmd+K or `/` opens a modal `<dialog>` with an input that queries `/c/{ctx}/lookup` (debounced 150ms). Up and Down move, Enter opens the memory in the detail pane, Esc closes. Static entries for pages (Review, Profile, Stats, Prune when admin) and contexts are listed when the query is empty.
- `j`/`k` move selection in the list and load the memory after 150ms without further movement; `e` focuses the content field; `F` toggles Expand; `?` opens a shortcut help dialog. Single-key shortcuts are ignored while focus is in an input, textarea, or select.
- Focus is always visible (2px `accent` ring); dialogs trap focus and restore it on close.

### Review

- Each section's rows use the list row component.
- Conflicts render as a two-column comparison: each side shows title, type and status chips, and the first 12 lines of content on the ruled sheet, with "Open" (loads into the workspace) and the existing actions (Delete, Supersede the other) in place.
- Quarantined items keep the amber "untrusted" card styling, restyled to tokens.

### Profile, Stats, Prune

- Profile memories keep their own page and admin forms; they do not open in the workspace editor.
- Restyle only. Stats tiles get the card surface and type colors for the by-type counts.

## 7. Delivery: stacked PRs

Each PR is independently shippable and keeps `make test`, `make lint`, and `make css-check` green.

1. **Foundation:** tokens, dark mode, theme toggle, embedded fonts (with `font/woff2` serving), `app.js` skeleton, restyle of every existing template to tokens. No layout change.
2. **Workspace shell and list:** two-pane layout, routing changes, list row component, filter disclosure, Load more, stacked narrow layout.
3. **Editor:** always-editable pane, dirty tracking, Save/Discard, Ctrl+S, unsaved guard, conflict handling, tabs with merged Activity, More actions menu, toasts, Expand.
4. **Inputs and navigation:** tag chips, date picker, `/lookup` endpoint, link and supersede lookup, command palette, `j`/`k`, `?` help.
5. **Review:** side-by-side conflicts, restyled quarantine cards, Profile/Stats/Prune polish.

## 8. Testing

- Go handler tests (existing `httptest` style) for every server-visible change: routing and fragments, `/edit` redirect, `/lookup` results and context isolation, `font/woff2` content type, read-only rendering, conflict and scanner re-render preserving submitted values, absence of `style=` attributes and inline scripts in every template (a template lint test).
- `app.js` has no automated browser tests (no Node). Each PR that touches it carries a manual verification checklist in its description, run against `memorable ui` in Firefox and Chromium, light and dark.
- Contrast check of the token pairs recorded in the foundation PR.

## 9. Left to the builder

- Exact spacing values, icon choice (inline SVG in templates, no icon font), and toast animation (respect `prefers-reduced-motion`).
- How the expiry time component is preserved (section 6, Inputs).
- Whether the list pane width is resizable (not required).
