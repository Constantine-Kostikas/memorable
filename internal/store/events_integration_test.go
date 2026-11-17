//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/rank"
	"github.com/constantine-kostikas/memorable/internal/store"
)

const testModel = "embeddinggemma"

func details(origin string, signals ...memory.ResultSignals) memory.EventDetails {
	return memory.EventDetails{Origin: origin, Model: testModel, Dimensions: 768, Signals: signals}
}

func shown(ids ...uuid.UUID) []memory.ResultSignals {
	out := make([]memory.ResultSignals, 0, len(ids))
	for i, id := range ids {
		out = append(out, memory.ResultSignals{ID: id, Similarity: 0.5, FTS: true, VectorRank: i + 1})
	}
	return out
}

func TestLogEventRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := insertWith(t, f, "a", "a", nil, 1)
	q := "which port"
	id, err := f.store.LogEvent(ctx, memory.Event{Kind: memory.EventInject, Query: &q, SessionID: "s1",
		Returned: []uuid.UUID{m.ID}, Details: details("hook", shown(m.ID)...)})
	require.NoError(t, err)

	var kind, session, origin string
	var vrank int
	require.NoError(t, f.admin.QueryRow(ctx, `SELECT kind, session_id, details->>'origin', (details->'signals'->0->>'vrank')::int
		FROM mem_personal.access_events WHERE id = $1`, id).Scan(&kind, &session, &origin, &vrank))
	assert.Equal(t, "inject", kind)
	assert.Equal(t, "s1", session)
	assert.Equal(t, "hook", origin)
	assert.Equal(t, 1, vrank)

	id, err = f.store.LogEvent(ctx, memory.Event{Kind: memory.EventSearch, Query: &q})
	require.NoError(t, err)
	var nullSession bool
	require.NoError(t, f.admin.QueryRow(ctx, `SELECT session_id IS NULL FROM mem_personal.access_events WHERE id = $1`, id).Scan(&nullSession))
	assert.True(t, nullSession, "an empty session id is stored as NULL")
}

