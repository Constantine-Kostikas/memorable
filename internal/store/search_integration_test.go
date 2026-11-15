//go:build integration

package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/store/storetest"
)

func ids(cs []memory.Candidate) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func insertWith(t *testing.T, f fixture, title, content string, mutate func(*memory.Memory), vecHot int) *memory.Memory {
	t.Helper()
	ctx := context.Background()
	m := newMem(title, content)
	if mutate != nil {
		mutate(m)
	}
	require.NoError(t, f.store.Insert(ctx, m, audit("create")))
	if vecHot >= 0 {
		require.NoError(t, f.store.SetEmbedding(ctx, m.ID, 1, unitVec(768, vecHot), "test"))
	}
	return m
}

func TestCandidates_Filters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	projA, projB := "github.com/a/a", "github.com/b/b"
	now := time.Now()
	past := now.Add(-time.Hour)

	global := insertWith(t, f, "Postgres global", "postgres everywhere", nil, 0)
	inA := insertWith(t, f, "Postgres in A", "postgres project a", func(m *memory.Memory) { m.Project = &projA }, 0)
	inB := insertWith(t, f, "Postgres in B", "postgres project b", func(m *memory.Memory) { m.Project = &projB }, 0)
	expired := insertWith(t, f, "Postgres expired", "postgres old", func(m *memory.Memory) { m.ExpiresAt = &past }, 0)
	quarantined := insertWith(t, f, "Postgres quarantined", "postgres q", func(m *memory.Memory) { m.Status = memory.StatusQuarantined }, 0)
	deleted := insertWith(t, f, "Postgres deleted", "postgres d", nil, 0)
	require.NoError(t, f.store.SetStatus(ctx, deleted.ID, memory.StatusDeleted, "delete", audit("delete")))
	flagged := insertWith(t, f, "Postgres flagged", "postgres f", func(m *memory.Memory) { m.Status = memory.StatusFlagged }, 0)
	profileID := uuid.Must(uuid.NewV7())
	_, err := f.admin.Exec(ctx, `INSERT INTO mem_profile.memories (id, owner, type, title, content, source, status, content_hash, embedding)
		VALUES ($1, 'dev', 'user', 'Postgres profile', 'postgres preference', 'user_stated', 'active', '\x00', $2)`,
		profileID, vecLiteral(768, 0))
	require.NoError(t, err)

	filter := memory.Filter{Owner: "dev", Project: &projA, Now: now}
	want := []uuid.UUID{global.ID, inA.ID, profileID}
	excluded := []uuid.UUID{inB.ID, expired.ID, quarantined.ID, deleted.ID, flagged.ID}

	vec, err := f.store.CandidatesVector(ctx, unitVec(768, 0), filter, 50)
	require.NoError(t, err)
	fts, err := f.store.CandidatesFTS(ctx, "postgres", filter, 50)
	require.NoError(t, err)
	tri, err := f.store.CandidatesTrigram(ctx, "Postgres", filter, 50)
	require.NoError(t, err)
	for name, got := range map[string][]uuid.UUID{"vector": ids(vec), "fts": ids(fts), "trigram": ids(tri)} {
		assert.ElementsMatch(t, want, got, name)
		for _, x := range excluded {
			assert.NotContains(t, got, x, name)
		}
	}
	assert.InDelta(t, 1.0, vec[0].Similarity, 1e-6)

	wide := filter
	wide.AllProjects = true
	wide.IncludeFlagged = true
	all, err := f.store.CandidatesFTS(ctx, "postgres", wide, 50)
	require.NoError(t, err)
	assert.Contains(t, ids(all), inB.ID)
	assert.Contains(t, ids(all), flagged.ID)
}

func TestCandidates_Greek(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	g := insertWith(t, f, "Βάσεις δεδομένων", "Χρησιμοποιούμε Postgres για όλες τις βάσεις δεδομένων.", nil, 7)
	filter := memory.Filter{Owner: "dev", Now: time.Now()}

	tri, err := f.store.CandidatesTrigram(ctx, "βάσεις", filter, 10)
	require.NoError(t, err)
	assert.Contains(t, ids(tri), g.ID)
	vec, err := f.store.CandidatesVector(ctx, unitVec(768, 7), filter, 10)
	require.NoError(t, err)
	assert.Contains(t, ids(vec), g.ID)
}

func TestNearest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := insertWith(t, f, "Ports", "The memorable database listens on port 5434 on localhost.", nil, 3)
	near, err := f.store.NearestByVector(ctx, "dev", unitVec(768, 3), 5)
	require.NoError(t, err)
	require.NotEmpty(t, near)
	assert.Equal(t, m.ID, near[0].ID)
	tri, err := f.store.NearestByTrigram(ctx, "dev", "The memorable database listens on port 5434 on localhost now.", 5)
	require.NoError(t, err)
	require.NotEmpty(t, tri)
	assert.Equal(t, m.ID, tri[0].ID)
	assert.Greater(t, tri[0].Similarity, 0.8)
}

