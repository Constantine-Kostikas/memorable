package store

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const reviewBucketLimit = 50

// List pages through context memories by id; statuses, when given, replace the filter's status set.
func (s *Store) List(ctx context.Context, f memory.Filter, statuses []memory.Status, cursor *uuid.UUID, limit int) ([]memory.Memory, *uuid.UUID, error) {
	a := &sqlArgs{}
	names := visibleStatuses(f)
	if statuses != nil {
		names = make([]string, 0, len(statuses))
		for _, st := range statuses {
			names = append(names, st.String())
		}
	}
	pred := localPredicateWith(f, a, names)
	if cursor != nil {
		pred += " AND id > " + a.add(*cursor)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+memCols+`, false AS is_profile FROM memories WHERE `+pred+
		` ORDER BY id LIMIT `+a.add(limit+1), a.args...)
	if err != nil {
		return nil, nil, mapErr("list", err)
	}
	out, err := collectMemories(rows, "list")
	if err != nil {
		return nil, nil, err
	}
	if len(out) <= limit {
		return out, nil, nil
	}
	next := out[limit-1].ID
	return out[:limit], &next, nil
}

// Review collects memories needing attention.
func (s *Store) Review(ctx context.Context, now time.Time, staleAfter, halfLife time.Duration) (memory.ReviewQueue, error) {
	var q memory.ReviewQueue
	staleKey := math.Log(0.5) + math.Ln2/halfLife.Seconds()*float64(now.Unix())
	buckets := []struct {
		dst   *[]memory.Memory
		where string
		args  []any
	}{
		{&q.Flagged, "status = 'flagged'", nil},
		{&q.Expired, "status IN ('active', 'unverified') AND expires_at IS NOT NULL AND expires_at <= $1", []any{now}},
		{&q.Unverified, "status = 'unverified'", nil},
		{&q.Stale, "status = 'active' AND NOT pinned AND coalesce(last_accessed_at, created_at) < $1 AND rank_key < $2",
			[]any{now.Add(-staleAfter), staleKey}},
	}
	for _, b := range buckets {
		rows, err := s.pool.Query(ctx, `SELECT `+memCols+`, false AS is_profile FROM memories WHERE `+b.where+
			` ORDER BY updated_at DESC LIMIT `+strconvItoa(reviewBucketLimit), b.args...)
		if err != nil {
			return q, mapErr("review", err)
		}
		if *b.dst, err = collectMemories(rows, "review"); err != nil {
			return q, err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT from_id, to_id, kind FROM memory_links WHERE kind = 'contradicts' AND removed_at IS NULL`)
	if err != nil {
		return q, mapErr("review conflicts", err)
	}
	q.Conflicts, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (memory.Link, error) {
		var l memory.Link
		err := r.Scan(&l.From, &l.To, &l.Kind)
		return l, err //nolint:wrapcheck // wrapped by mapErr below
	})
	if err != nil {
		return q, mapErr("review conflicts", err)
	}
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM memories WHERE status = 'quarantined'`).Scan(&q.QuarantinedCount)
	return q, mapErr("review quarantined", err)
}
