//go:build integration

package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func TestStoreActivityAndMarkSuperseded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	old, replacement := newMem("Ports", "postgres on 5433"), newMem("Ports now", "postgres on 5434")
	require.NoError(t, f.store.Insert(ctx, old, audit("create")))
	require.NoError(t, f.store.Insert(ctx, replacement, audit("create")))
	require.NoError(t, f.store.AddFeedback(ctx, old.ID, memory.FeedbackUseful, "first", nil))
	require.NoError(t, f.store.AddFeedback(ctx, old.ID, memory.FeedbackOutdated, "second", nil))

	ui := audit("supersede")
	ui.Details = map[string]any{"via": "ui"}
	require.NoError(t, f.store.MarkSuperseded(ctx, old.ID, replacement.ID, ui))

	got, err := f.store.GetByIDs(ctx, []uuid.UUID{old.ID})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, memory.StatusSuperseded, got[0].Status)
	links, err := f.store.LinksFor(ctx, []uuid.UUID{old.ID})
	require.NoError(t, err)
	assert.Equal(t, []memory.Link{{From: replacement.ID, To: old.ID, Kind: memory.LinkSupersedes}}, links)
	versions, err := f.store.History(ctx, old.ID)
	require.NoError(t, err)
	assert.Equal(t, "supersede", versions[len(versions)-1].ChangeKind)

	activity, err := f.store.Activity(ctx, old.ID)
	require.NoError(t, err)
	require.Len(t, activity.Feedback, 2)
	assert.Equal(t, "second", activity.Feedback[0].Note, "newest first")
	assert.Equal(t, memory.FeedbackOutdated, activity.Feedback[0].Kind)
	assert.Equal(t, "first", activity.Feedback[1].Note)
	require.Len(t, activity.Audit, 2)
	assert.Equal(t, "supersede", activity.Audit[0].Action)
	assert.Equal(t, "create", activity.Audit[1].Action)
	assert.Equal(t, "ui", activity.Audit[0].Details["via"])
	assert.Equal(t, "superseded", activity.Audit[0].Details["status"])
	assert.Equal(t, "/p/x", activity.Audit[0].Cwd)
	assert.Equal(t, "1@t", activity.Audit[0].Process)
	assert.False(t, activity.Audit[0].CreatedAt.IsZero())

	empty, err := f.store.Activity(ctx, uuid.New())
	require.NoError(t, err)
	assert.Empty(t, empty.Feedback)
	assert.Empty(t, empty.Audit)
}

func TestMarkSupersededRollsBackWhenLinkFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	old := newMem("Ports", "postgres on 5433")
	require.NoError(t, f.store.Insert(ctx, old, audit("create")))

	require.Error(t, f.store.MarkSuperseded(ctx, old.ID, uuid.New(), audit("supersede")))

	got, err := f.store.GetByIDs(ctx, []uuid.UUID{old.ID})
	require.NoError(t, err)
	assert.Equal(t, memory.StatusActive, got[0].Status)
	assert.Equal(t, 1, got[0].Version)
	versions, err := f.store.History(ctx, old.ID)
	require.NoError(t, err)
	assert.Len(t, versions, 1)
	activity, err := f.store.Activity(ctx, old.ID)
	require.NoError(t, err)
	require.Len(t, activity.Audit, 1)
	assert.Equal(t, "create", activity.Audit[0].Action)
}
