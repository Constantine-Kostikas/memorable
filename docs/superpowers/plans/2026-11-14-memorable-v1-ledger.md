# SDD ledger — plan: /home/dev/development/projects/memorable/docs/superpowers/plans/2026-11-14-memorable-v1.md
Spec: docs/design.md
Setup: Ruling: work on branch feat/v1 in the main checkout instead of a separate worktree — user said "go native" right after creating the repo; single-developer repo, nothing else checked out — cost if wrong: none beyond branch switch.
Pre-flight:
- T2 errors <- T4 scope.ErrNoContext: plan already orders the alias; Ruling: implement scope.ErrNoContext stub inside Task 2 (scope/errors.go) so T2 compiles, T4 fills scope — cost: none.
- T9/T10 Repository <- T12/T13 Service: T13 uses Hydrate (T10) and JobRepository (T10); T12 uses EnqueueEmbedding (T10) though T12 precedes nothing broken (T10 before T12) — consistent.
- T12 fakes <- T15 tests: T15 needs fakes exported; Ruling: put fakes in internal/memory/memorytest from the start (T12) instead of fakes_test.go — avoids a move later — cost: none.
- T11 Embedder <- T12/T14: DocumentText defined in memory (T11) consumed by worker (T14) — consistent.
- T16 app <- T17-T21 commands: all commands use buildApp — consistent.
- T8 storetest.Start <- T9,T10,T14,T18,T22: consistent.
Task 1: Ruling: pinned govulncheck v1.3.0 and gosec v2.28.0 as tool deps — latest releases require Go 1.26 and Q15 fixed Go 1.25 — cost if wrong: older scanners miss newer checks until Go is upgraded.
Task 1: Ruling: CLAUDE.md directive uses a colon instead of the template's em dash — user's global no-em-dash rule — cost: none.
Task 1: complete (commits c88221e..4b38cbd, tests: go test ./... → ok  	github.com/constantine-kostikas/memorable/cmd/memorable	0.004s)
Task 2: complete (commits 4b38cbd..f648664, tests: go test ./... → ?   	github.com/constantine-kostikas/memorable/internal/scope	[no test files])
Task 2: Ruling: validation also rejects empty/whitespace title or content, invalid UTF-8, and duplicate tags — limits alone would accept them and they make useless rows — cost if wrong: a caller must trim input.
Task 3: Ruling: Validate also requires absolute context paths, port range, and positive limits; added ContextNames() and ExpandPath() helpers — needed by later tasks (setup, context add) — cost: none.
Task 3: complete (commits f648664..391cf7c, tests: go test ./... → ?   	github.com/constantine-kostikas/memorable/internal/scope	[no test files])
Task 4: complete (commits 391cf7c..1b70913, tests: go test ./... → ok  	github.com/constantine-kostikas/memorable/internal/scope	0.019s)
Task 5: Ruling: added Account(user, host, port, db) helper for the plan's account format — keeps the format in one place — cost: none.
Task 5: complete (commits 1b70913..0cc79ce, tests: go test ./... → ok  	github.com/constantine-kostikas/memorable/internal/secret	(cached))
Task 6: complete (commits 0cc79ce..2e88d7c, tests: go test ./... → ok  	github.com/constantine-kostikas/memorable/internal/secret	(cached))
Task 6: Ruling: rank.RankKey renamed rank.Key — revive stutter check; later tasks use rank.Key — cost: none.
Task 7: Ruling: scan.ScanInjection renamed scan.Injection (revive stutter); later tasks use scan.Injection — cost: none.
Task 7: Ruling: added rule url-credentials (scheme://user:pass@host) — gitleaks has no generic DB URL rule and the plan's test requires it — cost: false positives on documented example URLs, approvable.
Task 7: Ruling: gitleaks rules honored: keywords, regex, entropy, secretGroup, allowlists (regexes with secret/match/line target, stopwords, AND condition); path-only rules skipped — cost: rare gitleaks false positives that path allowlists would suppress.
Task 7: complete (commits 2e88d7c..cae18c3, tests: go test ./... → ok  	github.com/constantine-kostikas/memorable/internal/secret	(cached))
Task 8: Ruling: pinned goose v3.26.0 and testcontainers-go v0.40.0 — newest releases require Go 1.26 (fedora updates has golang 1.26.10); Q15 fixed Go 1.25 so stay compatible and recommend a Go 1.26 upgrade at the end — cost if wrong: older deps until upgrade.
Task 8: Ruling: (supersedes the Task 1 and Task 8 pinning rulings) user approved upgrading to Go 1.26; go.mod is go 1.26.0 and all deps use current releases — cost: none.
Task 8: Ruling: grants applied in Go after migrations (Migrate takes appUser) instead of in SQL files — the app role name is configurable — cost: none.
Task 8: Ruling: storetest adds NewDatabase/Bootstrap/DatabaseConfig helpers and serializes role bootstrap with a mutex — roles are cluster-wide — cost: none.
Task 8: Ruling: migration 00002 also adds a content trigram index on context schemas (for NearestByTrigram) and memory_versions.change_kind allows 'status' for status-only changes — cost: none.
Task 8: complete (commits cae18c3..bbbf7d6, tests: go test -tags=integration ./... → ?   	github.com/constantine-kostikas/memorable/internal/store/storetest	[no test files])
Task 9: Ruling: store.Open takes an owner argument (Open(ctx, db, password, schema, owner)) — feedback, links, events, and audit rows need it — cost: none.
Task 9: Ruling: SetEmbedding applies when the memory's current content_hash equals the hash of the text at the given version (not version equality) — status changes bump the version and would otherwise drop pending embeddings — cost: none.
Task 9: Ruling: SetStatus bumps the version and writes a version row; Restore(nil) on a non-deleted memory returns ErrValidation; duplicate slug returns ErrConflict — cost: none.
Task 9: complete (commits bbbf7d6..80b495f, tests: go test -tags=integration ./internal/store/ → ok  	github.com/constantine-kostikas/memorable/internal/store	(cached))
Task 10: Ruling: Review takes a halfLife argument (Review(ctx, now, staleAfter, halfLife)) — stale detection compares rank_key against ln(0.5)+λ·now — cost: none.
Task 10: Ruling: trigram candidates use word_similarity with the <% operator (query as a fuzzy word inside the title) instead of similarity/% — short queries against long titles never pass plain similarity — cost: none.
Task 10: Ruling: Greek test asserts trigram and vector retrieval only — the english FTS config keeps unstemmed Greek tokens, so exact-form Greek queries do match FTS (plan premise wrong) — cost: none.
Task 10: Ruling: EXPLAIN test proves the HNSW index is usable with enable_seqscan off — at 10,000 rows Postgres correctly prefers a sequential scan even for an unfiltered ORDER BY embedding query; FTS, trigram, and top choose their indexes unaided — cost: none at personal scale.
Task 10: Ruling: List covers context memories only (profile via profile commands); OpenAdmin takes owner; Admin adds ProfileList/Ping; Top returns pinned (context + profile) then rank_key order — cost: none.
Task 10: complete (commits 80b495f..b51f3b3, tests: go test -tags=integration ./internal/store/ → ok  	github.com/constantine-kostikas/memorable/internal/store	34.092s)
Task 11: Ruling: exported config.CheckEmbeddingURL so the embedder reuses the loopback rule — cost: none.
Task 11: complete (commits b51f3b3..0d7b958, tests: go test ./... → ?   	github.com/constantine-kostikas/memorable/internal/store/storetest	[no test files])
Task 12: Ruling: fakes live in internal/memory/memorytest (exported) and service tests use package memory_test — reused by MCP tests — cost: none.
Task 12: Ruling: rate limiter burst = writes_per_minute (not MaxBatch) — 'N writes per minute' semantics; batches still capped at 20 — cost: none.
Task 12: Ruling: validation failures are per-item OutcomeRejected results (not batch errors); quarantined items are stored without duplicate checks; secret and injection scans cover title, content, and tags — cost: none.
Task 12: complete (commits 0d7b958..f6f768b, tests: go test -race ./internal/memory/... → ?   	github.com/constantine-kostikas/memorable/internal/memory/memorytest	[no test files])
Task 13: Ruling: the relevance cutoff treats trigram matches as text matches (keeps FTS or trigram hits; drops only vector-only hits below min_similarity) — otherwise typo searches are lost whenever the vector score is low — cost: slightly more trigram noise.
Task 13: Ruling: Fetched gains Ref (requested id or slug) and Err (per-item not found or quarantined); quarantined fetches carry only ID and Status — cost: none.
Task 13: Ruling: profile memories are read-only through the service (Update/Delete/Feedback return ErrReadOnly); List never returns quarantined rows and is capped at 100 per page — cost: none.
Task 13: complete (commits f6f768b..5b28631, tests: go test -race ./internal/memory/... → ?   	github.com/constantine-kostikas/memorable/internal/memory/memorytest	[no test files])
Task 14: Ruling: worker.New takes a narrow EmbeddingStore (SetEmbedding only) instead of the full memory.Repository; RunOnce exported for reembed draining — cost: none.
Task 14: Ruling: goleak ignores the testcontainers reaper goroutine (process-lifetime) — cost: none.
Task 14: complete (commits 5b28631..51953fc, tests: go test -race -tags=integration ./internal/worker/ → ok  	github.com/constantine-kostikas/memorable/internal/worker	7.212s)
Task 15: Ruling: mcpserver.New takes a Backend func(ctx) (*memory.Service, error) instead of a service plus StartupErr — serve resolves context lazily after the MCP handshake (Task 16), and errors surface per tool — cost: none.
Task 15: Ruling: tool results are text only (no structuredContent); wrappers escape any '<memory' or '</memory' prefix (covers memory-results, memory-version, and similar) — cost: none.
Task 15: Ruling: memory.GetRequest gains Passive (resource reads are not counted); slug conflicts on write become per-item rejections; description budget test measures descriptions plus input schemas under 12,000 chars — cost: none.
Task 15: complete (commits 51953fc..fe2b46d, tests: go test -race ./internal/... → ok  	github.com/constantine-kostikas/memorable/internal/worker	(cached))
Task 16: Ruling: pulled the internal/ui Printer (with its tests) forward from Task 17 — context output needed it — cost: none; Task 17 adds the read commands.
Task 16: Ruling: MEMORABLE_KEYRING=off forces the credentials file (secret.WithoutKeyring) — tests and headless machines must never touch the desktop keyring — cost: none.
Task 16: Ruling: serve retries service construction on every tool call until it succeeds (not sync.OnceValues) — a database started after Claude Code should not require restarting the session — cost: a failed connect attempt per tool call while down.
Task 16: Ruling: ListRoots runs in a goroutine from the initialized handler, tools wait up to 5s for it — calling back to the client inside the handler can block the message loop — cost: none.
Task 16: complete (commits fe2b46d..437a188, tests: go test -race ./... → ok  	github.com/constantine-kostikas/memorable/internal/worker	(cached))
Task 17: Ruling: CLI get is passive (human reads do not count toward ranking); JSON output uses snake_case view structs; positional-arg and unknown-command errors exit 2 — cost: none.
Task 17: complete (commits 437a188..b5688ad, tests: go test -race ./... → ok  	github.com/constantine-kostikas/memorable/internal/worker	(cached))
Task 18: Ruling: added Service.Activate (review action for flagged/unverified), Store.EnqueueAll/EmbeddingDimensions, store.ResizeEmbeddings, Admin.ProfileSetEmbedding — reembed and review needed them; TestActivate was written in the same step as Activate (RED not observed separately) — cost: low.
Task 18: Ruling: migrate generates and stores the app password when none exists; admin commands take --context (default: current directory's context) — cost: none.
Task 18: Ruling: review is interactive only on a TTY; otherwise it prints the queue counts; quarantined content is shown only by 'approve --list' (admin) — cost: none.
Task 18: complete (commits b5688ad..17ae82c, tests: go test -race -tags=integration ./cmd/... → ok  	github.com/constantine-kostikas/memorable/cmd/memorable	(cached))
Task 19: Ruling: the app role may SELECT goose_db_version (was revoked) so doctor can check schema versions without the admin password; Store gains Extensions/SchemaVersion — cost: none.
Task 19: complete (commits 17ae82c..fc7d327, tests: go test ./internal/doctor/ ./cmd/... → ok  	github.com/constantine-kostikas/memorable/cmd/memorable	0.979s)
Task 20: Ruling: import uses a dedicated service with memory.WithoutRateLimit (human-driven bulk import) — the 60/minute limit would make large imports take many minutes — cost: none; still validated and scanned.
Task 20: Ruling: imports skip deleted and superseded memories; Markdown export uses a Claude-compatible front matter (name, description, type) plus tags, pinned, scope; unknown types import as reference — cost: none.
Task 20: Ruling: the committed fixture is a copy of a real Claude memory file with its session id replaced by zeros — cost: none.
Task 20: complete (commits fc7d327..3fdb406, tests: go test -race ./internal/transfer/ ./internal/backup/ ./cmd/... → ok  	github.com/constantine-kostikas/memorable/cmd/memorable	(cached))
Task 21: IN PROGRESS — claudecfg, dockerpg, scheduler, and deps committed; setup command not written yet; cmd/memorable/setup_test.go is uncommitted (its RED tests). Resume: implement cmd/memorable/setup.go (steps 1-11, --dry-run), then Tasks 22-23 and the final review.
Task 21: Ruling: setup order is prerequisites, container, contexts/config, database, model, MCP, CLAUDE.md, settings (permissions + optional hook in one write), backups, doctor — migrations need the contexts first — cost: none.
Task 21: Ruling: snippet canonical copy is internal/claudecfg/snippet.md (embedded); docs/claude-md-snippet.md mirrors it, enforced by a test; settings.json is re-printed in Claude Code's own two-space style; diffs show three lines of context — cost: none.
Task 21: Ruling: Step 5 manual check (real setup, claude mcp get) left to the user — it writes ~/.claude files and asks for confirmation interactively — cost: setup is verified by dry run and unit tests only.
Task 21: complete (commits 3fdb406..8c928c3, tests: go test -race ./internal/claudecfg/ ./internal/dockerpg/ ./internal/scheduler/ ./cmd/... → ok  	github.com/constantine-kostikas/memorable/cmd/memorable	(cached))
Task 22: Ruling: eval results (103 queries, 50 memories): embeddinggemma recall@5 0.855 / MRR 0.844 / FP 0.00 at min_similarity 0.35; qwen3-embedding:0.6b recall@5 0.860 / MRR 0.771 / FP 1.00 — default model stays embeddinggemma.
Task 22: Ruling: min_similarity 0.35 -> 0.30 (recall@5 0.930, MRR 0.909, FP 0.00; 0.28 gives 0.973/0.936 but FP 0.20) and conflict_threshold 0.80 -> 0.75 (measured: paraphrase dup 0.85, contradiction 0.80, update 0.88, related 0.40-0.46) — cost if wrong: slightly more needs_decision prompts.
Task 22: Ruling: 103 queries instead of 100 (test requires at least 100) — cost: none.
Task 22: complete (commits 8c928c3..d928eb0, tests: go test -tags=eval -run TestEval -count=1 ./internal/memory/ → ok  	github.com/constantine-kostikas/memorable/internal/memory	7.362s)
Task 23: Ruling: Linux arm64 archives are named aarch64 to match uname -m; release archives include the README; changelog excludes docs/build/ci/test commits — cost: none. Step 5 (repo creation) was already done at the user's request before execution; GPG secrets must be added by the user with gh secret set.
Task 23: complete (commits d928eb0..38d7e87, tests: go test -race ./... → ok  	github.com/constantine-kostikas/memorable/internal/worker	(cached))
Final: fixed Critical 1 (Claude could release quarantined memories via feedback, delete/restore, update, supersede) — TestQuarantine_CannotBeReleasedByClaude RED→GREEN, plus TestQuarantineGuards (store), suite 17/17 packages
Final: fixed Critical 2 (memory_history leaked quarantined text) — covered by TestQuarantine_CannotBeReleasedByClaude RED→GREEN
Final: fixed Important 8 (quarantined write still superseded its target) — TestQuarantinedWriteDoesNotSupersede RED→GREEN
Final: fixed Important 9 (injection scanner missed tag characters, soft hyphen, extra whitespace) — TestInjection_Evasions RED→GREEN
Final: fixed Important 5 partly (Approve now queues an embedding) — TestAdmin_PurgeApproveProfile extended
Final: fixed Important 1 (SessionStart hook output not wrapped as data) — TestHook_PrintsPreload RED→GREEN
Final: fixed Important 2 (quarantining update was two transactions) — TestUpdate_QuarantineIsAtomic RED→GREEN
Final: fixed Important 3 (concurrent update stored a hash and scans for text never written) — TestUpdate_ConcurrentChangeConflicts RED→GREEN; updates without expected_version now pin the version read
Final: fixed Important 6 partly (vector query errors broke search and writes) — TestSearch_VectorQueryErrorFallsBack RED→GREEN
Final: fixed Important 4 (a stale job completion finished a newer job) — TestJobs_StaleVersionNotCompleted RED→GREEN; Complete/FailJob filter by version and SetEmbedding reports stale vectors
Final: fixed Important 5 (given-up and missing embeddings never retried) — TestRequeueMissing RED→GREEN; serve and reembed requeue missing embeddings, approve enqueues
Final: fixed Important 6 (model change resized only the current context) — TestReembed_DimensionChange extended RED→GREEN
Final: fixed Important 7 (serve hung after the client disconnected) — TestServe_ExitsWhenClientDisconnects RED→GREEN
Final: suite green: unit and integration (-race), 18 packages
Final review: fresh reviewer (opus subagent): 2 critical, 9 important, 7 minor; all critical and important fixed above.
Final: minor (deferred): mid-session connection loss maps to 'internal error' instead of the doctor hint (map net.Error and pgconn.SafeToRetry to ErrUnavailable).
Final: minor (deferred): spec section 6 says results carry structured JSON; ruling T15 made them text only; amend the spec.
Final: minor (deferred): memory_search stores the raw query in access_events without a secret scan.
Final: minor (deferred): searches from a read-only fallback context still write access events and scores.
Final: minor (deferred): setup passes POSTGRES_PASSWORD on the docker argv (visible in ps to local users during container creation).
Final: minor (deferred): memory_write drops per-item results already committed when a later item fails at batch level.
Final: minor (deferred): serve holds its lock across a database ping while the database is down, serializing parallel tool calls.
Final: Ruling: context isolation rests on search_path with one app role (reviewer declined to judge) — matches the spec's single-role design; per-context roles would be stronger — cost if wrong: a SQL injection could cross contexts, but all SQL is parameterized.
Final: Ruling: gitleaks rule fidelity, setup flows beyond file writes, transfer edge cases, scheduler, GoReleaser, and eval thresholds were not reviewed in depth (reviewer declined) — each has its own tests or manual verification in this session — cost if wrong: bugs in less-reviewed areas.
