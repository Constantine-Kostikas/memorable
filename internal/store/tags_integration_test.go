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
)

func seedTags(t *testing.T, f fixture) {
	t.Helper()
	ctx := context.Background()
	insertWith(t, f, "One", "first", func(m *memory.Memory) { m.Tags = []string{"a", "b"} }, -1)
	insertWith(t, f, "Two", "second", func(m *memory.Memory) { m.Tags = []string{"a"} }, -1)
	gone := insertWith(t, f, "Three", "third", func(m *memory.Memory) { m.Tags = []string{"c"} }, -1)
	require.NoError(t, f.store.SetStatus(ctx, gone.ID, memory.StatusDeleted, "delete", audit("delete")))
	_, err := f.admin.Exec(ctx, `INSERT INTO mem_profile.memories (id, owner, type, title, content, tags, source, status, content_hash)
		VALUES ($1, 'dev', 'user', 'Profile', 'profile text', '{a}', 'user_stated', 'active', '\x00')`, uuid.Must(uuid.NewV7()))
	require.NoError(t, err)
}

func TestTagCounts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	seedTags(t, f)

	got, err := f.store.TagCounts(context.Background(), memory.Filter{Owner: "dev", Now: time.Now()}, 100)

	require.NoError(t, err)
	assert.Equal(t, []memory.TagCount{{Tag: "a", Count: 3}, {Tag: "b", Count: 1}}, got)
}

func TestTagCounts_Limit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	seedTags(t, f)

	got, err := f.store.TagCounts(context.Background(), memory.Filter{Owner: "dev", Now: time.Now()}, 1)

	require.NoError(t, err)
	assert.Equal(t, []memory.TagCount{{Tag: "a", Count: 3}}, got)
}
