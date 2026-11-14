package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const memCols = `id, owner, slug, project, type, title, content, tags, source, status, pinned, expires_at,
	version, content_hash, embedding_model, access_count, access_score, score_updated_at,
	created_at, updated_at, last_accessed_at, deleted_at`

type memoryRow struct {
	ID             uuid.UUID  `db:"id"`
	Owner          string     `db:"owner"`
	Slug           *string    `db:"slug"`
	Project        *string    `db:"project"`
	Type           string     `db:"type"`
	Title          string     `db:"title"`
	Content        string     `db:"content"`
	Tags           []string   `db:"tags"`
	Source         string     `db:"source"`
	Status         string     `db:"status"`
	Pinned         bool       `db:"pinned"`
	ExpiresAt      *time.Time `db:"expires_at"`
	Version        int        `db:"version"`
	ContentHash    []byte     `db:"content_hash"`
	EmbeddingModel *string    `db:"embedding_model"`
	AccessCount    int64      `db:"access_count"`
	AccessScore    float64    `db:"access_score"`
	ScoreUpdatedAt time.Time  `db:"score_updated_at"`
	CreatedAt      time.Time  `db:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at"`
	LastAccessedAt *time.Time `db:"last_accessed_at"`
	DeletedAt      *time.Time `db:"deleted_at"`
	IsProfile      bool       `db:"is_profile"`
}

func (r memoryRow) toDomain() (memory.Memory, error) {
	typ, err := memory.ParseType(r.Type)
	if err != nil {
		return memory.Memory{}, err
	}
	src, err := memory.ParseSource(r.Source)
	if err != nil {
		return memory.Memory{}, err
	}
	st, err := memory.ParseStatus(r.Status)
	if err != nil {
		return memory.Memory{}, err
	}
	scope := memory.ScopeGlobal
	switch {
	case r.IsProfile:
		scope = memory.ScopeProfile
	case r.Project != nil:
		scope = memory.ScopeProject
	}
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	return memory.Memory{
		ID: r.ID, Owner: r.Owner, Slug: r.Slug, Project: r.Project, Type: typ, Title: r.Title, Content: r.Content,
		Tags: tags, Source: src, Status: st, Pinned: r.Pinned, ExpiresAt: r.ExpiresAt, Version: r.Version,
		ContentHash: r.ContentHash, EmbeddingModel: r.EmbeddingModel, AccessCount: r.AccessCount,
		AccessScore: r.AccessScore, ScoreUpdatedAt: r.ScoreUpdatedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		LastAccessedAt: r.LastAccessedAt, DeletedAt: r.DeletedAt, Scope: scope,
	}, nil
}

func collectMemories(rows pgx.Rows, op string) ([]memory.Memory, error) {
	raw, err := pgx.CollectRows(rows, pgx.RowToStructByName[memoryRow])
	if err != nil {
		return nil, mapErr(op, err)
	}
	out := make([]memory.Memory, 0, len(raw))
	for _, r := range raw {
		m, err := r.toDomain()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op, err)
		}
		out = append(out, m)
	}
	return out, nil
}

func insertVersion(ctx context.Context, tx pgx.Tx, m memory.Memory, kind string) error {
	_, err := tx.Exec(ctx, `INSERT INTO memory_versions (memory_id, version, owner, title, content, tags, type, change_kind)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		m.ID, m.Version, m.Owner, m.Title, m.Content, m.Tags, m.Type.String(), kind)
	return mapErr("insert version", err)
}

func insertAudit(ctx context.Context, tx pgx.Tx, owner string, a memory.AuditEntry) error {
	details := a.Details
	if details == nil {
		details = map[string]any{}
	}
	b, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encoding audit details: %w", err)
	}
	var memID *uuid.UUID
	if a.MemoryID != uuid.Nil {
		memID = &a.MemoryID
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log (owner, action, memory_id, content_hash, cwd, process, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, owner, a.Action, memID, a.ContentHash, a.Cwd, a.Process, b)
	return mapErr("insert audit", err)
}

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }
