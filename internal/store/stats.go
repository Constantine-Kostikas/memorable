package store

import "context"

// Stats summarizes the context schema.
type Stats struct {
	Total       int            `json:"total"`
	Embedded    int            `json:"embedded"`
	PendingJobs int            `json:"pending_jobs"`
	ByStatus    map[string]int `json:"by_status"`
	ByType      map[string]int `json:"by_type"`
	ByProject   map[string]int `json:"by_project"`
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
	return st, nil
}
