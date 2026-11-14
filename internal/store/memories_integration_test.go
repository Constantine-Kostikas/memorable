//go:build integration

package store_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/rank"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/store/storetest"
)

type fixture struct {
	db    storetest.Database
	store *store.Store
	admin *pgx.Conn
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	require.NoError(t, storetest.Bootstrap(ctx, t, db, testConfig()))
	s, err := store.Open(ctx, storetest.DatabaseConfig(db), storetest.AppPassword, "mem_personal", "dev")
	require.NoError(t, err)
	t.Cleanup(s.Close)
	admin, err := pgx.Connect(ctx, db.AdminDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(ctx) })
	return fixture{db: db, store: s, admin: admin}
}

func newMem(title, content string) *memory.Memory {
	return &memory.Memory{
		ID: uuid.Must(uuid.NewV7()), Owner: "dev", Type: memory.TypeProject,
		Title: title, Content: content, Tags: []string{"db"},
		Source: memory.SourceUserStated, Status: memory.StatusActive, Version: 1,
		ContentHash: memory.ContentHash(title, content),
	}
}

func audit(action string) memory.AuditEntry {
	return memory.AuditEntry{Action: action, Cwd: "/p/x", Process: "1@t"}
}

func unitVec(dims int, hot int) []float32 {
	v := make([]float32, dims)
	v[hot%dims] = 1
	return v
}

func TestInsertAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := newMem("Database choice", "We use Postgres.")
	require.NoError(t, f.store.Insert(ctx, m, audit("create")))
	assert.False(t, m.CreatedAt.IsZero())

	got, err := f.store.GetByIDs(ctx, []uuid.UUID{m.ID})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "We use Postgres.", got[0].Content)
	assert.Equal(t, memory.ScopeGlobal, got[0].Scope)
	assert.Equal(t, []string{"db"}, got[0].Tags)

	var versions, audits int
	require.NoError(t, f.admin.QueryRow(ctx, "SELECT count(*) FROM mem_personal.memory_versions WHERE memory_id=$1 AND version=1", m.ID).Scan(&versions))
	assert.Equal(t, 1, versions)
	var hash []byte
	var details string
	require.NoError(t, f.admin.QueryRow(ctx, "SELECT count(*) OVER (), content_hash, details::text FROM mem_personal.audit_log WHERE memory_id=$1", m.ID).Scan(&audits, &hash, &details))
	assert.Equal(t, 1, audits)
	assert.Equal(t, m.ContentHash, hash)
	assert.NotContains(t, details, "Postgres")

	found, err := f.store.FindByHash(ctx, "dev", m.ContentHash)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, m.ID, found.ID)
	missing, err := f.store.FindByHash(ctx, "dev", []byte{1})
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestInsert_DuplicateSlugConflicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	a, b := newMem("a", "a"), newMem("b", "b")
	slug := "memorable/db"
	a.Slug, b.Slug = &slug, &slug
	require.NoError(t, f.store.Insert(ctx, a, audit("create")))
	require.ErrorIs(t, f.store.Insert(ctx, b, audit("create")), memory.ErrConflict)
}

func TestGetBySlugs_IncludesProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	local := newMem("local", "local content")
	s := "local-slug"
	local.Slug = &s
	require.NoError(t, f.store.Insert(ctx, local, audit("create")))
	_, err := f.admin.Exec(ctx, `INSERT INTO mem_profile.memories (id, owner, slug, type, title, content, source, status, content_hash)
		VALUES ($1, 'dev', 'style', 'user', 'Style', 'No em dashes.', 'user_stated', 'active', '\x00')`, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)

	got, err := f.store.GetBySlugs(ctx, "dev", []string{"local-slug", "style", "missing"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	scopes := map[string]memory.Scope{}
	for _, m := range got {
		scopes[*m.Slug] = m.Scope
	}
	assert.Equal(t, memory.ScopeGlobal, scopes["local-slug"])
	assert.Equal(t, memory.ScopeProfile, scopes["style"])
}

func TestUpdate_ExpectedVersionConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := newMem("t", "c")
	require.NoError(t, f.store.Insert(ctx, m, audit("create")))
	one := 1
	title := "new"
	got, err := f.store.Update(ctx, m.ID, &one, memory.Patch{Title: &title}, memory.ContentHash(title, "c"), audit("update"))
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version)
	assert.Equal(t, "new", got.Title)

	_, err = f.store.Update(ctx, m.ID, &one, memory.Patch{Title: &title}, nil, audit("update"))
	require.ErrorIs(t, err, memory.ErrConflict)
	_, err = f.store.Update(ctx, uuid.Must(uuid.NewV7()), nil, memory.Patch{Title: &title}, nil, audit("update"))
	require.ErrorIs(t, err, memory.ErrNotFound)
}

func TestUpdate_ClearsEmbeddingOnContentChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := newMem("t", "c")
	require.NoError(t, f.store.Insert(ctx, m, audit("create")))
	require.NoError(t, f.store.SetEmbedding(ctx, m.ID, 1, unitVec(768, 1), "embeddinggemma"))

	pinned := true
	_, err := f.store.Update(ctx, m.ID, nil, memory.Patch{Pinned: &pinned}, nil, audit("update"))
	require.NoError(t, err)
	var has bool
	require.NoError(t, f.admin.QueryRow(ctx, "SELECT embedding IS NOT NULL FROM mem_personal.memories WHERE id=$1", m.ID).Scan(&has))
	assert.True(t, has, "pinning keeps the embedding")

	content := "changed"
	_, err = f.store.Update(ctx, m.ID, nil, memory.Patch{Content: &content}, memory.ContentHash("t", content), audit("update"))
	require.NoError(t, err)
	require.NoError(t, f.admin.QueryRow(ctx, "SELECT embedding IS NOT NULL FROM mem_personal.memories WHERE id=$1", m.ID).Scan(&has))
	assert.False(t, has, "content change clears the embedding")

	require.NoError(t, f.store.SetEmbedding(ctx, m.ID, 1, unitVec(768, 2), "embeddinggemma"))
	require.NoError(t, f.admin.QueryRow(ctx, "SELECT embedding IS NOT NULL FROM mem_personal.memories WHERE id=$1", m.ID).Scan(&has))
	assert.False(t, has, "a stale version's embedding is ignored")
}

func TestSetStatus_SoftDeleteAndRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := newMem("t", "c")
	require.NoError(t, f.store.Insert(ctx, m, audit("create")))
	require.NoError(t, f.store.SetStatus(ctx, m.ID, memory.StatusDeleted, "delete", audit("delete")))

	got, err := f.store.GetByIDs(ctx, []uuid.UUID{m.ID})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, memory.StatusDeleted, got[0].Status)
	assert.NotNil(t, got[0].DeletedAt)

	restored, err := f.store.Restore(ctx, m.ID, nil, audit("restore"))
	require.NoError(t, err)
	assert.Equal(t, memory.StatusActive, restored.Status)
	assert.Nil(t, restored.DeletedAt)
}

func TestRestore_PriorVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := newMem("t", "original")
	require.NoError(t, f.store.Insert(ctx, m, audit("create")))
	c := "edited"
	_, err := f.store.Update(ctx, m.ID, nil, memory.Patch{Content: &c}, memory.ContentHash("t", c), audit("update"))
	require.NoError(t, err)

	one := 1
	got, err := f.store.Restore(ctx, m.ID, &one, audit("restore"))
	require.NoError(t, err)
	assert.Equal(t, "original", got.Content)
	assert.Equal(t, 3, got.Version)
	assert.Equal(t, memory.ContentHash("t", "original"), got.ContentHash)

	hist, err := f.store.History(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, hist, 3)
	assert.Equal(t, []string{"create", "update", "restore"}, []string{hist[0].ChangeKind, hist[1].ChangeKind, hist[2].ChangeKind})
}

func TestInsertSuperseding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	old := newMem("old", "MariaDB")
	require.NoError(t, f.store.Insert(ctx, old, audit("create")))
	nm := newMem("new", "Postgres")
	require.NoError(t, f.store.InsertSuperseding(ctx, nm, old.ID, audit("supersede")))

	got, err := f.store.GetByIDs(ctx, []uuid.UUID{old.ID})
	require.NoError(t, err)
	assert.Equal(t, memory.StatusSuperseded, got[0].Status)
	links, err := f.store.LinksFor(ctx, []uuid.UUID{nm.ID})
	require.NoError(t, err)
	assert.Equal(t, []memory.Link{{From: nm.ID, To: old.ID, Kind: memory.LinkSupersedes}}, links)
}

func TestRecordAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := newMem("t", "c")
	require.NoError(t, f.store.Insert(ctx, m, audit("create")))
	hl := 30 * 24 * time.Hour
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	project := "github.com/a/b"

	require.NoError(t, f.store.RecordAccess(ctx, m.ID, &project, 1, t0, hl))
	require.NoError(t, f.store.RecordAccess(ctx, m.ID, &project, 1, t0.Add(hl), hl))

	var score, key float64
	var count int64
	require.NoError(t, f.admin.QueryRow(ctx, "SELECT access_score, rank_key, access_count FROM mem_personal.memories WHERE id=$1", m.ID).Scan(&score, &key, &count))
	assert.InDelta(t, 1.5, score, 1e-6)
	assert.InDelta(t, rank.Key(1.5, t0.Add(hl), hl), key, 1e-6)
	assert.Equal(t, int64(2), count)

	var pscore float64
	require.NoError(t, f.admin.QueryRow(ctx, "SELECT access_score FROM mem_personal.memory_project_access WHERE memory_id=$1 AND project=$2", m.ID, project).Scan(&pscore))
	assert.InDelta(t, 1.5, pscore, 1e-6)

	require.NoError(t, f.store.RecordAccess(ctx, uuid.Must(uuid.NewV7()), nil, 1, t0, hl), "unknown or profile ids are ignored")
	assert.False(t, math.IsNaN(score))
}

func TestFeedbackLinksAndEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	a, b := newMem("a", "a"), newMem("b", "b")
	require.NoError(t, f.store.Insert(ctx, a, audit("create")))
	require.NoError(t, f.store.Insert(ctx, b, audit("create")))

	require.NoError(t, f.store.AddFeedback(ctx, a.ID, memory.FeedbackWrong, "outdated port", nil))
	l := memory.Link{From: a.ID, To: b.ID, Kind: memory.LinkRelated}
	require.NoError(t, f.store.AddLink(ctx, l))
	require.NoError(t, f.store.AddLink(ctx, l), "adding twice is idempotent")
	links, err := f.store.LinksFor(ctx, []uuid.UUID{b.ID})
	require.NoError(t, err)
	assert.Len(t, links, 1)
	require.NoError(t, f.store.RemoveLink(ctx, l))
	links, err = f.store.LinksFor(ctx, []uuid.UUID{b.ID})
	require.NoError(t, err)
	assert.Empty(t, links)

	sid, err := f.store.LogSearch(ctx, "postgres", nil, []uuid.UUID{a.ID, b.ID})
	require.NoError(t, err)
	require.NoError(t, f.store.LogGet(ctx, a.ID, &sid, nil))
	var n int
	require.NoError(t, f.admin.QueryRow(ctx, "SELECT count(*) FROM mem_personal.access_events").Scan(&n))
	assert.Equal(t, 2, n)
}

func TestOpen_DatabaseDown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := testConfig().Database
	cfg.Port = 1
	s, err := store.Open(ctx, cfg, "x", "mem_personal", "dev")
	require.NoError(t, err)
	defer s.Close()
	require.ErrorIs(t, s.Ping(ctx), memory.ErrUnavailable)
}

func TestStats(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	a := newMem("a", "a")
	project := "github.com/a/b"
	a.Project = &project
	require.NoError(t, f.store.Insert(ctx, a, audit("create")))
	require.NoError(t, f.store.Insert(ctx, newMem("b", "b"), audit("create")))
	require.NoError(t, f.store.SetEmbedding(ctx, a.ID, 1, unitVec(768, 0), "t"))
	require.NoError(t, f.store.EnqueueEmbedding(ctx, a.ID, 1))
	st, err := f.store.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, st.Total)
	assert.Equal(t, 1, st.Embedded)
	assert.Equal(t, 1, st.PendingJobs)
	assert.Equal(t, 2, st.ByStatus["active"])
	assert.Equal(t, 1, st.ByProject["(global)"])
	assert.Equal(t, 1, st.ByProject[project])
}

func TestExtensionsAndSchemaVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	ext, err := f.store.Extensions(ctx)
	require.NoError(t, err)
	assert.Equal(t, "0.8.7", ext["vector"])
	assert.NotEmpty(t, ext["pg_trgm"])
	for _, schema := range []string{"mem_personal", store.ProfileSchema} {
		v, err := f.store.SchemaVersion(ctx, schema)
		require.NoError(t, err)
		assert.Equal(t, store.LatestVersion(store.SchemaContext), v, schema)
	}
	v, err := f.store.SchemaVersion(ctx, "mem_missing")
	require.NoError(t, err)
	assert.Zero(t, v)
}
