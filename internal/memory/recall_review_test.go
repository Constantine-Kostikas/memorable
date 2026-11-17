package memory_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func TestSearchSurvivesSchemaBehind(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	created(t, e, note("Postgres port", "postgres runs on port 5434"))
	e.repo.LogEventErr = fmt.Errorf("log event: %w", memory.ErrSchemaBehind)
	res, err := e.svc.Search(context.Background(), memory.SearchRequest{Query: "postgres port", Origin: memory.OriginMCP})
	require.NoError(t, err, "search works between `memorable update` and `memorable migrate`")
	assert.Len(t, res.Results, 1)
	assert.Equal(t, uuid.Nil, res.SearchID)

	e.repo.LogEventErr = fmt.Errorf("log event: %w", memory.ErrUnavailable)
	_, err = e.svc.Search(context.Background(), memory.SearchRequest{Query: "postgres port", Origin: memory.OriginMCP})
	require.ErrorIs(t, err, memory.ErrUnavailable, "other logging failures still surface")
}

func TestRecallNullsSecretCutAtTruncation(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	created(t, e, note("Postgres port", "postgres runs on port 5434"))
	token := "ghp_" + "9Xk2mQ7vR4tL8wN3pZ6yB1cD5fG0hJ2sK4aE"
	prompt := "which postgres port " + strings.Repeat("x ", 237) + token
	require.Greater(t, utf8.RuneCountInString(prompt), memory.MaxQueryRunes)
	require.Less(t, utf8.RuneCountInString(prompt)-len(token), memory.MaxQueryRunes, "the token straddles the cut")
	_, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: prompt, SessionID: "s"})
	require.NoError(t, err)
	events := injectEvents(e)
	require.Len(t, events, 1)
	assert.Nil(t, events[0].Query, "the full prompt is scanned, not just the stored prefix")
}

// setStatus moves a memory to status directly, bypassing the service's lifecycle rules.
func setStatus(t *testing.T, e *env, id uuid.UUID, s memory.Status) {
	t.Helper()
	require.NoError(t, e.repo.SetStatus(context.Background(), id, s, "test", memory.AuditEntry{Action: "test", MemoryID: id}))
}

func TestRecallNeverInjectsToolOutput(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	tool := note("Postgres port", "postgres runs on port 5434")
	tool.Source = memory.SourceToolOutput
	m := created(t, e, tool)
	setStatus(t, e, m.ID, memory.StatusActive) // active, so only the source rule can exclude it
	res, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port do we use", SessionID: "s"})
	require.NoError(t, err)
	assert.Empty(t, res.Items)
	assert.Empty(t, injectEvents(e))
}

func TestRecallNeverInjectsUnverified(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, &rankOnly, nil)
	m := created(t, e, note("Postgres port", "postgres runs on port 5434"))
	setStatus(t, e, m.ID, memory.StatusUnverified) // user_stated source, so only the status rule can exclude it
	res, err := e.svc.Recall(context.Background(), memory.RecallRequest{Prompt: "which postgres port do we use", SessionID: "s"})
	require.NoError(t, err)
	assert.Empty(t, res.Items)
	assert.Empty(t, injectEvents(e))
}
