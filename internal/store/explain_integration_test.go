//go:build integration

package store_test

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/store"
)

func randomVec(r *rand.Rand, dims int) pgvector.Vector {
	v := make([]float32, dims)
	var norm float64
	for i := range v {
		x := r.NormFloat64()
		v[i] = float32(x)
		norm += x * x
	}
	n := float32(math.Sqrt(norm))
	for i := range v {
		v[i] /= n
	}
	return pgvector.NewVector(v)
}

func TestExplain_UsesIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	require.NoError(t, pgxvec.RegisterTypes(ctx, f.admin))

	r := rand.New(rand.NewPCG(7, 11))
	words := make([]string, 2000)
	for i := range words {
		words[i] = fmt.Sprintf("w%04dx", i)
	}
	sentence := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = words[r.IntN(len(words))]
		}
		return strings.Join(parts, " ")
	}
	const n = 10000
	start := time.Now()
	rows := make([][]any, 0, n)
	for i := range n {
		title := sentence(4)
		content := sentence(30)
		rows = append(rows, []any{
			uuid.Must(uuid.NewV7()), "dev", "project", title, content, []string{"t"}, "user_stated", "active",
			memory.ContentHash(title, content), randomVec(r, 768), r.Float64() * float64(i%50),
		})
	}
	copied, err := f.admin.CopyFrom(ctx, pgx.Identifier{"mem_personal", "memories"},
		[]string{"id", "owner", "type", "title", "content", "tags", "source", "status", "content_hash", "embedding", "rank_key"},
		pgx.CopyFromRows(rows))
	require.NoError(t, err)
	require.Equal(t, int64(n), copied)
	_, err = f.admin.Exec(ctx, "ANALYZE mem_personal.memories")
	require.NoError(t, err)
	t.Logf("seeded %d rows in %s", n, time.Since(start))

	filter := memory.Filter{Owner: "dev", Now: time.Now()}
	cases := []struct {
		name  string
		sql   string
		args  []any
		index string
	}{}
	add := func(name, index string, sql string, args []any) {
		cases = append(cases, struct {
			name  string
			sql   string
			args  []any
			index string
		}{name, sql, args, index})
	}
	sql, args := store.VectorCandidatesSQL(randomVec(r, 768).Slice(), filter, 50)
	add("vector", "memories_embedding_idx", sql, args)
	sql, args = store.FTSCandidatesSQL(words[42], filter, 50)
	add("fts", "memories_fts_idx", sql, args)
	sql, args = store.TrigramCandidatesSQL(words[42], filter, 50)
	add("trigram", "memories_title_trgm_idx", sql, args)
	sql, args = store.TopRankedSQL(filter, 10)
	add("top", "memories_rank_key_idx", sql, args)

	// At 10,000 rows the planner may legitimately prefer a sequential scan for vectors, so the
	// vector case proves index compatibility with sequential scans disabled; the others must
	// choose their index on their own.
	for _, c := range cases {
		plan, err := f.store.Explain(ctx, c.sql, c.args, c.name == "vector")
		require.NoError(t, err, c.name)
		if !strings.Contains(plan, c.index) {
			t.Errorf("%s plan does not use %s", c.name, c.index)
		}
	}
}