func TestSessionShownResetsOnCompact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	a, b, c, d, e := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	log := func(kind memory.EventKind, source string, id uuid.UUID) {
		t.Helper()
		_, err := f.store.LogEvent(ctx, memory.Event{Kind: kind, SessionID: "s1", Returned: []uuid.UUID{id},
			Details: memory.EventDetails{Origin: "hook", HookSource: source}})
		require.NoError(t, err)
	}
	log(memory.EventPreload, "startup", a)
	log(memory.EventInject, "", b)
	got, err := f.store.SessionShown(ctx, "s1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{a, b}, got)

	log(memory.EventPreload, "compact", c)
	log(memory.EventInject, "", d)
	got, err = f.store.SessionShown(ctx, "s1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{c, d}, got, "compaction resets dedup")

	log(memory.EventPreload, "resume", e)
	got, err = f.store.SessionShown(ctx, "s1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{c, d, e}, got, "resume does not reset dedup")

	got, err = f.store.SessionShown(ctx, "other")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestLabeledResults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	x := insertWith(t, f, "x", "x", nil, 1)
	y := insertWith(t, f, "y", "y", nil, 2)
	z := insertWith(t, f, "z", "z", nil, 3)

	searchID, err := f.store.LogEvent(ctx, memory.Event{Kind: memory.EventSearch, Returned: []uuid.UUID{x.ID, y.ID},
		Details: details("mcp", shown(x.ID, y.ID)...)})
	require.NoError(t, err)
	require.NoError(t, f.store.LogGet(ctx, x.ID, &searchID, nil))
	injectID, err := f.store.LogEvent(ctx, memory.Event{Kind: memory.EventInject, Returned: []uuid.UUID{z.ID},
		Details: details("hook", shown(z.ID)...)})
	require.NoError(t, err)
	require.NoError(t, f.store.LogGet(ctx, z.ID, &injectID, nil))
	_, err = f.store.LogEvent(ctx, memory.Event{Kind: memory.EventSearch, Returned: []uuid.UUID{y.ID},
		Details: details("cli", shown(y.ID)...)})
	require.NoError(t, err)
	other := details("mcp", shown(y.ID)...)
	other.Model = "other-model"
	_, err = f.store.LogEvent(ctx, memory.Event{Kind: memory.EventSearch, Returned: []uuid.UUID{y.ID}, Details: other})
	require.NoError(t, err)

	samples, err := f.store.LabeledResults(ctx, time.Now().Add(time.Minute), testModel, 768)
	require.NoError(t, err)
	require.Len(t, samples, 3, "cli searches and other models are excluded")
	relevant := 0
	for _, s := range samples {
		if s.Relevant {
			relevant++
		}
	}
	assert.Equal(t, 2, relevant, "x fetched after the search, z fetched after the injection")
	assert.Contains(t, samples, rank.Sample{Signals: rank.Signals{Similarity: 0.5, FTS: true, VectorRank: 2}, Relevant: false})

	samples, err = f.store.LabeledResults(ctx, time.Now().Add(-time.Hour), testModel, 768)
	require.NoError(t, err)
	assert.Empty(t, samples, "events newer than the cutoff are excluded")
}

func TestLabeledResultsSkipsMissingMemories(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	x := insertWith(t, f, "x", "x", nil, 1)
	ghost := uuid.New()
	_, err := f.store.LogEvent(ctx, memory.Event{Kind: memory.EventSearch, Returned: []uuid.UUID{x.ID, ghost},
		Details: details("mcp", shown(x.ID, ghost)...)})
	require.NoError(t, err)
	samples, err := f.store.LabeledResults(ctx, time.Now().Add(time.Minute), testModel, 768)
	require.NoError(t, err)
	assert.Len(t, samples, 1)
}

func TestCalibrationLatestByModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	save := func(model string, b0 float64) {
		t.Helper()
		require.NoError(t, f.store.SaveCalibration(ctx, memory.StoredCalibration{Model: model, Dimensions: 768,
			Coefficients: rank.Calibration{B0: b0, B4: 1}, Positives: 20, Negatives: 30, Source: "usage"}))
	}
	save("a", 1)
	save("b", 2)
	save("a", 3)

	got, err := f.store.LatestCalibration(ctx, "a", 768)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.InDelta(t, 3.0, got.Coefficients.B0, 1e-9)
	assert.InDelta(t, 1.0, got.Coefficients.B4, 1e-9)
	assert.Equal(t, 20, got.Positives)
	assert.Equal(t, 30, got.Negatives)
	assert.Equal(t, "usage", got.Source)
	assert.False(t, got.FittedAt.IsZero())

	got, err = f.store.LatestCalibration(ctx, "a", 1024)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestStatsRecall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	x := insertWith(t, f, "x", "x", nil, 1)
	y := insertWith(t, f, "y", "y", nil, 2)
	id, err := f.store.LogEvent(ctx, memory.Event{Kind: memory.EventInject, Returned: []uuid.UUID{x.ID, y.ID}})
	require.NoError(t, err)
	require.NoError(t, f.store.LogGet(ctx, x.ID, &id, nil))
	_, err = f.store.LogEvent(ctx, memory.Event{Kind: memory.EventInject, Returned: []uuid.UUID{y.ID}})
	require.NoError(t, err)

	st, err := f.store.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, store.RecallStats{Days: 30, Injections: 2, PerDay: 2.0 / 30, Shown: 3, Fetched: 1}, st.Recall)
}

func TestRecallEventsLeaveScoresUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	m := insertWith(t, f, "x", "x", nil, 1)
	for _, kind := range []memory.EventKind{memory.EventPreload, memory.EventInject} {
		_, err := f.store.LogEvent(ctx, memory.Event{Kind: kind, SessionID: "s", Returned: []uuid.UUID{m.ID}})
		require.NoError(t, err)
	}
	var score float64
	require.NoError(t, f.admin.QueryRow(ctx, `SELECT access_score FROM mem_personal.memories WHERE id = $1`, m.ID).Scan(&score))
	assert.Zero(t, score)
}
