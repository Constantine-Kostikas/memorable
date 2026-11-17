# Tags: ranking, vocabulary, and browsing

Status: proposed. Amends `docs/design.md` sections 5 (schema), 6 (MCP tools), and 7 (preload).

## Why

Tags exist (`tags text[]`, max 10, `[a-z0-9-]{1,40}`, GIN index; `tags @>` filter on `memory_search`, `memory_list`, and `memorable list --tag`), but they do little:

- They do not affect ranking. `fts` is built from title and content only, and so is the embedded text (`DocumentText(title, content)`). A tag is a hard filter or nothing.
- Nobody can see the vocabulary. Claude invents tags at write time with no view of existing ones, which invites drift (`go`, `golang`, `go-lang`).
- Nothing tells Claude when or how to tag. `docs/claude-md-snippet.md` does not mention tags.
- Measured on 2026-11-15: 66 of 68 memories in the current context have no tags.

The goal is organizing (browse by area) and better search recall.

## Goals

- Tags contribute to every search, with no extra tool call or retry.
- Claude reuses an existing, visible vocabulary instead of inventing tags.
- Browsing by tag works well in the CLI and the web UI, including "any of these tags".

## Non-goals

- A tag fallback search ("if the query misses, search again by tags"). It depends on the model remembering to retry, and a wrong tag guess fails the same way a wrong query does. Folding tags into ranking gives the same benefit on every search.
- Tag hierarchies, synonyms, or aliases.
- Automatic tagging by an LLM.
- Backfilling tags on existing memories (a manual `memorable edit` or review pass is enough).

## Anti-goals

- A tag that matches nothing in title or content should not outrank a strong content match. Tags are a boost, not an override.
- Untagged memories must not get worse search results.

## Approaches

1. **Fold tags into ranking, expose the vocabulary, and guide Claude (chosen).** One search benefits from tags automatically; the vocabulary is discoverable; browsing gains any-of matching.
2. **Tag fallback search driven by the model.** Cheap to build (instructions only) but adds a round trip, relies on the model, and returns nothing while tagging is sparse.
3. **Fixed, closed vocabulary set by the user.** Prevents drift fully but needs upfront curation and blocks writes on unknown tags. Premature with two tags in use; revisit if drift shows up despite approach 1.

## Design

### 1. Tags in ranking

- **Keyword:** new migration (`00003_tags_fts.sql`; goose version 2 is the Go embedding migration) in both `context` and `profile` schemas. Rebuild the generated `fts` column as title (weight A) + tags (weight A) + content (weight B), and recreate `memories_fts_idx`. Generated columns require immutable expressions, and `array_to_string` is expected to be marked stable (confirm in `pg_proc`); if so, add an `IMMUTABLE` SQL helper (for example `memorable_tags_text(text[])`) and use that.
- **Semantic:** `DocumentText(title, content string)` becomes `DocumentText(title string, tags []string, content string)`, producing `title: … | tags: a, b | text: …`. The `tags:` segment is omitted when there are no tags, so untagged memories keep byte-identical embedding text and need no re-embed.
- **Re-embed on tag change:** a change to tags alone now marks the embedding stale, in `internal/memory` (update path) and `internal/adminops` (profile). Today only title or content changes do.
- **Rollout:** after the migration, `memorable reembed --all` refreshes tagged memories, including profile memories. The release note says so.
- **Eval:** add cases to `internal/memory/eval_test.go` where the only link between query and memory is a tag. Compare recall before and after.

### 2. Visible vocabulary

- **MCP tool `memory_tags`:** returns tags in use with counts, for the current project plus context-global plus profile; `all_projects` widens it. Compact output, sorted by count.
- **CLI `memorable tags`:** same data, text or JSON (`-o json`), `--all-projects`.
- **Preload:** one extra line, `Tags in use: a (12), b (7), …`, top 20 by count, outside the existing 4,000-character summary budget (20 tags of at most 40 characters bound it). Claude sees the vocabulary at session start without a tool call.
- **Normalization on write:** lowercase, trim, replace spaces and underscores with hyphens, and dedupe before validation, so `Go` and `go` collapse instead of failing validation.

### 3. Guidance

Add to `docs/claude-md-snippet.md`: tag every new memory with 1 to 3 tags; reuse tags from the preload line or `memory_tags` before inventing one; use tags for the topic or area (`database`, `testing`, `ci`), not for the type or project, which have their own fields.

### 4. Any-of matching

- `memory_search` and `memory_list` gain `tags_any` (match at least one, `tags && $n`) beside the existing `tags` (match all, `tags @> $n`). Both may be combined.
- CLI: `--tag` stays all-of; new repeatable `--any-tag`.
- Web UI: the tags filter offers all/any (added in the web UI plan, which is not merged yet).

## Decided by the user

- Tags are for organizing and for search.
- Approach 1 over a model-driven fallback search.

## Calls made in this spec (open to correction)

- Open vocabulary nudged toward reuse, not a closed list.
- Tags weighted the same as the title (A) in `fts`.
- Vocabulary shown in the preload, not only on request.
- Write-time normalization instead of rejection.

## Left to the builder

- Whether the eval shows tag weight A over-boosts; drop to B if so.
- Exact `memory_tags` output shape, following the compact style of `memory_list`.