func TestHydrate_UsesProjectScore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := insertWith(t, f, "t", "c", nil, -1)
	project := "github.com/a/a"
	hl := 30 * 24 * time.Hour
	now := time.Now()
	require.NoError(t, f.store.RecordAccess(ctx, m.ID, nil, 5, now, hl))
	require.NoError(t, f.store.RecordAccess(ctx, m.ID, &project, 1, now, hl))

	got, err := f.store.Hydrate(ctx, []uuid.UUID{m.ID}, &project)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.InDelta(t, 1.0, got[0].AccessScore, 1e-6, "project score wins")
	got, err = f.store.Hydrate(ctx, []uuid.UUID{m.ID}, nil)
	require.NoError(t, err)
	assert.InDelta(t, 6.0, got[0].AccessScore, 1e-3, "global score without project")
}

func TestTop_Order(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	hl := 30 * 24 * time.Hour
	now := time.Now()
	low := insertWith(t, f, "low", "low", nil, -1)
	high := insertWith(t, f, "high", "high", nil, -1)
	pinned := insertWith(t, f, "pinned", "pinned", func(m *memory.Memory) { m.Pinned = true }, -1)
	require.NoError(t, f.store.RecordAccess(ctx, low.ID, nil, 1, now, hl))
	require.NoError(t, f.store.RecordAccess(ctx, high.ID, nil, 9, now, hl))

	got, err := f.store.Top(ctx, memory.Filter{Owner: "dev", Now: now}, 10)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []uuid.UUID{pinned.ID, high.ID, low.ID}, []uuid.UUID{got[0].ID, got[1].ID, got[2].ID})
}

func TestList_Paginates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	for i := range 5 {
		insertWith(t, f, "item", string(rune('a'+i)), nil, -1)
	}
	filter := memory.Filter{Owner: "dev", Now: time.Now()}
	page1, next, err := f.store.List(ctx, filter, nil, nil, 3)
	require.NoError(t, err)
	assert.Len(t, page1, 3)
	require.NotNil(t, next)
	page2, next2, err := f.store.List(ctx, filter, nil, next, 3)
	require.NoError(t, err)
	assert.Len(t, page2, 2)
	assert.Nil(t, next2)
}

func TestReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	now := time.Now()
	past := now.Add(-time.Hour)
	insertWith(t, f, "flagged", "f", func(m *memory.Memory) { m.Status = memory.StatusFlagged }, -1)
	insertWith(t, f, "expired", "e", func(m *memory.Memory) { m.ExpiresAt = &past }, -1)
	insertWith(t, f, "unverified", "u", func(m *memory.Memory) { m.Status = memory.StatusUnverified; m.Source = memory.SourceToolOutput }, -1)
	insertWith(t, f, "quarantined", "q", func(m *memory.Memory) { m.Status = memory.StatusQuarantined }, -1)
	stale := insertWith(t, f, "stale", "s", nil, -1)
	_, err := f.admin.Exec(ctx, `UPDATE mem_personal.memories SET created_at = now() - interval '90 days' WHERE id = $1`, stale.ID)
	require.NoError(t, err)
	a := insertWith(t, f, "a", "a", nil, -1)
	b := insertWith(t, f, "b", "b", nil, -1)
	require.NoError(t, f.store.AddLink(ctx, memory.Link{From: a.ID, To: b.ID, Kind: memory.LinkContradicts}))

	q, err := f.store.Review(ctx, now, 60*24*time.Hour, 30*24*time.Hour)
	require.NoError(t, err)
	assert.Len(t, q.Flagged, 1)
	assert.Len(t, q.Expired, 1)
	assert.Len(t, q.Unverified, 1)
	require.Len(t, q.Stale, 1)
	assert.Equal(t, stale.ID, q.Stale[0].ID)
	assert.Len(t, q.Conflicts, 1)
	assert.Equal(t, 1, q.QuarantinedCount)
}

