package store

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

type sqlArgs struct{ args []any }

func (a *sqlArgs) add(v any) string {
	a.args = append(a.args, v)
	return "$" + strconv.Itoa(len(a.args))
}

func visibleStatuses(f memory.Filter) []string {
	s := []string{memory.StatusActive.String(), memory.StatusUnverified.String()}
	if f.IncludeFlagged {
		s = append(s, memory.StatusFlagged.String())
	}
	if f.IncludeSuperseded {
		s = append(s, memory.StatusSuperseded.String())
	}
	return s
}

func typeNames(ts []memory.Type) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.String())
	}
	return out
}

// localPredicate filters rows of the context schema's memories table.
func localPredicate(f memory.Filter, a *sqlArgs) string {
	return localPredicateWith(f, a, visibleStatuses(f))
}

func localPredicateWith(f memory.Filter, a *sqlArgs, statuses []string) string {
	parts := []string{
		"owner = " + a.add(f.Owner),
		"status = ANY(" + a.add(statuses) + ")",
		"(expires_at IS NULL OR expires_at > " + a.add(f.Now) + ")",
	}
	if !f.AllProjects {
		if f.Project != nil {
			parts = append(parts, "(project IS NULL OR project = "+a.add(*f.Project)+")")
		} else {
			parts = append(parts, "project IS NULL")
		}
	}
	return strings.Join(append(parts, tagTypeParts(f, a)...), " AND ")
}

// profilePredicate filters rows of mem_profile.memories.
func profilePredicate(f memory.Filter, a *sqlArgs) string {
	parts := []string{
		"owner = " + a.add(f.Owner),
		"status = 'active'",
		"(expires_at IS NULL OR expires_at > " + a.add(f.Now) + ")",
	}
	return strings.Join(append(parts, tagTypeParts(f, a)...), " AND ")
}

func tagTypeParts(f memory.Filter, a *sqlArgs) []string {
	var parts []string
	if len(f.Tags) > 0 {
		parts = append(parts, "tags @> "+a.add(f.Tags))
	}
	if len(f.Types) > 0 {
		parts = append(parts, "type = ANY("+a.add(typeNames(f.Types))+")")
	}
	return parts
}

func (s *Store) candidates(ctx context.Context, op, sql string, args []any) ([]memory.Candidate, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(op, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (memory.Candidate, error) {
		var c memory.Candidate
		err := r.Scan(&c.ID, &c.Similarity)
		return c, err //nolint:wrapcheck // wrapped by mapErr below
	})
	return out, mapErr(op, err)
}

func vectorCandidatesSQL(vec []float32, f memory.Filter, limit int) (string, []any) {
	a := &sqlArgs{}
	v := a.add(pgvector.NewVector(vec))
	n := a.add(limit)
	sql := `(SELECT id, 1 - (embedding <=> ` + v + `) AS sim FROM memories
			WHERE embedding IS NOT NULL AND ` + localPredicate(f, a) + ` ORDER BY embedding <=> ` + v + ` LIMIT ` + n + `)
		UNION ALL
		(SELECT id, 1 - (embedding <=> ` + v + `) FROM mem_profile.memories
			WHERE embedding IS NOT NULL AND ` + profilePredicate(f, a) + ` ORDER BY embedding <=> ` + v + ` LIMIT ` + n + `)
		ORDER BY sim DESC LIMIT ` + n
	return sql, a.args
}

func ftsCandidatesSQL(query string, f memory.Filter, limit int) (string, []any) {
	a := &sqlArgs{}
	q := "websearch_to_tsquery('english', " + a.add(query) + ")"
	n := a.add(limit)
	sql := `(SELECT id, ts_rank_cd(fts, ` + q + `) AS sim FROM memories
			WHERE fts @@ ` + q + ` AND ` + localPredicate(f, a) + ` ORDER BY sim DESC LIMIT ` + n + `)
		UNION ALL
		(SELECT id, ts_rank_cd(fts, ` + q + `) FROM mem_profile.memories
			WHERE fts @@ ` + q + ` AND ` + profilePredicate(f, a) + ` ORDER BY 2 DESC LIMIT ` + n + `)
		ORDER BY sim DESC LIMIT ` + n
	return sql, a.args
}

func trigramCandidatesSQL(query string, f memory.Filter, limit int) (string, []any) {
	a := &sqlArgs{}
	q := a.add(query)
	n := a.add(limit)
	sql := `(SELECT id, word_similarity(` + q + `, title) AS sim FROM memories
			WHERE ` + q + ` <% title AND ` + localPredicate(f, a) + ` ORDER BY sim DESC LIMIT ` + n + `)
		UNION ALL
		(SELECT id, word_similarity(` + q + `, title) FROM mem_profile.memories
			WHERE ` + q + ` <% title AND ` + profilePredicate(f, a) + ` ORDER BY 2 DESC LIMIT ` + n + `)
		ORDER BY sim DESC LIMIT ` + n
	return sql, a.args
}

