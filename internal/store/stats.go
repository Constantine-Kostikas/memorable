package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Stats summarizes the context schema.
type Stats struct {
	Total       int            `json:"total"`
	Embedded    int            `json:"embedded"`
	PendingJobs int            `json:"pending_jobs"`
	ByStatus    map[string]int `json:"by_status"`
	ByType      map[string]int `json:"by_type"`
	ByProject   map[string]int `json:"by_project"`
	Recall      RecallStats    `json:"recall"`
}

// recallStatsDays is the window prompt-recall statistics cover.
const recallStatsDays = 30

// RecallStats counts prompt-recall activity over the last Days days.
type RecallStats struct {
	Days int `json:"days"`
	// Injections is how many prompts got a recall block; Shown counts the memories in those blocks, and
	// Fetched how many of them Claude then fetched with the block's search_id.
	Injections int `json:"injections"`
	Shown      int `json:"shown"`
	Fetched    int `json:"fetched"`
}

// Stats counts memories by status, type, and project.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	st := Stats{ByStatus: map[string]int{}, ByType: map[string]int{}, ByProject: map[string]int{}}
	err := s.pool.QueryRow(ctx, `SELECT count(*), count(embedding),
			(SELECT count(*) FROM embedding_jobs WHERE done_at IS NULL)
		FROM memories`).Scan(&st.Total, &st.Embedded, &st.PendingJobs)
	if err != nil {
		return st, mapErr("stats", err)
	}
	for col, dst := range map[string]map[string]int{"status": st.ByStatus, "type": st.ByType, "coalesce(project, '(global)')": st.ByProject} {
		rows, err := s.pool.Query(ctx, `SELECT `+col+`, count(*) FROM memories GROUP BY 1`)
		if err != nil {
			return st, mapErr("stats", err)
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return st, mapErr("stats", err)
			}
			dst[k] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return st, mapErr("stats", err)
		}
	}
	st.Recall.Days = recallStatsDays
	err = s.pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(cardinality(e.returned_ids)), 0),
			coalesce(sum((SELECT count(DISTINCT g.memory_id) FROM access_events g
				WHERE g.kind = 'get' AND g.search_id = e.id AND g.memory_id = ANY(e.returned_ids))), 0)
		FROM access_events e
		WHERE e.owner = $1 AND e.kind = 'inject' AND e.created_at > now() - make_interval(days => $2)`, s.owner, recallStatsDays).
		Scan(&st.Recall.Injections, &st.Recall.Shown, &st.Recall.Fetched)
	return st, mapErr("stats recall", err)
}

// Extensions returns installed extension versions by name.
func (s *Store) Extensions(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT extname, extversion FROM pg_extension`)
	if err != nil {
		return nil, mapErr("extensions", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, version string
		if err := rows.Scan(&name, &version); err != nil {
			return nil, mapErr("extensions", err)
		}
		out[name] = version
	}
	return out, mapErr("extensions", rows.Err())
}

// SchemaVersion returns the newest applied migration of schema, or 0 when it has none.
func (s *Store) SchemaVersion(ctx context.Context, schema string) (int64, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".goose_db_version").Scan(&exists); err != nil {
		return 0, mapErr("schema version", err)
	}
	if !exists {
		return 0, nil
	}
	var v int64
	err := s.pool.QueryRow(ctx, `SELECT coalesce(max(version_id), 0) FROM `+pgx.Identifier{schema, "goose_db_version"}.Sanitize()+
		` WHERE is_applied`).Scan(&v)
	return v, mapErr("schema version", err)
}
