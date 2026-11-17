package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations
var migrationsFS embed.FS

// SchemaKind selects the migration set for a schema.
type SchemaKind uint8

// Schema kinds.
const (
	SchemaUnknown SchemaKind = iota
	SchemaContext
	SchemaProfile
)

const embeddingMigrationVersion = 2

const latestMigrationVersion = 3

// LatestVersion returns the newest migration version for a schema kind.
func LatestVersion(SchemaKind) int64 { return latestMigrationVersion }

// Migrate applies pending migrations to schema and (re)applies the app role's grants.
// It returns the versions applied by this call.
func Migrate(ctx context.Context, adminDSN, schema string, kind SchemaKind, dims int, appUser string) ([]int64, error) {
	db, err := openSchemaDB(adminDSN, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	sub := "migrations/context"
	if kind == SchemaProfile {
		sub = "migrations/profile"
	}
	fsys, err := fs.Sub(migrationsFS, sub)
	if err != nil {
		return nil, fmt.Errorf("opening migrations: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys,
		goose.WithTableName(schema+".goose_db_version"),
		goose.WithDisableGlobalRegistry(true),
		goose.WithGoMigrations(goose.NewGoMigration(embeddingMigrationVersion,
			&goose.GoFunc{RunTx: addEmbeddingColumn(dims, kind)},
			&goose.GoFunc{RunTx: dropEmbeddingColumn},
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("creating migration provider: %w", err)
	}
	results, err := p.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrating %s: %w", schema, err)
	}
	applied := make([]int64, 0, len(results))
	for _, r := range results {
		applied = append(applied, r.Source.Version)
	}
	if err := grant(ctx, db, schema, kind, appUser); err != nil {
		return applied, err
	}
	return applied, nil
}

func openSchemaDB(dsn, schema string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing admin dsn: %w", err)
	}
	cfg.RuntimeParams["search_path"] = schema + ",public"
	return stdlib.OpenDB(*cfg), nil
}

func addEmbeddingColumn(dims int, kind SchemaKind) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		if dims <= 0 || dims > 16000 {
			return fmt.Errorf("embedding dimensions %d out of range", dims)
		}
		stmts := []string{
			fmt.Sprintf("ALTER TABLE memories ADD COLUMN embedding vector(%d)", dims),
			"CREATE INDEX memories_embedding_idx ON memories USING hnsw (embedding vector_cosine_ops)",
		}
		if kind == SchemaContext {
			stmts = append(stmts, "CREATE INDEX memories_content_trgm_idx ON memories USING gin (content gin_trgm_ops)")
		}
		for _, s := range stmts {
			if _, err := tx.ExecContext(ctx, s); err != nil {
				return fmt.Errorf("embedding migration %q: %w", s, err)
			}
		}
		return nil
	}
}

func dropEmbeddingColumn(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "ALTER TABLE memories DROP COLUMN IF EXISTS embedding"); err != nil {
		return fmt.Errorf("dropping embedding column: %w", err)
	}
	return nil
}

func grant(ctx context.Context, db *sql.DB, schema string, kind SchemaKind, appUser string) error {
	s := pgx.Identifier{schema}.Sanitize()
	app := pgx.Identifier{appUser}.Sanitize()
	var stmts []string
	if kind == SchemaProfile {
		stmts = []string{
			"GRANT SELECT ON ALL TABLES IN SCHEMA " + s + " TO " + app,
		}
	} else {
		stmts = []string{
			"GRANT SELECT, INSERT, UPDATE ON " + qualify(s, "memories", "memory_links", "memory_project_access", "embedding_jobs") + " TO " + app,
			"GRANT SELECT, INSERT ON " + qualify(s, "memory_versions", "memory_feedback", "access_events", "audit_log") + " TO " + app,
			"GRANT USAGE ON ALL SEQUENCES IN SCHEMA " + s + " TO " + app,
		}
	}
	stmts = append(stmts, "GRANT SELECT ON "+s+".goose_db_version TO "+app)
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("granting on %s: %w", schema, err)
		}
	}
	return nil
}

func qualify(schema string, tables ...string) string {
	out := ""
	for i, t := range tables {
		if i > 0 {
			out += ", "
		}
		out += schema + "." + pgx.Identifier{t}.Sanitize()
	}
	return out
}

// ResizeEmbeddings replaces the embedding column of schema with vector(dims), dropping existing vectors.
// It needs the admin role; callers re-embed afterwards.
func ResizeEmbeddings(ctx context.Context, adminDSN, schema string, dims int) error {
	if dims <= 0 || dims > 16000 {
		return fmt.Errorf("embedding dimensions %d out of range", dims)
	}
	db, err := openSchemaDB(adminDSN, schema)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("resizing embeddings: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		"DROP INDEX IF EXISTS memories_embedding_idx",
		"ALTER TABLE memories DROP COLUMN IF EXISTS embedding",
		fmt.Sprintf("ALTER TABLE memories ADD COLUMN embedding vector(%d)", dims),
		"CREATE INDEX memories_embedding_idx ON memories USING hnsw (embedding vector_cosine_ops)",
		"UPDATE memories SET embedding_model = NULL, embedded_at = NULL",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("resizing embeddings in %s: %w", schema, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("resizing embeddings in %s: %w", schema, err)
	}
	return nil
}
