package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

// AddLink creates or revives a link.
func (s *Store) AddLink(ctx context.Context, l memory.Link) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO memory_links (from_id, to_id, kind, owner) VALUES ($1, $2, $3, $4)
		ON CONFLICT (from_id, to_id, kind) DO UPDATE SET removed_at = NULL`, l.From, l.To, l.Kind, s.owner)
	return mapErr("add link", err)
}

// RemoveLink soft-removes a link.
func (s *Store) RemoveLink(ctx context.Context, l memory.Link) error {
	_, err := s.pool.Exec(ctx, `UPDATE memory_links SET removed_at = now()
		WHERE from_id = $1 AND to_id = $2 AND kind = $3 AND removed_at IS NULL`, l.From, l.To, l.Kind)
	return mapErr("remove link", err)
}

// LinksFor returns active links touching any of ids.
func (s *Store) LinksFor(ctx context.Context, ids []uuid.UUID) ([]memory.Link, error) {
	rows, err := s.pool.Query(ctx, `SELECT from_id, to_id, kind FROM memory_links
		WHERE (from_id = ANY($1) OR to_id = ANY($1)) AND removed_at IS NULL ORDER BY created_at`, ids)
	if err != nil {
		return nil, mapErr("links", err)
	}
	links, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (memory.Link, error) {
		var l memory.Link
		err := r.Scan(&l.From, &l.To, &l.Kind)
		return l, err //nolint:wrapcheck // wrapped by mapErr below
	})
	return links, mapErr("links", err)
}

// AddFeedback records feedback on a memory.
func (s *Store) AddFeedback(ctx context.Context, id uuid.UUID, k memory.FeedbackKind, note string, project *string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO memory_feedback (id, memory_id, owner, kind, note, project)
		VALUES ($1, $2, $3, $4, $5, $6)`, newID(), id, s.owner, k.String(), note, project)
	return mapErr("add feedback", err)
}
