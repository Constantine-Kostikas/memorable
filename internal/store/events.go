package store

import (
	"context"

	"github.com/google/uuid"
)

// LogSearch records a search and returns its id.
func (s *Store) LogSearch(ctx context.Context, query string, project *string, returned []uuid.UUID) (uuid.UUID, error) {
	id := newID()
	if returned == nil {
		returned = []uuid.UUID{}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO access_events (id, owner, kind, query, project, returned_ids)
		VALUES ($1, $2, 'search', $3, $4, $5)`, id, s.owner, query, project, returned)
	if err != nil {
		return uuid.Nil, mapErr("log search", err)
	}
	return id, nil
}

// LogGet records a fetch, linked to the search that surfaced it when known.
func (s *Store) LogGet(ctx context.Context, id uuid.UUID, searchID *uuid.UUID, project *string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO access_events (id, owner, kind, project, memory_id, search_id)
		VALUES ($1, $2, 'get', $3, $4, $5)`, newID(), s.owner, project, id, searchID)
	return mapErr("log get", err)
}