func TestClaimJobs_NoDoubleClaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	for i := range 50 {
		m := insertWith(t, f, "job", string(rune('A'+i)), nil, -1)
		require.NoError(t, f.store.EnqueueEmbedding(ctx, m.ID, 1))
	}
	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				jobs, err := f.store.ClaimJobs(ctx, 7, time.Now())
				if err != nil || len(jobs) == 0 {
					return
				}
				mu.Lock()
				for _, j := range jobs {
					seen[j.MemoryID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Len(t, seen, 50)
	for id, n := range seen {
		assert.Equal(t, 1, n, id.String())
	}
}

func TestJobs_Lifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := insertWith(t, f, "Title", "Body", nil, -1)
	require.NoError(t, f.store.EnqueueEmbedding(ctx, m.ID, 1))
	jobs, err := f.store.ClaimJobs(ctx, 10, time.Now())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "Title", jobs[0].Title)
	assert.Equal(t, "Body", jobs[0].Content)

	require.NoError(t, f.store.FailJob(ctx, m.ID, 1, "ollama down", time.Now().Add(-time.Second), false))
	jobs, err = f.store.ClaimJobs(ctx, 10, time.Now())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, 1, jobs[0].Attempts)
	require.NoError(t, f.store.CompleteJob(ctx, m.ID, 1))
	jobs, err = f.store.ClaimJobs(ctx, 10, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestAdmin_PurgeApproveProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	cfg := testConfig().Database
	cfg.Host, cfg.Port, cfg.Name = f.db.Host, f.db.Port, f.db.Name
	admin, err := store.OpenAdmin(ctx, cfg, storetest.AdminPassword, "mem_personal", "dev")
	require.NoError(t, err)
	t.Cleanup(admin.Close)

	q := insertWith(t, f, "q", "q", func(m *memory.Memory) { m.Status = memory.StatusQuarantined }, -1)
	ql, err := admin.QuarantinedList(ctx)
	require.NoError(t, err)
	require.Len(t, ql, 1)
	require.NoError(t, admin.Approve(ctx, q.ID))
	got, err := f.store.GetByIDs(ctx, []uuid.UUID{q.ID})
	require.NoError(t, err)
	assert.Equal(t, memory.StatusActive, got[0].Status)
	jobs, err := f.store.ClaimJobs(ctx, 10, time.Now())
	require.NoError(t, err)
	require.Len(t, jobs, 1, "approval queues an embedding")
	assert.Equal(t, q.ID, jobs[0].MemoryID)

	victim := insertWith(t, f, "secret", "leaked", nil, 1)
	_, err = f.store.LogSearch(ctx, "leaked", nil, []uuid.UUID{victim.ID})
	require.NoError(t, err)
	require.NoError(t, f.store.AddFeedback(ctx, victim.ID, memory.FeedbackWrong, "", nil))
	require.NoError(t, admin.Purge(ctx, victim.ID))
	for _, table := range []string{"memories", "memory_versions", "memory_feedback", "access_events"} {
		var n int
		col := "memory_id"
		if table == "memories" {
			col = "id"
		}
		q := "SELECT count(*) FROM mem_personal." + table + " WHERE " + col + " = $1"
		if table == "access_events" {
			q = "SELECT count(*) FROM mem_personal.access_events WHERE memory_id = $1 OR $1 = ANY(returned_ids)"
		}
		require.NoError(t, f.admin.QueryRow(ctx, q, victim.ID).Scan(&n))
		assert.Zero(t, n, table)
	}
	require.ErrorIs(t, admin.Purge(ctx, victim.ID), memory.ErrNotFound)

	p := newMem("Style", "Use US spelling.")
	p.Type = memory.TypeUser
	require.NoError(t, admin.ProfileInsert(ctx, p, unitVec(768, 9), "test"))
	profile, err := admin.ProfileList(ctx)
	require.NoError(t, err)
	require.Len(t, profile, 1)
	assert.Equal(t, memory.ScopeProfile, profile[0].Scope)
	vec, err := f.store.CandidatesVector(ctx, unitVec(768, 9), memory.Filter{Owner: "dev", Now: time.Now()}, 5)
	require.NoError(t, err)
	assert.Contains(t, ids(vec), p.ID)

	n, err := admin.Prune(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(0))
}

