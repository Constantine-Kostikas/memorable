# Prompt-time Recall Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a UserPromptSubmit hook that injects pointers to memories whose calibrated relevance is at least 70%, and the calibration machinery (signals logging, logistic fit, `memorable calibrate`) behind the percentage.

**Architecture:** A pure logistic model in `internal/rank` maps retrieval signals to relevance. `internal/memory` gains `Recall` (hook flow) and `Calibrate` (fit from labeled events). `internal/store` logs richer access events (kind, session, per-result signals) and stores fits. `cmd/memorable` adds `hook prompt-submit` and `calibrate`; setup, doctor, and stats learn about both.

**Tech Stack:** Go 1.26, pgx v5, goose migrations, cobra, testify, testcontainers (integration), local Ollama (eval only).

**Spec:** `docs/superpowers/specs/2026-11-17-prompt-recall-design.md`

## Global Constraints

- Hooks never fail a session: any error or timeout prints nothing and exits 0.
- `memorable serve` opens no listeners.
- Injected output is labeled stored data, never instructions.
- Preload, inject, and hook events never change usage scores (`RecordAccess` is not called).
- Defaults: `min_relevance = 0.70`, `max_items = 3`, `min_words = 3`, `deadline_ms = 1500`. Validation: `min_relevance` in (0, 1), `max_items` 1 to 10, `deadline_ms` 200 to 5000, `min_words` >= 1.
- Search K for recall is 10; query truncated to `memory.MaxQueryRunes` (500) runes.
- A usage fit is saved only with at least 20 positives and 20 negatives; negatives count only for events older than 24 hours.
- No new module dependencies. English only. No em dashes in docs.
- Use `rg`, never `grep`; `fd`, never `find`.

## Review Focus

1. A prompt containing a pasted secret: the search still runs, but the stored event's `query` is NULL (Task 5 test `TestRecallNullsSecretQuery`).
2. Multibyte prompts longer than 500 runes: truncation by runes, never splitting a UTF-8 sequence (Task 5 test `TestRecallTruncatesByRunes`).
3. A cold or hung Ollama: the hook's deadline expires and stdout stays empty (Task 7 test `TestPromptSubmitDeadline`).
4. Hook input without `session_id`: no dedup is possible, injection still works, nothing errors (Task 5 test `TestRecallWithoutSession`).
5. A memory deleted or purged after it was shown: labeling and `calibrate` skip it without error (Task 4 test `TestLabeledResultsSkipsMissingMemories`).

---

### Task 1: Logistic relevance model

**Files:**
- Create: `internal/rank/calibration.go`, `internal/rank/calibration_test.go`

**Interfaces:**
- Produces:
  - `type Signals struct { Similarity float64; FTS, Trigram bool; VectorRank int }` (enters as 1/VectorRank, 0 when absent)
  - `type Calibration struct { B0, B1, B2, B3, B4 float64 }`
  - `func (c Calibration) Relevance(s Signals) float64` (sigmoid of `B0 + B1*Similarity + B2*fts + B3*tri + B4/VectorRank`)
  - `type Sample struct { Signals Signals; Relevant bool }`
  - `func Fit(samples []Sample, prior Calibration, lambda float64) Calibration` (L2 penalty `lambda * ||b - prior||^2`, batch gradient descent, fixed iterations, deterministic)
  - `type Bucket struct { Low, High float64; Predicted, Observed float64; N int }`
  - `func Reliability(samples []Sample, c Calibration) []Bucket` (10 equal-width buckets over [0,1], empty buckets omitted)
  - `func DefaultCalibration(model string, dims int) (Calibration, bool)` reading a package-level `map[string]Calibration` keyed `model + "/" + strconv.Itoa(dims)`; empty until Task 2.

- [ ] **Step 1: Write the failing tests**

