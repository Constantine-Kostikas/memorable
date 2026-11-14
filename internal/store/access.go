package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/constantine-kostikas/memorable/internal/rank"
)

// RecordAccess bumps the decayed access score globally and for project. Unknown and profile ids are ignored.
func (s *Store) RecordAccess(ctx context.Context, id uuid.UUID, project *string, weight float64, now time.Time, halfLife time.Duration) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		var score float64
		var updated time.Time
		err := tx.QueryRow(ctx, `SELECT access_score, score_updated_at FROM memories WHERE id = $1 FOR UPDATE`, id).Scan(&score, &updated)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return mapErr("record access", err)
		}
		next := rank.Bump(score, updated, now, halfLife, weight)
		_, err = tx.Exec(ctx, `UPDATE memories SET access_count = access_count + 1, access_score = $2,
			score_updated_at = $3, rank_key = $4, last_accessed_at = $3 WHERE id = $1`,
			id, next, now, rank.Key(next, now, halfLife))
		if err != nil {
			return mapErr("record access", err)
		}
		if project == nil {
			return nil
		}
		var pscore float64
		var pupdated time.Time
		err = tx.QueryRow(ctx, `SELECT access_score, score_updated_at FROM memory_project_access
			WHERE memory_id = $1 AND project = $2 FOR UPDATE`, id, *project).Scan(&pscore, &pupdated)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			pscore, pupdated = 0, now
		case err != nil:
			return mapErr("record project access", err)
		}
		pnext := rank.Bump(pscore, pupdated, now, halfLife, weight)
		_, err = tx.Exec(ctx, `INSERT INTO memory_project_access
			(memory_id, project, owner, access_count, access_score, score_updated_at, rank_key)
			VALUES ($1, $2, $3, 1, $4, $5, $6)
			ON CONFLICT (memory_id, project) DO UPDATE SET access_count = memory_project_access.access_count + 1,
				access_score = EXCLUDED.access_score, score_updated_at = EXCLUDED.score_updated_at, rank_key = EXCLUDED.rank_key`,
			id, *project, s.owner, pnext, now, rank.Key(pnext, now, halfLife))
		return mapErr("record project access", err)
	})
}