// CandidatesVector returns nearest neighbors by cosine similarity.
func (s *Store) CandidatesVector(ctx context.Context, vec []float32, f memory.Filter, limit int) ([]memory.Candidate, error) {
	sql, args := vectorCandidatesSQL(vec, f, limit)
	return s.candidates(ctx, "vector candidates", sql, args)
}

// CandidatesFTS returns full-text matches ranked by ts_rank_cd.
func (s *Store) CandidatesFTS(ctx context.Context, query string, f memory.Filter, limit int) ([]memory.Candidate, error) {
	sql, args := ftsCandidatesSQL(query, f, limit)
	return s.candidates(ctx, "fts candidates", sql, args)
}

// CandidatesTrigram returns titles that contain a fuzzy match for query.
func (s *Store) CandidatesTrigram(ctx context.Context, query string, f memory.Filter, limit int) ([]memory.Candidate, error) {
	sql, args := trigramCandidatesSQL(query, f, limit)
	return s.candidates(ctx, "trigram candidates", sql, args)
}

const dupStatuses = `status NOT IN ('deleted', 'superseded')`

// NearestByVector finds the closest memories regardless of project, for duplicate detection.
func (s *Store) NearestByVector(ctx context.Context, owner string, vec []float32, limit int) ([]memory.Candidate, error) {
	return s.candidates(ctx, "nearest by vector", `SELECT id, 1 - (embedding <=> $2) FROM memories
		WHERE owner = $1 AND embedding IS NOT NULL AND `+dupStatuses+` ORDER BY embedding <=> $2 LIMIT $3`,
		[]any{owner, pgvector.NewVector(vec), limit})
}

// NearestByTrigram finds memories with similar content, for duplicate detection without embeddings.
func (s *Store) NearestByTrigram(ctx context.Context, owner, content string, limit int) ([]memory.Candidate, error) {
	return s.candidates(ctx, "nearest by trigram", `SELECT id, similarity(content, $2) AS sim FROM memories
		WHERE owner = $1 AND content % $2 AND `+dupStatuses+` ORDER BY sim DESC LIMIT $3`,
		[]any{owner, content, limit})
}

func hydrateCols() string {
	cols := strings.Split(memCols, ",")
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		c = strings.TrimSpace(c)
		switch c {
		case "access_score":
			out = append(out, "coalesce(pa.access_score, m.access_score) AS access_score")
		case "score_updated_at":
			out = append(out, "coalesce(pa.score_updated_at, m.score_updated_at) AS score_updated_at")
		default:
			out = append(out, "m."+c)
		}
	}
	return strings.Join(out, ", ")
}

// Hydrate loads full rows for ids, using the per-project access score when one exists.
func (s *Store) Hydrate(ctx context.Context, ids []uuid.UUID, project *string) ([]memory.Memory, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+hydrateCols()+`, false AS is_profile
			FROM memories m LEFT JOIN memory_project_access pa ON pa.memory_id = m.id AND pa.project = $2
			WHERE m.id = ANY($1)
		UNION ALL SELECT `+memCols+`, true FROM mem_profile.memories WHERE id = ANY($1)`, ids, project)
	if err != nil {
		return nil, mapErr("hydrate", err)
	}
	return collectMemories(rows, "hydrate")
}

func topPinnedSQL(f memory.Filter) (string, []any) {
	a := &sqlArgs{}
	return `SELECT ` + memCols + `, false AS is_profile FROM memories WHERE pinned AND ` + localPredicate(f, a) + `
		UNION ALL SELECT ` + memCols + `, true FROM mem_profile.memories WHERE pinned AND ` + profilePredicate(f, a) + `
		ORDER BY updated_at DESC`, a.args
}

func topRankedSQL(f memory.Filter, limit int) (string, []any) {
	a := &sqlArgs{}
	pred := localPredicate(f, a)
	return `SELECT ` + memCols + `, false AS is_profile FROM memories WHERE NOT pinned AND ` + pred +
		` ORDER BY rank_key DESC LIMIT ` + a.add(limit), a.args
}

// Top returns pinned memories (context and profile) followed by the highest decayed scores.
func (s *Store) Top(ctx context.Context, f memory.Filter, limit int) ([]memory.Memory, error) {
	sql, args := topPinnedSQL(f)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr("top pinned", err)
	}
	out, err := collectMemories(rows, "top pinned")
	if err != nil {
		return nil, err
	}
	if len(out) >= limit {
		return out[:limit], nil
	}
	sql, args = topRankedSQL(f, limit-len(out))
	rows, err = s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr("top ranked", err)
	}
	ranked, err := collectMemories(rows, "top ranked")
	if err != nil {
		return nil, err
	}
	return append(out, ranked...), nil
}