```go
func TestRelevanceIsSigmoid(t *testing.T) {
	c := rank.Calibration{B0: 0}
	assert.InDelta(t, 0.5, c.Relevance(rank.Signals{}), 1e-9)
	assert.Greater(t, rank.Calibration{B1: 4}.Relevance(rank.Signals{Similarity: 0.9}), 0.95)
}

func TestFitRecoversKnownCoefficients(t *testing.T) {
	truth := rank.Calibration{B0: -4, B1: 8, B2: 1.5, B3: 0.5}
	samples := synthetic(t, truth, 20000, 42) // deterministic rand.New(rand.NewPCG(42, 0))
	got := rank.Fit(samples, rank.Calibration{}, 1e-4)
	assert.InDelta(t, truth.B0, got.B0, 0.4)
	assert.InDelta(t, truth.B1, got.B1, 0.8)
	assert.InDelta(t, truth.B2, got.B2, 0.4)
	assert.InDelta(t, truth.B3, got.B3, 0.4)
}

func TestFitStaysNearPriorWithFewSamples(t *testing.T) {
	prior := rank.Calibration{B0: -3, B1: 6, B2: 1, B3: 1}
	got := rank.Fit(synthetic(t, rank.Calibration{B0: 2}, 10, 1), prior, 1.0)
	assert.InDelta(t, prior.B1, got.B1, 0.5)
}

func TestReliabilityBuckets(t *testing.T) { /* all-positive samples at p≈0.85 → one bucket [0.8,0.9), Observed 1, N == len */ }

func TestDefaultCalibrationUnknownModel(t *testing.T) {
	_, ok := rank.DefaultCalibration("nope", 1)
	assert.False(t, ok)
}
```

- [ ] **Step 2: Run** `go test ./internal/rank/ -run 'Relevance|Fit|Reliability|DefaultCalibration'`. Expected: FAIL, undefined.
- [ ] **Step 3: Implement** in `internal/rank/calibration.go`. Learning rate and iteration count are the implementer's choice, as long as the recovery test passes in under one second.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(rank): add logistic relevance calibration`.

### Task 2: Prompt-shaped queries and the default fit

**Files:**
- Modify: `testdata/eval/queries.json` (append about 40 entries with `"prompt_shaped": true`)
- Modify: `internal/memory/eval_test.go`
- Modify: `internal/rank/calibration.go` (the defaults map gains the generated entry)

**Interfaces:**
- Consumes: `rank.Fit`, `rank.Reliability`, `rank.Signals`, `memory.SearchRequest{Explain: true}` and `memory.Explanation`.
- Produces: `rank.DefaultCalibration("embeddinggemma", 768)` returns `ok == true`.

- [ ] **Step 1: Add the queries.** About 40 conversational, multi-sentence prompts (30 to 400 characters) written the way a developer talks to Claude Code, against the existing 50 memories. About 10 have `"relevant": []`. Example: `{"query": "I'm about to change how the database container is started, anything I should keep in mind about ports or the image before I touch docker?", "project": "memorable", "relevant": ["memorable/db-port", "memorable/db-image"], "prompt_shaped": true}`.
- [ ] **Step 2: Extend the harness.** Add `PromptShaped bool \`json:"prompt_shaped"\`` to `evalQuery`. Search with `Explain: true`. For every returned result build a `rank.Sample` (signals from `Explanation`: `Similarity`, `FTSRank > 0`, `TrigramRank > 0`; `Relevant` when the slug is in `q.Relevant`). After the loop, for `defaultModel`, fit with `rank.Fit(samples, rank.Calibration{}, 1e-3)`, log the reliability table for all, short, and prompt-shaped queries separately, and log a Go literal: `"embeddinggemma/768": {B0: ..., B1: ..., B2: ..., B3: ...},`. Report recall@5 and MRR separately for prompt-shaped queries. Keep existing assertions on short queries only.
- [ ] **Step 3: Run** `make test-eval` (needs local Ollama with `embeddinggemma`). Expected: PASS, and the log shows the literal and three reliability tables.
- [ ] **Step 4: Paste the literal** into the defaults map in `internal/rank/calibration.go`. Add `TestDefaultCalibrationEmbeddingGemma` asserting `ok` and `B1 > 0`.
- [ ] **Step 5: Run** `go test ./internal/rank/`. Expected: PASS.
- [ ] **Step 6: Commit** `test(eval): add prompt-shaped queries and fit the default calibration`.

### Task 3: Schema for events and calibrations

**Files:**
- Create: `internal/store/migrations/context/00004_recall_events.sql`, `internal/store/migrations/profile/00004_recall_events.sql` (profile: version bump only, `SELECT 1;` up and down, so `doctor`'s single latest version still holds)
- Modify: `internal/store/migrate.go` (`latestMigrationVersion = 4`; grant `SELECT, INSERT` on `calibrations` to the app role in context schemas)
- Test: `internal/store/migrate_integration_test.go` (or the existing migration test file)

Context migration up:

```sql
ALTER TABLE access_events DROP CONSTRAINT access_events_kind_check;
ALTER TABLE access_events ADD CONSTRAINT access_events_kind_check CHECK (kind IN ('search', 'get', 'inject', 'preload'));
ALTER TABLE access_events ADD COLUMN session_id text;
ALTER TABLE access_events ADD COLUMN details jsonb NOT NULL DEFAULT '{}';
CREATE INDEX access_events_session_idx ON access_events (session_id, kind) WHERE session_id IS NOT NULL;
CREATE INDEX access_events_search_idx ON access_events (search_id) WHERE search_id IS NOT NULL;
CREATE TABLE calibrations (
    id         uuid PRIMARY KEY,
    owner      text NOT NULL,
    model      text NOT NULL,
    dimensions int  NOT NULL,
    b0 float8 NOT NULL, b1 float8 NOT NULL, b2 float8 NOT NULL, b3 float8 NOT NULL, b4 float8 NOT NULL,
    positives  int  NOT NULL,
    negatives  int  NOT NULL,
    source     text NOT NULL CHECK (source IN ('default', 'usage')),
    fitted_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX calibrations_model_idx ON calibrations (owner, model, dimensions, fitted_at DESC);
```

Down reverses it: delete rows with the new kinds before restoring the old CHECK.

- [ ] **Step 1: Write the failing integration test** `TestMigrationRecallEvents`: after `Migrate`, inserting an `access_events` row with `kind = 'inject'`, `session_id`, and `details` succeeds as the app role, and inserting into `calibrations` as the app role succeeds while `DELETE FROM calibrations` is denied. Confirm the constraint name with `\d access_events` semantics (query `pg_constraint`) in the test setup if needed.
- [ ] **Step 2: Run** `make test-integration` (or `go test -tags integration ./internal/store/ -run TestMigrationRecallEvents`). Expected: FAIL.
- [ ] **Step 3: Add the migrations and grant.**
- [ ] **Step 4: Run** again, plus the existing "migrate up from previous version" test. Expected: PASS.
- [ ] **Step 5: Commit** `feat(store): add recall event columns and calibrations table`.

### Task 4: Event logging, labels, and calibration storage

**Files:**
- Modify: `internal/memory/repository.go`, `internal/memory/memorytest/fakes.go`
- Modify: `internal/store/events.go`
- Create: `internal/store/calibrations.go`
- Test: `internal/store/events_integration_test.go`, `internal/store/calibrations_integration_test.go`

**Interfaces:**
- Produces (in `internal/memory/repository.go`), replacing `LogSearch`:
  - `type EventKind string` with `EventSearch = "search"`, `EventInject = "inject"`, `EventPreload = "preload"`
  - `type ResultSignals struct { ID uuid.UUID \`json:"id"\`; Similarity float64 \`json:"sim"\`; FTS bool \`json:"fts"\`; Trigram bool \`json:"tri"\`; VectorRank int \`json:"vrank"\` }`
  - `type EventDetails struct { Origin string \`json:"origin,omitempty"\`; HookSource string \`json:"hook_source,omitempty"\`; Signals []ResultSignals \`json:"signals,omitempty"\` }`
  - `type Event struct { Kind EventKind; Query *string; Project *string; SessionID string; Returned []uuid.UUID; Details EventDetails }`
  - Repository methods:
    - `LogEvent(ctx context.Context, e Event) (uuid.UUID, error)` (empty `SessionID` stored as NULL)
    - `SessionShown(ctx context.Context, sessionID string) ([]uuid.UUID, error)`: ids returned by `preload` and `inject` events of that session created at or after the newest `preload` event whose `details->>'hook_source'` is `startup`, `clear`, or `compact` (all events when there is none)
    - `LabeledResults(ctx context.Context, before time.Time) ([]rank.Sample, error)`: one sample per `details.signals` element of `search` and `inject` events created before `before` with `details->>'origin'` in (`mcp`, `hook`), `Relevant` = a `get` event exists with that `search_id` and `memory_id`; elements whose memory no longer exists are skipped
    - `SaveCalibration(ctx context.Context, c StoredCalibration) error`
    - `LatestCalibration(ctx context.Context, model string, dims int) (*StoredCalibration, error)` (nil, nil when none)
    - `RecallStats(ctx context.Context, since time.Time) (RecallStats, error)`
  - `type StoredCalibration struct { Model string; Dimensions int; Coefficients rank.Calibration; Positives, Negatives int; Source string; FittedAt time.Time }`
  - `type RecallStats struct { Injections, Injected, Fetched int }` (events, memories shown, of those fetched)
- The fake implements all of these in memory and keeps recording event kinds in `events` so existing tests still work.

- [ ] **Step 1: Write failing integration tests:**
  - `TestLogEventRoundTrip`: an inject event with signals and a session id is stored with the right kind, session, and `details`.
  - `TestSessionShownResetsOnCompact`: preload(startup, A) then inject(B) then preload(compact, C) then inject(D) → `{C, D}`; then preload(resume, E) → `{C, D, E}`.
  - `TestLabeledResults`: an mcp search showing X and Y, then `LogGet(X, &searchID)` → X relevant, Y not; a `cli` origin event and an event newer than `before` are excluded.
  - `TestLabeledResultsSkipsMissingMemories`: a signal whose memory was purged is skipped.
  - `TestCalibrationLatestByModel`: two fits for model A and one for B → newest A returned for A; unknown model → nil, nil.
- [ ] **Step 2: Run** `go test -tags integration ./internal/store/ -run 'LogEvent|SessionShown|LabeledResults|Calibration'`. Expected: FAIL.
- [ ] **Step 3: Implement** the store methods, the interface, and the fake. Update `Service.Search` to call `LogEvent(Event{Kind: EventSearch, ...})` in place of `LogSearch` (behavior unchanged for now).
- [ ] **Step 4: Run** the integration tests and `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(store): log typed access events with signals and store calibrations`.

### Task 5: Search signals, origin, and the Recall service

**Files:**
- Modify: `internal/memory/search.go`, `internal/memory/types.go` (`Summary.Source`), `internal/memory/service.go` (`Settings.Recall`, `Settings.Calibration`)
- Create: `internal/memory/recall.go`, `internal/memory/recall_test.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`
- Modify: `internal/mcpserver/tools.go` (search origin `mcp`), `cmd/memorable/read.go` (search origin `cli`)

**Interfaces:**
- Consumes: Task 1 (`rank.Calibration`, `rank.Signals`), Task 4 (`Event`, `LogEvent`, `SessionShown`, `LatestCalibration`).
- Produces:
  - `config.Recall struct { MinRelevance float64 \`toml:"min_relevance"\`; MaxItems int \`toml:"max_items"\`; MinWords int \`toml:"min_words"\`; DeadlineMS int \`toml:"deadline_ms"\` }`, field `Config.Recall \`toml:"recall"\``, defaults and validation per Global Constraints.
  - `SearchRequest.Origin string`; non-passive searches log `Details{Origin, Signals}` where signals exclude `ScopeProfile` results.
  - `Summary.Source Source`, set by `summarize`.
  - `type RecallRequest struct { Prompt, SessionID string }`
  - `type RecalledMemory struct { Summary Summary; Relevance float64 }`
  - `type RecallResponse struct { SearchID uuid.UUID; Items []RecalledMemory }`
  - `func (s *Service) Recall(ctx context.Context, r RecallRequest) (RecallResponse, error)`
  - `func (s *Service) ActiveCalibration(ctx context.Context) (cal rank.Calibration, source string, ok bool, err error)`: newest stored fit for `s.emb.Model()`/`s.emb.Dimensions()`, else `rank.DefaultCalibration`, else `ok == false`.
  - `func (s *Service) LogPreload(ctx context.Context, sessionID, hookSource string, ids []uuid.UUID) error` (Event kind `preload`, origin `hook`)

Recall follows spec section 2 steps 1 to 9 exactly. Empty `Items` with zero `SearchID` when skipped or nothing passes; no event is logged then. `ok == false` from `ActiveCalibration` also yields empty.

- [ ] **Step 1: Write failing unit tests** (fake repo, fake embedder, fixed calibration via a `Settings` override or a stored fit in the fake):
  - `TestRecallSkipsShortAndSlashPrompts`: `"ok thanks"`, `"/clear"` → empty, no events.
  - `TestRecallTruncatesByRunes`: a 600-rune prompt of `"é"` reaches the embedder as 500 runes, valid UTF-8.
  - `TestRecallNullsSecretQuery`: a prompt containing an AWS-style key yields a logged inject event with `Query == nil`.
  - `TestRecallRequiresVector`: failing embedder → empty, no event.
  - `TestRecallFiltersAndCaps`: five matches with relevance 0.95, 0.9, 0.8, 0.75, 0.5 → three items in that order; a `SourceToolOutput` match and an unverified match are dropped.
  - `TestRecallDedupsSession`: ids returned by `SessionShown` are excluded.
  - `TestRecallWithoutSession`: empty `SessionID` → items returned, `SessionShown` not consulted.
  - `TestRecallNeverCountsAccess`: no `RecordAccess` calls.
  - `TestRecallNoCalibration`: unknown model, nothing stored → empty.
  - `TestSearchLogsOriginAndSignals`: non-passive search with `Origin: "mcp"` logs signals for non-profile results only.
  - Config: `TestRecallDefaults`, `TestRecallValidation` (0.0, 1.0, `max_items` 11, `deadline_ms` 100 rejected).
- [ ] **Step 2: Run** `go test ./internal/memory/ ./internal/config/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Relevance uses `rank.Signals{Similarity: Explanation.Similarity, FTS: FTSRank > 0, Trigram: TrigramRank > 0, VectorRank: Explanation.VectorRank}`.
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(memory): add calibrated prompt recall`.

### Task 6: `memorable calibrate`

**Files:**
- Create: `internal/memory/calibrate.go`, `internal/memory/calibrate_test.go`, `cmd/memorable/calibrate.go`, `cmd/memorable/calibrate_test.go`
- Modify: `cmd/memorable/root.go` (register under the maintenance group used by `reembed`)

**Interfaces:**
- Consumes: `LabeledResults`, `SaveCalibration`, `ActiveCalibration`, `rank.Fit`, `rank.Reliability`.
- Produces:
  - `type CalibrateResult struct { Coefficients rank.Calibration; Prior rank.Calibration; Positives, Negatives int; Saved bool; Reliability []rank.Bucket }`
  - `func (s *Service) Calibrate(ctx context.Context, dryRun bool) (CalibrateResult, error)`: prior = `ActiveCalibration` (error when none: `ErrValidation` "no default calibration for model X"); samples = `LabeledResults(now - 24h)`; fit with `lambda = 1.0` scaled by `1/len(samples)`; saves with source `usage` only when both counts are at least 20 and not `dryRun`.
  - CLI `memorable calibrate [--dry-run]` with `--output json` support: panel with counts, coefficients, saved or "not saved: need 20 relevant and 20 irrelevant labels, have N and M", and a reliability table (`RANGE`, `PREDICTED`, `OBSERVED`, `N`).

- [ ] **Step 1: Write failing tests:** `TestCalibrateBelowMinimumDoesNotSave`, `TestCalibrateSavesUsageFit`, `TestCalibrateDryRun`, `TestCalibrateNoPrior`; CLI `TestCalibrateCommandPlainOutput` (golden lines for counts and the not-saved message) and `TestCalibrateCommandJSON`.
- [ ] **Step 2: Run** `go test ./internal/memory/ ./cmd/memorable/ -run Calibrate`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cli): add memorable calibrate`.

### Task 7: Hook commands and setup

**Files:**
- Modify: `cmd/memorable/hook.go`, `cmd/memorable/hook_test.go`
- Modify: `internal/claudecfg/claudecfg.go`, `internal/claudecfg/claudecfg_test.go`
- Modify: `cmd/memorable/setup.go`, `cmd/memorable/setup_test.go`

**Interfaces:**
- Consumes: `Service.Recall`, `Service.LogPreload`, `Service.Preload`, `config.Recall`.
- Produces:
  - `hookInput` gains `Prompt string \`json:"prompt"\`` and `Source string \`json:"source"\``.
  - `memorable hook prompt-submit`: reads stdin (1 MiB cap), deadline `cfg.Recall.DeadlineMS`, prints the block from spec section 2 "Output" when items exist; nothing otherwise.
  - `hook session-start` additionally calls `LogPreload(sessionID, source, ids)`; a logging failure still prints the preload.
  - `claudecfg.PromptHookCommand = "memorable hook prompt-submit"`
  - `func AddHook(settings []byte, event, command string) ([]byte, error)`; `AddSessionStartHook` becomes a wrapper.
  - `func HasHook(settings []byte, event, command string) bool`
  - `func RenderRecall(searchID uuid.UUID, items []memory.RecalledMemory) string` in `internal/mcpserver` beside `RenderSummaries`, percent as `fmt.Sprintf("%d%%", int(math.Round(r*100)))`.
  - Setup: after the SessionStart question, `s.confirm("Also show relevant memories on every prompt (UserPromptSubmit hook, about 1 to 3 short lines when something matches)?", false)`.

- [ ] **Step 1: Write failing tests:**
  - `TestPromptSubmitPrintsRecall` (fake app with one 86% match) asserts the header line, `search_id="`, and `- 86% slug=`.
  - `TestPromptSubmitSilentOnBadInput`: empty stdin, invalid JSON, missing cwd, missing prompt → empty stdout, nil error.
  - `TestPromptSubmitDeadline`: embedder that blocks until ctx done, `DeadlineMS: 200` → empty stdout within 1 s.
  - `TestSessionStartLogsPreload`: preload event recorded with `hook_source`.
  - `TestAddHookIdempotent`, `TestHasHook`, setup `TestSetupAddsPromptHook`.
- [ ] **Step 2: Run** `go test ./cmd/memorable/ ./internal/claudecfg/ ./internal/mcpserver/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Verify against current Claude Code hook docs that plain stdout from UserPromptSubmit is added as context; if not, print `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"..."}}`. Also confirm SessionStart fires with `source: "compact"` and the same session id. Record both findings in the commit message.
- [ ] **Step 4: Run** `make test`. Expected: PASS. Then a manual smoke test: `echo '{"session_id":"s1","cwd":"'$PWD'","prompt":"which port does the memory database use and why"}' | go run ./cmd/memorable hook prompt-submit`.
- [ ] **Step 5: Commit** `feat(hook): add prompt-submit recall hook and setup step`.

### Task 8: Doctor, stats, and docs

**Files:**
- Modify: `internal/doctor/doctor.go`, `internal/doctor/doctor_test.go` (`Deps.ClaudeSettings func() ([]byte, error)`; `Probe.LatestCalibration(ctx, model string, dims int) (*memory.StoredCalibration, error)` or a store-level equivalent)
- Modify: `cmd/memorable/doctor.go` (wire deps), `internal/store/stats.go`, `cmd/memorable/read.go` (stats panel), `internal/webui/stats.go` only if it renders `store.Stats` fields generically
- Modify: `docs/design.md`, `README.md`, `docs/claude-md-snippet.md`

**Interfaces:**
- Doctor checks:
  - `recall hook`: ok "installed" or ok "not installed (optional)"; never fail.
  - `calibration`: ok "usage fit from N/M labels, <date>" or ok "built-in default"; fail with hint "run `make test-eval` to fit a default, or switch to a supported model" when neither exists.
- `store.Stats` gains `Recall RecallStats` for the last 30 days; plain stats adds a line `recall     N injections, M memories shown, K fetched`.

- [ ] **Step 1: Write failing tests:** `TestDoctorRecallHook`, `TestDoctorCalibrationDefault`, `TestDoctorCalibrationMissing`, store `TestStatsRecall` (integration), CLI stats golden update.
- [ ] **Step 2: Run** `make test` and `make test-integration`. Expected: FAIL on the new tests.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Update docs.** `docs/design.md`: section 6 (no tool change; note `search_id` from recall), section 7 (new "Prompt recall" and "Calibration" subsections, `access_events` kinds), section 9 (commands `hook prompt-submit`, `calibrate`), section 10 (`[recall]`), section 12 (eval prompt-shaped set), section 15 (resolve threshold item). `README.md`: recall hook in setup and a "Prompt recall" section with `memorable calibrate`. `docs/claude-md-snippet.md`: one line, "When a `<memory-recall>` block lists a relevant memory, fetch it with `memory_get`, passing its `search_id`."
- [ ] **Step 5: Run** `make test`, `make test-integration`, `make lint`. Expected: all PASS.
- [ ] **Step 6: Commit** `feat(doctor): report recall hook and calibration; docs for prompt recall`.

---

## Amendments from plan review (binding; they override the task text above)

1. Task 5 also modifies `cmd/memorable/app.go`: `memory.Settings` gets `Recall: cfg.Recall`.
2. Task 6: the prior is `rank.DefaultCalibration(model, dims)` only, never a stored usage fit; no default means `ErrValidation`.
3. Inject events always carry `Origin: "hook"`. `LabeledResults` selects `kind = 'inject' OR (kind = 'search' AND details->>'origin' = 'mcp')`.
4. Signals (search and inject) cover only ids in `Returned` and exclude `ScopeProfile` results. The purge-leak test near `internal/store/search_integration_test.go:300` also asserts no `details` still names the purged id; purge removes signal entries for the purged id.
5. Task 3 updates `LatestVersion == 4` in `bootstrap_integration_test.go`. Task 4 keeps `Store.LogSearch` as a thin wrapper over `LogEvent` (not in the interface) so existing tests keep compiling, or updates the four call sites.
6. `hook prompt-submit` loads config first, builds the deadline ctx from `cfg.Recall.DeadlineMS` (falling back to the default when config fails to load), then calls `d.build(ctx, ...)`. Test: a blocking `build` that honors ctx returns empty within the deadline.
7. Doctor `calibration`: ok when a usage fit or default exists; skipped ("no calibration for <model>; prompt recall disabled") when neither exists and the hook is not installed; fail only when the hook is installed and nothing matches. Skip when the probed schema is the profile schema.
8. Task 8 edits `internal/claudecfg/snippet.md` and mirrors the text into `docs/claude-md-snippet.md`.
9. Unit tests: truncation uses `strings.Repeat("é ", 300)`; slash test uses `/review the whole branch please`; the fakes gain input capture (`Embedder.Inputs()`) and full event capture (`Repo.LoggedEvents() []memory.Event`); setup is tested through dry-run output ("would ask: Also show relevant memories...") plus `AddHook`/`HasHook` unit tests.
10. `RenderRecall` always prints `id=`, escapes titles and snippets with the existing `attr` and `escapeBody` helpers, and prints `slug=` only when set.
11. Task 3 adds `TestMigrateUpFromV3`: migrate to v3, seed an old-shape `search` row, migrate to v4, the row survives with `details = '{}'`.
12. `EventDetails` gains `Model string` and `Dimensions int`; `LabeledResults(ctx, before, model, dims)` filters on them.
13. Non-passive `Search` computes signals regardless of `r.Explain`.
14. Integration tests add: inject event then `LogGet` with its id yields a positive label; preload and inject events leave `access_score` unchanged.
15. `RecallStats` lives only on `store.Stats` (not in `memory.Repository`), with json tags; the stats line reports 30-day totals and the average per day.
