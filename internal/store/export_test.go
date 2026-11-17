//go:build integration

package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// Test hooks exposing the exact SQL the store runs.
var (
	VectorCandidatesSQL  = vectorCandidatesSQL
	FTSCandidatesSQL     = ftsCandidatesSQL
	TrigramCandidatesSQL = trigramCandidatesSQL
	TopRankedSQL         = topRankedSQL
)

// Explain returns the JSON plan for sql run on the store's own sessions. With noSeqScan the planner is
// told to avoid sequential scans, which proves whether a query shape can use an index at all.
func (s *Store) Explain(ctx context.Context, sql string, args []any, noSeqScan bool) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("explain: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if noSeqScan {
		if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
			return "", fmt.Errorf("explain: %w", err)
		}
	}
	var plan []map[string]any
	if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+sql, args...).Scan(&plan); err != nil {
		return "", fmt.Errorf("explain: %w", err)
	}
	b, err := json.Marshal(plan)
	return string(b), err
}

// MigrateDownTo rolls a context schema back to version, for upgrade tests.
func MigrateDownTo(ctx context.Context, adminDSN, schema string, dims int, version int64) error {
	db, err := openSchemaDB(adminDSN, schema)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	p, err := newProvider(db, schema, SchemaContext, dims)
	if err != nil {
		return err
	}
	_, err = p.DownTo(ctx, version)
	return err
}

// Setting returns a run-time parameter as the store's own sessions see it.
func (s *Store) Setting(ctx context.Context, name string) (string, error) {
	var v string
	err := s.pool.QueryRow(ctx, "SELECT current_setting($1)", name).Scan(&v)
	return v, err
}
