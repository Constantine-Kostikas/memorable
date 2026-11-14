package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

var _ memory.JobRepository = (*Store)(nil)

const jobLease = 5 * time.Minute

func strconvItoa(n int) string { return strconv.Itoa(n) }

// EnqueueEmbedding schedules (or reschedules) an embedding for a memory version.
func (s *Store) EnqueueEmbedding(ctx context.Context, id uuid.UUID, version int) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO embedding_jobs (memory_id, owner, target_version) VALUES ($1, $2, $3)
		ON CONFLICT (memory_id) DO UPDATE SET target_version = EXCLUDED.target_version, attempts = 0,
			next_attempt_at = now(), last_error = NULL, done_at = NULL`, id, s.owner, version)
	return mapErr("enqueue embedding", err)
}

// ClaimJobs leases up to n due jobs; concurrent workers never receive the same job.
func (s *Store) ClaimJobs(ctx context.Context, n int, now time.Time) ([]memory.Job, error) {
	var jobs []memory.Job
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT j.memory_id, j.target_version, j.attempts, v.title, v.content
			FROM embedding_jobs j JOIN memory_versions v ON v.memory_id = j.memory_id AND v.version = j.target_version
			WHERE j.done_at IS NULL AND j.next_attempt_at <= $1
			ORDER BY j.next_attempt_at LIMIT $2 FOR UPDATE OF j SKIP LOCKED`, now, n)
		if err != nil {
			return mapErr("claim jobs", err)
		}
		jobs, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (memory.Job, error) {
			var j memory.Job
			err := r.Scan(&j.MemoryID, &j.Version, &j.Attempts, &j.Title, &j.Content)
			return j, err //nolint:wrapcheck // wrapped by mapErr below
		})
		if err != nil {
			return mapErr("claim jobs", err)
		}
		if len(jobs) == 0 {
			return nil
		}
		claimed := make([]uuid.UUID, 0, len(jobs))
		for _, j := range jobs {
			claimed = append(claimed, j.MemoryID)
		}
		_, err = tx.Exec(ctx, `UPDATE embedding_jobs SET next_attempt_at = $2 WHERE memory_id = ANY($1)`, claimed, now.Add(jobLease))
		return mapErr("lease jobs", err)
	})
	return jobs, err
}

// CompleteJob marks a job done.
func (s *Store) CompleteJob(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE embedding_jobs SET done_at = now(), last_error = NULL WHERE memory_id = $1`, id)
	return mapErr("complete job", err)
}

// FailJob records a failure and schedules the next attempt, or gives up.
func (s *Store) FailJob(ctx context.Context, id uuid.UUID, errText string, next time.Time, giveUp bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE embedding_jobs SET attempts = attempts + 1, last_error = $2, next_attempt_at = $3,
		done_at = CASE WHEN $4 THEN now() ELSE NULL END WHERE memory_id = $1`, id, errText, next, giveUp)
	return mapErr("fail job", err)
}

// EnqueueAll schedules a fresh embedding for every non-deleted memory and returns how many were queued.
func (s *Store) EnqueueAll(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO embedding_jobs (memory_id, owner, target_version)
		SELECT id, owner, version FROM memories WHERE status NOT IN ('deleted', 'superseded')
		ON CONFLICT (memory_id) DO UPDATE SET target_version = EXCLUDED.target_version, attempts = 0,
			next_attempt_at = now(), last_error = NULL, done_at = NULL`)
	if err != nil {
		return 0, mapErr("enqueue all", err)
	}
	return tag.RowsAffected(), nil
}

// EmbeddingDimensions returns the vector size of the embedding column.
func (s *Store) EmbeddingDimensions(ctx context.Context) (int, error) {
	var typ string
	err := s.pool.QueryRow(ctx, `SELECT format_type(a.atttypid, a.atttypmod) FROM pg_attribute a
		WHERE a.attrelid = 'memories'::regclass AND a.attname = 'embedding' AND NOT a.attisdropped`).Scan(&typ)
	if err != nil {
		return 0, mapErr("embedding dimensions", err)
	}
	return parseVectorType(typ)
}

func parseVectorType(typ string) (int, error) {
	inner := strings.TrimSuffix(strings.TrimPrefix(typ, "vector("), ")")
	n, err := strconv.Atoi(inner)
	if err != nil {
		return 0, fmt.Errorf("unexpected embedding column type %q", typ)
	}
	return n, nil
}
