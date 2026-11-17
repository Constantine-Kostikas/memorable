package memory_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/rank"
)

// rankOnly makes relevance depend only on vector rank: 0.95, 0.73, 0.58, 0.50, 0.45 for ranks 1 to 5.
var rankOnly = rank.Calibration{B0: -1, B4: 4}

func newRecallEnv(t *testing.T, c *rank.Calibration, mutate func(*memory.Settings)) *env {
	t.Helper()
	e := newEnv(t, func(s *memory.Settings) {
		s.Recall = config.Default().Recall
		s.Recall.MinRelevance = 0.5
		if mutate != nil {
			mutate(s)
		}
	})
	if c != nil {
		require.NoError(t, e.repo.SaveCalibration(context.Background(), memory.StoredCalibration{
			Model: e.emb.Model(), Dimensions: e.emb.Dimensions(), Coefficients: *c, Source: "usage"}))
	}
	return e
}

func injectEvents(e *env) []memory.Event {
	var out []memory.Event
	for _, ev := range e.repo.LoggedEvents() {
		if ev.Kind == memory.EventInject {
			out = append(out, ev)
		}
	}
	return out
}

func TestRecallSkipsShortAndSlashPrompts(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	created(t, e, note("Postgres port", "postgres runs on port 5434"))
	for _, p := range []string{"ok thanks", "", "/review the whole branch please"} {
		res, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: p, SessionID: "s"})
		require.NoError(t, err, p)
		assert.Empty(t, res.Items, p)
	}
	assert.Empty(t, injectEvents(e))
	assert.Zero(t, e.emb.Calls-1, "only the write embedded; skipped prompts never reach the embedder")
}

func TestRecallTruncatesByRunes(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	_, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: strings.Repeat("é ", 300), SessionID: "s"})
	require.NoError(t, err)
	inputs := e.emb.Inputs()
	require.NotEmpty(t, inputs)
	last := inputs[len(inputs)-1]
	assert.True(t, utf8.ValidString(last))
	assert.LessOrEqual(t, utf8.RuneCountInString(strings.TrimSpace(last)), memory.MaxQueryRunes+40, "query prefix aside, at most 500 runes")
	assert.NotContains(t, last, strings.Repeat("é ", 260))
}

func TestRecallInjectsAndLogs(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	m := created(t, e, note("Postgres port", "postgres runs on port 5434"))
	res, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port do we use", SessionID: "s"})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	assert.Equal(t, m.ID, res.Items[0].Summary.ID)
	assert.InDelta(t, rankOnly.Relevance(rank.Signals{VectorRank: 1, Similarity: 0}), res.Items[0].Relevance, 0.05)
	assert.NotEqual(t, uuid.Nil, res.SearchID)

	events := injectEvents(e)
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, "s", ev.SessionID)
	assert.Equal(t, []uuid.UUID{m.ID}, ev.Returned)
	assert.Equal(t, memory.OriginHook, ev.Details.Origin)
	assert.Equal(t, "fake", ev.Details.Model)
	require.Len(t, ev.Details.Signals, 1)
	assert.Equal(t, 1, ev.Details.Signals[0].VectorRank)
	require.NotNil(t, ev.Query)
}

func TestRecallNullsSecretQuery(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	created(t, e, note("AWS access", "the deploy role uses aws keys from the vault"))
	_, err := e.svc.Recall(context.Background(), memory.RecallRequest{
		Prompt: "why does aws reject AKIAZ5MDKS7G3XWRL2QP with the deploy role", SessionID: "s"})
	require.NoError(t, err)
	events := injectEvents(e)
	require.Len(t, events, 1)
	assert.Nil(t, events[0].Query, "a prompt with a secret is never stored")
}

func TestRecallRequiresVector(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	created(t, e, note("Postgres port", "postgres runs on port 5434"))
	e.emb.Down = true
	res, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port do we use", SessionID: "s"})
	require.NoError(t, err)
	assert.Empty(t, res.Items)
	assert.Empty(t, injectEvents(e))
}

func TestRecallFiltersAndCaps(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	for _, title := range []string{"alpha port", "beta port", "gamma port", "delta port", "epsilon port"} {
		created(t, e, note(title, title+" postgres"))
	}
	tool := note("zeta port", "zeta port postgres")
	tool.Source = memory.SourceToolOutput
	created(t, e, tool)

	res, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port again", SessionID: "s"})
	require.NoError(t, err)
	require.Len(t, res.Items, 3, "capped at max_items")
	for i, it := range res.Items {
		assert.GreaterOrEqual(t, it.Relevance, 0.5)
		assert.NotEqual(t, "zeta port", it.Summary.Title, "tool_output memories are never injected")
		if i > 0 {
			assert.LessOrEqual(t, it.Relevance, res.Items[i-1].Relevance, "ordered by relevance")
		}
	}
}

func TestRecallDedupsSession(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	m := created(t, e, note("Postgres port", "postgres runs on port 5434"))
	require.NoError(t, e.svc.LogPreload(context.Background(), "s", "startup", []uuid.UUID{m.ID}))
	res, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port do we use", SessionID: "s"})
	require.NoError(t, err)
	assert.Empty(t, res.Items, "already preloaded")

	res, err = e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port do we use", SessionID: "t"})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	res, err = e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port do we use", SessionID: "t"})
	require.NoError(t, err)
	assert.Empty(t, res.Items, "already injected in this session")
}

func TestRecallWithoutSession(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	created(t, e, note("Postgres port", "postgres runs on port 5434"))
	for range 2 {
		res, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port do we use"})
		require.NoError(t, err)
		assert.Len(t, res.Items, 1)
	}
}

func TestRecallNeverCountsAccess(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	m := created(t, e, note("Postgres port", "postgres runs on port 5434"))
	_, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port do we use", SessionID: "s"})
	require.NoError(t, err)
	got, err := e.svc.Get(context.Background(), memory.GetRequest{IDs: []uuid.UUID{m.ID}, Passive: true})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Zero(t, got[0].Memory.AccessScore)
}

func TestRecallNoCalibration(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, nil, nil)
	created(t, e, note("Postgres port", "postgres runs on port 5434"))
	res, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port do we use", SessionID: "s"})
	require.NoError(t, err)
	assert.Empty(t, res.Items)
	_, _, ok, err := e.svc.ActiveCalibration(context.Background())
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestActiveCalibrationPrefersUsageFit(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	c, source, ok, err := e.svc.ActiveCalibration(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "usage", source)
	assert.Equal(t, rankOnly, c)
}

func TestSearchLogsOriginAndSignals(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	m := created(t, e, note("Postgres port", "postgres runs on port 5434"))
	e.repo.AddProfile(memory.Memory{ID: uuid.New(), Owner: "dev", Title: "postgres port profile", Content: "postgres port",
		Type: memory.TypeUser, Status: memory.StatusActive, Source: memory.SourceUserStated}, nil)
	_, err := e.svc.Search(context.Background(), memory.SearchRequest{Query: "postgres port", Origin: memory.OriginMCP})
	require.NoError(t, err)
	var search *memory.Event
	for _, ev := range e.repo.LoggedEvents() {
		if ev.Kind == memory.EventSearch {
			search = &ev
		}
	}
	require.NotNil(t, search)
	assert.Equal(t, memory.OriginMCP, search.Details.Origin)
	assert.Equal(t, "fake", search.Details.Model)
	require.Len(t, search.Details.Signals, 1, "profile results carry no signals")
	assert.Equal(t, m.ID, search.Details.Signals[0].ID)
	assert.GreaterOrEqual(t, len(search.Returned), 1)
}
