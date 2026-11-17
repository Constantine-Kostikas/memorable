//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/store/storetest"
)

func testConfig() *config.Config {
	c := config.Default()
	c.Contexts = map[string]config.Context{"personal": {Paths: []string{"/p"}}, "work": {Paths: []string{"/w"}}}
	return &c
}

func TestBootstrapAndMigrate_Idempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	cfg := testConfig()

	for range 2 {
		require.NoError(t, storetest.Bootstrap(ctx, t, db, cfg))
	}
	applied, err := store.Migrate(ctx, db.AdminDSN, "mem_personal", store.SchemaContext, 768, cfg.Database.AppUser)
	require.NoError(t, err)
	assert.Empty(t, applied, "second migrate must apply nothing")
	assert.Equal(t, int64(4), store.LatestVersion(store.SchemaContext))
}

func TestMigrateUpFromV3(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	cfg := testConfig()
	require.NoError(t, storetest.Bootstrap(ctx, t, db, cfg))
	require.NoError(t, store.MigrateDownTo(ctx, db.AdminDSN, "mem_personal", 768, 3))

	conn, err := pgx.Connect(ctx, db.AdminDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })
	_, err = conn.Exec(ctx, `INSERT INTO mem_personal.access_events (id, owner, kind, query) VALUES (gen_random_uuid(), 'dev', 'search', 'old')`)
	require.NoError(t, err)

	applied, err := store.Migrate(ctx, db.AdminDSN, "mem_personal", store.SchemaContext, 768, cfg.Database.AppUser)
	require.NoError(t, err)
	assert.Equal(t, []int64{4}, applied)
	var details string
	require.NoError(t, conn.QueryRow(ctx, `SELECT details::text FROM mem_personal.access_events WHERE query = 'old'`).Scan(&details))
	assert.Equal(t, "{}", details)
}

func TestMigrationRecallEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	require.NoError(t, storetest.Bootstrap(ctx, t, db, testConfig()))

	app, err := pgx.Connect(ctx, db.AppDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Close(ctx) })
	_, err = app.Exec(ctx, "SET search_path = mem_personal, public")
	require.NoError(t, err)

	for _, kind := range []string{"search", "get", "inject", "preload"} {
		_, err = app.Exec(ctx, `INSERT INTO access_events (id, owner, kind, session_id, details)
			VALUES (gen_random_uuid(), 'dev', $1, 's1', '{"origin": "hook"}')`, kind)
		require.NoError(t, err, kind)
	}
	_, err = app.Exec(ctx, `INSERT INTO access_events (id, owner, kind) VALUES (gen_random_uuid(), 'dev', 'bogus')`)
	assert.Equal(t, "23514", pgCode(err), "unknown kinds are rejected")

	_, err = app.Exec(ctx, `INSERT INTO calibrations (id, owner, model, dimensions, b0, b1, b2, b3, b4, positives, negatives, source)
		VALUES (gen_random_uuid(), 'dev', 'embeddinggemma', 768, 0, 0, 0, 0, 0, 20, 20, 'usage')`)
	require.NoError(t, err)
	_, err = app.Exec(ctx, `DELETE FROM calibrations`)
	assert.Equal(t, "42501", pgCode(err), "calibrations are append-only for the app role")
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	cfg := testConfig()
	require.NoError(t, storetest.Bootstrap(ctx, t, db, cfg))

	app, err := pgx.Connect(ctx, db.AppDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Close(ctx) })
	_, err = app.Exec(ctx, "SET search_path = mem_personal, public")
	require.NoError(t, err)

	_, err = app.Exec(ctx, `INSERT INTO memories (id, owner, type, title, content, source, status, content_hash)
		VALUES (gen_random_uuid(), 'dev', 'user', 't', 'c', 'user_stated', 'active', '\x00')`)
	require.NoError(t, err)
	_, err = app.Exec(ctx, `UPDATE memories SET title = 'u'`)
	require.NoError(t, err)

	_, err = app.Exec(ctx, `DELETE FROM memories`)
	assert.Equal(t, "42501", pgCode(err), "app role must not delete")

	_, err = app.Exec(ctx, `INSERT INTO audit_log (owner, action) VALUES ('dev', 'x')`)
	require.NoError(t, err)
	_, err = app.Exec(ctx, `UPDATE audit_log SET action = 'y'`)
	assert.Equal(t, "42501", pgCode(err), "audit log is append-only")

	_, err = app.Exec(ctx, `INSERT INTO mem_profile.memories (id, owner, type, title, content, source, status, content_hash)
		VALUES (gen_random_uuid(), 'dev', 'user', 't', 'c', 'user_stated', 'active', '\x00')`)
	assert.Equal(t, "42501", pgCode(err), "profile is read-only for the app role")

	_, err = app.Exec(ctx, `SELECT count(*) FROM mem_profile.memories`)
	require.NoError(t, err)

	_, err = app.Exec(ctx, `CREATE TABLE public.sneaky (id int)`)
	assert.Equal(t, "42501", pgCode(err), "app role must not create objects")
}

func TestMigrate_Dimension(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	require.NoError(t, storetest.Bootstrap(ctx, t, db, testConfig()))

	conn, err := pgx.Connect(ctx, db.AdminDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })

	for _, schema := range []string{"mem_personal", "mem_work", "mem_profile"} {
		var typ string
		require.NoError(t, conn.QueryRow(ctx, `SELECT format_type(a.atttypid, a.atttypmod)
			FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = 'memories' AND a.attname = 'embedding'`, schema).Scan(&typ))
		assert.Equal(t, "vector(768)", typ, schema)
	}
}
