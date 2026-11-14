package memory_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func TestPreload_Budget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, nil)
	var pinned memory.Memory
	for i := range 12 {
		n := note(fmt.Sprintf("Memory %02d", i), fmt.Sprintf("content number %d", i))
		if i == 11 {
			n.Pinned = true
		}
		m := created(t, e, n)
		if i == 11 {
			pinned = m
		}
	}
	tool := note("From a README", "port 8080 per docs")
	tool.Source = memory.SourceToolOutput
	toolMem := created(t, e, tool)
	p := memory.Memory{ID: uuid.Must(uuid.NewV7()), Owner: "dev", Title: "Style", Content: "US spelling",
		Type: memory.TypeUser, Status: memory.StatusActive, Version: 1, Pinned: true}
	e.repo.AddProfile(p, nil)

	got, err := e.svc.Preload(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 10)
	ids := resultIDs(got)
	assert.Contains(t, ids[:2], pinned.ID, "pinned first")
	assert.Contains(t, ids[:2], p.ID, "profile pinned first")
	assert.NotContains(t, ids, toolMem.ID, "tool output never preloaded")
}

func TestPreload_CharBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(s *memory.Settings) { s.Preload.MaxChars = 500 })
	for i := range 5 {
		created(t, e, note(fmt.Sprintf("Long %d", i), strings.Repeat(fmt.Sprintf("word%d ", i), 60)))
	}
	got, err := e.svc.Preload(context.Background())
	require.NoError(t, err)
	total := 0
	for _, s := range got {
		total += len([]rune(s.Title)) + len([]rune(s.Snippet))
	}
	assert.LessOrEqual(t, total, 500)
	assert.NotEmpty(t, got)
}
