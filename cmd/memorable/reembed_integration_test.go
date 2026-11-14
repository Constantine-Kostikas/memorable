//go:build integration

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/memory/memorytest"
	"github.com/constantine-kostikas/memorable/internal/scan"
	"github.com/constantine-kostikas/memorable/internal/scope"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/store/storetest"
)

func TestReembed_DimensionChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	cfg := config.Default()
	cfg.Database = storetest.DatabaseConfig(db)
	cfg.Contexts = map[string]config.Context{"personal": {Paths: []string{t.TempDir()}}}
	require.NoError(t, storetest.Bootstrap(ctx, t, db, &cfg))

	st, err := store.Open(ctx, cfg.Database, storetest.AppPassword, "mem_personal", "dev")
	require.NoError(t, err)
	defer st.Close()
	small := memorytest.NewEmbedder()
	small.Dims = 768
	sec, err := scan.NewSecretScanner()
	require.NoError(t, err)
	settings := memory.Settings{Owner: "dev", Scope: scope.Resolution{Context: "personal"}, Search: cfg.Search,
		Preload: cfg.Preload, WritesPerMinute: 60}
	svc := memory.NewService(st, st, small, sec, scan.Injection, settings)
	for _, c := range []string{"alpha one", "beta two", "gamma three"} {
		_, err := svc.Write(ctx, []memory.NewMemory{{Title: c, Content: c + " content", Type: memory.TypeProject, Source: memory.SourceUserStated}})
		require.NoError(t, err)
	}

	cfg.Embedding.Dimensions = 1024
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	f, err := os.Create(cfgPath)
	require.NoError(t, err)
	require.NoError(t, toml.NewEncoder(f).Encode(cfg))
	require.NoError(t, f.Close())

	big := memorytest.NewEmbedder()
	big.Dims = 1024
	d := defaultDeps()
	d.adminPassword = func(*cobra.Command) (string, error) { return storetest.AdminPassword, nil }
	d.build = func(ctx context.Context, _ string, _ []string, _ *slog.Logger) (*app, error) {
		cfgCopy := cfg
		own, err := store.Open(ctx, cfg.Database, storetest.AppPassword, "mem_personal", "dev")
		if err != nil {
			return nil, err
		}
		return &app{cfg: &cfgCopy, res: scope.Resolution{Context: "personal"}, store: own, emb: big,
			svc: memory.NewService(own, own, big, sec, scan.Injection, settings)}, nil
	}
	out, err := runRoot(t, d, "", "--config", cfgPath, "reembed", "--all")
	require.NoError(t, err, out)
	assert.Contains(t, out, "processed 3 embedding jobs")

	dims, err := st.EmbeddingDimensions(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1024, dims)
	admin, err := pgx.Connect(ctx, db.AdminDSN)
	require.NoError(t, err)
	defer func() { _ = admin.Close(ctx) }()
	var embedded int
	require.NoError(t, admin.QueryRow(ctx, "SELECT count(embedding) FROM mem_personal.memories").Scan(&embedded))
	assert.Equal(t, 3, embedded)
}
