package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/rank"
)

// LogEvent records a search, injection, or preload and returns its id.
func (s *Store) LogEvent(ctx context.Context, e memory.Event) (uuid.UUID, error) {
	id := newID()
	returned := e.Returned
	if returned == nil {
		returned = []uuid.UUID{}
	}
	details, err := json.Marshal(e.Details)
	if err != nil {
		return uuid.Nil, fmt.Errorf("log event: encoding details: %w", err)
	}
	var session *string
	if e.SessionID != "" {
		session = &e.SessionID
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO access_events (id, owner, kind, query, project, returned_ids, session_id, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, s.owner, string(e.Kind), e.Query, e.Project, returned, session, details)
	if err != nil {
		return uuid.Nil, mapErr("log event", err)
	}
	return id, nil
}

// LogSearch records a search and returns its id. It is a shorthand for LogEvent.
func (s *Store) LogSearch(ctx context.Context, query string, project *string, returned []uuid.UUID) (uuid.UUID, error) {
	return s.LogEvent(ctx, memory.Event{Kind: memory.EventSearch, Query: &query, Project: project, Returned: returned})
}

// LogGet records a fetch, linked to the search that surfaced it when known.
func (s *Store) LogGet(ctx context.Context, id uuid.UUID, searchID *uuid.UUID, project *string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO access_events (id, owner, kind, project, memory_id, search_id)
		VALUES ($1, $2, 'get', $3, $4, $5)`, newID(), s.owner, project, id, searchID)
	return mapErr("log get", err)
}

// sessionShownSQL lists ids shown in a session since its newest context reset. Event ids are UUIDv7, so
// they order by creation time.
const sessionShownSQL = `WITH reset AS (
	SELECT (SELECT id FROM access_events
		WHERE owner = $1 AND session_id = $2 AND kind = 'preload'
			AND details->>'hook_source' IN ('startup', 'clear', 'compact')
		ORDER BY id DESC LIMIT 1) AS id)
SELECT DISTINCT shown FROM access_events e CROSS JOIN reset CROSS JOIN LATERAL unnest(e.returned_ids) AS shown
WHERE e.owner = $1 AND e.session_id = $2 AND e.kind IN ('preload', 'inject') AND (reset.id IS NULL OR e.id >= reset.id)`

// SessionShown implements memory.Repository.
func (s *Store) SessionShown(ctx context.Context, sessionID string) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, sessionShownSQL, s.owner, sessionID)
	if err != nil {
		return nil, mapErr("session shown", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, mapErr("session shown", err)
		}
		out = append(out, id)
	}
	return out, mapErr("session shown", rows.Err())
}

const labeledResultsSQL = `SELECT (sig->>'sim')::float8, (sig->>'fts')::bool, (sig->>'tri')::bool,
	coalesce((sig->>'vrank')::int, 0),
	EXISTS (SELECT 1 FROM access_events g
		WHERE g.owner = e.owner AND g.kind = 'get' AND g.search_id = e.id AND g.memory_id = (sig->>'id')::uuid)
FROM access_events e CROSS JOIN LATERAL jsonb_array_elements(e.details->'signals') AS sig
WHERE e.owner = $1 AND e.created_at < $2
	AND (e.kind = 'inject' OR (e.kind = 'search' AND e.details->>'origin' = 'mcp'))
	AND e.details->>'model' = $3 AND (e.details->>'dims')::int = $4
	AND EXISTS (SELECT 1 FROM memories m WHERE m.id = (sig->>'id')::uuid)`

// LabeledResults implements memory.Repository.
func (s *Store) LabeledResults(ctx context.Context, before time.Time, model string, dims int) ([]rank.Sample, error) {
	rows, err := s.pool.Query(ctx, labeledResultsSQL, s.owner, before, model, dims)
	if err != nil {
		return nil, mapErr("labeled results", err)
	}
	defer rows.Close()
	var out []rank.Sample
	for rows.Next() {
		var smp rank.Sample
		if err := rows.Scan(&smp.Signals.Similarity, &smp.Signals.FTS, &smp.Signals.Trigram, &smp.Signals.VectorRank, &smp.Relevant); err != nil {
			return nil, mapErr("labeled results", err)
		}
		out = append(out, smp)
	}
	return out, mapErr("labeled results", rows.Err())
}
