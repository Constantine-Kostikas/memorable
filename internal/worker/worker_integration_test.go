//go:build integration

package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/store/storetest"
)

// countingEmbedder returns unit vectors and counts how often each text was embedded.
type countingEmbedder struct {
	mu    sync.Mutex
	calls map[string]int
}

func (c *countingEmbedder) Embed(_ context.Context, _ memory.EmbedKind, texts []string) ([][]float32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]float32, len(texts))
	for i, t := range texts {
		c.calls[t]++
		out[i] = make([]float32, 768)
		out[i][0] = 1
	}
	return out, nil
}

func (c *countingEmbedder) Model() string   { return "counting" }
func (c *countingEmbedder) Dimensions() int { return 768 }

func TestWorker_TwoWorkersNoDuplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	cfg := config.Default()
	cfg.Contexts = map[string]config.Context{"personal": {Paths: []string{"/p"}}}
	require.NoError(t, storetest.Bootstrap(ctx, t, db, &cfg))
	s, err := store.Open(ctx, storetest.DatabaseConfig(db), storetest.AppPassword, "mem_personal", "dev")
	require.NoError(t, err)
	defer s.Close()

	const n = 100
	for i := range n {
		title, content := "job", uuid.NewString()
		m := &memory.Memory{ID: uuid.Must(uuid.NewV7()), Owner: "dev", Type: memory.TypeProject, Title: title,
			Content: content, Source: memory.SourceUserStated, Status: memory.StatusActive, Version: 1,
			ContentHash: memory.ContentHash(title, content)}
		require.NoError(t, s.Insert(ctx, m, memory.AuditEntry{Action: "create"}), i)
		require.NoError(t, s.EnqueueEmbedding(ctx, m.ID, 1))
	}

	emb := &countingEmbedder{calls: map[string]int{}}
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for range 2 {
		w := New(s, s, emb, quiet())
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Run(runCtx)
		}()
	}
	admin, err := pgx.Connect(ctx, db.AdminDSN)
	require.NoError(t, err)
	defer func() { _ = admin.Close(ctx) }()
	require.Eventually(t, func() bool {
		var pending int
		err := admin.QueryRow(ctx, "SELECT count(*) FROM mem_personal.embedding_jobs WHERE done_at IS NULL").Scan(&pending)
		return err == nil && pending == 0
	}, 30*time.Second, 100*time.Millisecond)
	cancel()
	wg.Wait()

	emb.mu.Lock()
	defer emb.mu.Unlock()
	assert.Len(t, emb.calls, n, "every job embedded")
	duplicates := 0
	for _, count := range emb.calls {
		if count != 1 {
			duplicates++
		}
	}
	assert.Zero(t, duplicates, "no job embedded twice")
}