func TestAdminPruneCountAndAuditDetails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	cfg := testConfig().Database
	cfg.Host, cfg.Port, cfg.Name = f.db.Host, f.db.Port, f.db.Name
	admin, err := store.OpenAdmin(ctx, cfg, storetest.AdminPassword, "mem_personal", "dev",
		store.WithAuditDetails(map[string]any{"via": "ui"}))
	require.NoError(t, err)
	t.Cleanup(admin.Close)

	quarantined := insertWith(t, f, "q", "q", func(m *memory.Memory) { m.Status = memory.StatusQuarantined }, -1)
	victim := insertWith(t, f, "secret", "leaked", nil, -1)
	require.NoError(t, admin.Approve(ctx, quarantined.ID))
	require.NoError(t, admin.Purge(ctx, victim.ID))

	for _, id := range []uuid.UUID{quarantined.ID, victim.ID} {
		var via string
		require.NoError(t, f.admin.QueryRow(ctx,
			`SELECT details->>'via' FROM mem_personal.audit_log WHERE memory_id = $1 AND action <> 'create'`, id).Scan(&via))
		assert.Equal(t, "ui", via)
	}
	var untagged int
	require.NoError(t, f.admin.QueryRow(ctx,
		`SELECT count(*) FROM mem_personal.audit_log WHERE action IN ('approve', 'purge') AND details->>'via' IS DISTINCT FROM 'ui'`).Scan(&untagged))
	assert.Zero(t, untagged)

	_, err = f.store.LogSearch(ctx, "old", nil, nil)
	require.NoError(t, err)
	_, err = f.admin.Exec(ctx, `UPDATE mem_personal.access_events SET created_at = now() - interval '100 days'`)
	require.NoError(t, err)
	_, err = f.store.LogSearch(ctx, "recent", nil, nil)
	require.NoError(t, err)

	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	counted, err := admin.PruneCount(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(1), counted)
	deleted, err := admin.Prune(ctx, cutoff)
	require.NoError(t, err)
	assert.Equal(t, counted, deleted)
	after, err := admin.PruneCount(ctx, cutoff)
	require.NoError(t, err)
	assert.Zero(t, after)
}

func vecLiteral(dims, hot int) string {
	b := make([]byte, 0, dims*2+2)
	b = append(b, '[')
	for i := range dims {
		if i > 0 {
			b = append(b, ',')
		}
		if i == hot%dims {
			b = append(b, '1')
		} else {
			b = append(b, '0')
		}
	}
	return string(append(b, ']'))
}

func TestEnqueueAllAndDimensions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	insertWith(t, f, "a", "a", nil, 0)
	insertWith(t, f, "b", "b", nil, -1)
	del := insertWith(t, f, "c", "c", nil, -1)
	require.NoError(t, f.store.SetStatus(ctx, del.ID, memory.StatusDeleted, "delete", audit("delete")))

	n, err := f.store.EnqueueAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "deleted memories are not re-embedded")
	dims, err := f.store.EmbeddingDimensions(ctx)
	require.NoError(t, err)
	assert.Equal(t, 768, dims)
}

func TestResizeEmbeddings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := insertWith(t, f, "a", "a", nil, 0)
	for _, schema := range []string{"mem_personal", store.ProfileSchema} {
		require.NoError(t, store.ResizeEmbeddings(ctx, f.db.AdminDSN, schema, 1024))
	}
	dims, err := f.store.EmbeddingDimensions(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1024, dims)
	require.NoError(t, f.store.SetEmbedding(ctx, m.ID, 1, unitVec(1024, 1), "big"))
	vec, err := f.store.CandidatesVector(ctx, unitVec(1024, 1), memory.Filter{Owner: "dev", Now: time.Now()}, 5)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{m.ID}, ids(vec))
}

func TestJobs_StaleVersionNotCompleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := insertWith(t, f, "Ports", "postgres on 5433", nil, -1)
	require.NoError(t, f.store.EnqueueEmbedding(ctx, m.ID, 1))
	jobs, err := f.store.ClaimJobs(ctx, 10, time.Now())
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	c := "postgres on 5434"
	updated, err := f.store.Update(ctx, m.ID, nil, memory.Patch{Content: &c}, nil, audit("update"))
	require.NoError(t, err)
	require.NoError(t, f.store.EnqueueEmbedding(ctx, m.ID, updated.Version))

	require.ErrorIs(t, f.store.SetEmbedding(ctx, m.ID, 1, unitVec(768, 0), "t"), memory.ErrConflict, "stale vector is reported")
	require.NoError(t, f.store.CompleteJob(ctx, m.ID, 1))
	jobs, err = f.store.ClaimJobs(ctx, 10, time.Now())
	require.NoError(t, err)
	require.Len(t, jobs, 1, "completing the old version must not finish the new job")
	assert.Equal(t, updated.Version, jobs[0].Version)
}

func TestRequeueMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	lost := insertWith(t, f, "lost", "lost", nil, -1)
	insertWith(t, f, "embedded", "embedded", nil, 0)
	insertWith(t, f, "q", "q", func(m *memory.Memory) { m.Status = memory.StatusQuarantined }, -1)
	require.NoError(t, f.store.EnqueueEmbedding(ctx, lost.ID, 1))
	require.NoError(t, f.store.FailJob(ctx, lost.ID, 1, "ollama down", time.Now().Add(time.Hour), true))

	n, err := f.store.RequeueMissing(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	jobs, err := f.store.ClaimJobs(ctx, 10, time.Now())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, lost.ID, jobs[0].MemoryID)
}
