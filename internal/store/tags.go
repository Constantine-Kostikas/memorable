package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

// TagCounts counts tags across the visible context memories and the profile, most used first.
func (s *Store) TagCounts(ctx context.Context, f memory.Filter, limit int) ([]memory.TagCount, error) {
	a := &sqlArgs{}
	local, profile := localPredicate(f, a), profilePredicate(f, a)
	sql := `SELECT tag, count(*) AS n FROM (
			SELECT unnest(tags) AS tag FROM memories WHERE ` + local + `
			UNION ALL
			SELECT unnest(tags) FROM mem_profile.memories WHERE ` + profile + `
		) t GROUP BY tag ORDER BY n DESC, tag LIMIT ` + a.add(limit)
	rows, err := s.pool.Query(ctx, sql, a.args...)
	if err != nil {
		return nil, mapErr("tag counts", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (memory.TagCount, error) {
		var c memory.TagCount
		err := r.Scan(&c.Tag, &c.Count)
		return c, err //nolint:wrapcheck // wrapped by mapErr below
	})
	return out, mapErr("tag counts", err)
}
