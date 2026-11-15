package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

// Insert stores a new memory with its first version and an audit entry.
func (s *Store) Insert(ctx context.Context, m *memory.Memory, a memory.AuditEntry) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		return insertMemory(ctx, tx, m, a)
	})
}

// InsertSuperseding stores m, marks old as superseded, and links m to old.
func (s *Store) InsertSuperseding(ctx context.Context, m *memory.Memory, old uuid.UUID, a memory.AuditEntry) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if err := insertMemory(ctx, tx, m, a); err != nil {
			return err
		}
		oldAudit := a
		oldAudit.Action = "supersede"
		oldAudit.MemoryID = old
		oldAudit.ContentHash = nil
		return s.supersede(ctx, tx, old, m.ID, oldAudit)
	})
}

// MarkSuperseded marks old as superseded by an existing replacement and links them.
func (s *Store) MarkSuperseded(ctx context.Context, old, replacement uuid.UUID, a memory.AuditEntry) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		return s.supersede(ctx, tx, old, replacement, a)
	})
}

func (s *Store) supersede(ctx context.Context, tx pgx.Tx, old, replacement uuid.UUID, a memory.AuditEntry) error {
	if err := setStatus(ctx, tx, s.owner, old, memory.StatusSuperseded, "supersede", a, false); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO memory_links (from_id, to_id, kind, owner) VALUES ($1, $2, $3, $4)
		ON CONFLICT (from_id, to_id, kind) DO UPDATE SET removed_at = NULL`, replacement, old, memory.LinkSupersedes, s.owner)
	return mapErr("link supersede", err)
}

func insertMemory(ctx context.Context, tx pgx.Tx, m *memory.Memory, a memory.AuditEntry) error {
	if m.Tags == nil {
		m.Tags = []string{}
	}
	err := tx.QueryRow(ctx, `INSERT INTO memories
		(id, owner, slug, project, type, title, content, tags, source, status, pinned, expires_at, version, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING created_at, updated_at, score_updated_at`,
		m.ID, m.Owner, m.Slug, m.Project, m.Type.String(), m.Title, m.Content, m.Tags, m.Source.String(),
		m.Status.String(), m.Pinned, m.ExpiresAt, m.Version, m.ContentHash,
	).Scan(&m.CreatedAt, &m.UpdatedAt, &m.ScoreUpdatedAt)
	if err != nil {
		return mapErr("insert memory", err)
	}
	if err := insertVersion(ctx, tx, *m, "create"); err != nil {
		return err
	}
	a.MemoryID, a.ContentHash = m.ID, m.ContentHash
	return insertAudit(ctx, tx, m.Owner, a)
}

// GetByIDs returns memories from this context and the profile schema.
func (s *Store) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]memory.Memory, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+memCols+`, false AS is_profile FROM memories WHERE id = ANY($1)
		UNION ALL SELECT `+memCols+`, true FROM mem_profile.memories WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, mapErr("get by ids", err)
	}
	return collectMemories(rows, "get by ids")
}

// GetBySlugs returns non-deleted memories with the given slugs from this context and the profile schema.
func (s *Store) GetBySlugs(ctx context.Context, owner string, slugs []string) ([]memory.Memory, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+memCols+`, false AS is_profile FROM memories
			WHERE owner = $1 AND slug = ANY($2) AND status <> 'deleted'
		UNION ALL SELECT `+memCols+`, true FROM mem_profile.memories
			WHERE owner = $1 AND slug = ANY($2) AND status <> 'deleted'`, owner, slugs)
	if err != nil {
		return nil, mapErr("get by slugs", err)
	}
	return collectMemories(rows, "get by slugs")
}

// FindByHash returns a non-deleted memory with the same content hash, or nil.
func (s *Store) FindByHash(ctx context.Context, owner string, hash []byte) (*memory.Memory, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+memCols+`, false AS is_profile FROM memories
		WHERE owner = $1 AND content_hash = $2 AND status NOT IN ('deleted', 'superseded') LIMIT 1`, owner, hash)
	if err != nil {
		return nil, mapErr("find by hash", err)
	}
	ms, err := collectMemories(rows, "find by hash")
	if err != nil || len(ms) == 0 {
		return nil, err
	}
	return &ms[0], nil
}

func lockMemory(ctx context.Context, tx pgx.Tx, id uuid.UUID) (memory.Memory, error) {
	rows, err := tx.Query(ctx, `SELECT `+memCols+`, false AS is_profile FROM memories WHERE id = $1 FOR UPDATE`, id)
	if err != nil {
		return memory.Memory{}, mapErr("lock memory", err)
	}
	ms, err := collectMemories(rows, "lock memory")
	if err != nil {
		return memory.Memory{}, err
	}
	if len(ms) == 0 {
		return memory.Memory{}, fmt.Errorf("memory %s: %w", id, memory.ErrNotFound)
	}
	return ms[0], nil
}

// Update applies a patch with optimistic concurrency; content changes clear the embedding.
func (s *Store) Update(ctx context.Context, id uuid.UUID, expectedVersion *int, p memory.Patch, hash []byte, a memory.AuditEntry) (memory.Memory, error) {
	var out memory.Memory
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockMemory(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Status == memory.StatusQuarantined {
			return fmt.Errorf("memory %s: %w", id, memory.ErrQuarantined)
		}
		if expectedVersion != nil && *expectedVersion != cur.Version {
			return fmt.Errorf("memory %s is at version %d, not %d: %w", id, cur.Version, *expectedVersion, memory.ErrConflict)
		}
		next := applyPatch(cur, p)
		textChanged := next.Title != cur.Title || next.Content != cur.Content
		if hash == nil {
			hash = memory.ContentHash(next.Title, next.Content)
		}
		out, err = writeMemory(ctx, tx, next, hash, textChanged, p.Status)
		if err != nil {
			return err
		}
		if err := insertVersion(ctx, tx, out, "update"); err != nil {
			return err
		}
		a.MemoryID, a.ContentHash = id, hash
		return insertAudit(ctx, tx, s.owner, a)
	})
	return out, err
}

func applyPatch(m memory.Memory, p memory.Patch) memory.Memory {
	if p.Title != nil {
		m.Title = *p.Title
	}
	if p.Content != nil {
		m.Content = *p.Content
	}
	if p.Tags != nil {
		m.Tags = *p.Tags
	}
	if p.Type != nil {
		m.Type = *p.Type
	}
	if p.Pinned != nil {
		m.Pinned = *p.Pinned
	}
	if p.ExpiresAt != nil {
		m.ExpiresAt = *p.ExpiresAt
	}
	return m
}

// writeMemory persists the editable fields, bumps the version, and returns the stored row.
func writeMemory(ctx context.Context, tx pgx.Tx, m memory.Memory, hash []byte, clearEmbedding bool, status *memory.Status) (memory.Memory, error) {
	st := m.Status
	if status != nil {
		st = *status
	}
	rows, err := tx.Query(ctx, `UPDATE memories SET title = $2, content = $3, tags = $4, type = $5, pinned = $6,
			expires_at = $7, content_hash = $8, status = $9, version = version + 1, updated_at = now(),
			deleted_at = CASE WHEN $9 = 'deleted' THEN coalesce(deleted_at, now()) ELSE NULL END,
			embedding = CASE WHEN $10 THEN NULL ELSE embedding END,
			embedding_model = CASE WHEN $10 THEN NULL ELSE embedding_model END,
			embedded_at = CASE WHEN $10 THEN NULL ELSE embedded_at END
		WHERE id = $1 RETURNING `+memCols+`, false AS is_profile`,
		m.ID, m.Title, m.Content, m.Tags, m.Type.String(), m.Pinned, m.ExpiresAt, hash, st.String(), clearEmbedding)
	if err != nil {
		return memory.Memory{}, mapErr("update memory", err)
	}
	ms, err := collectMemories(rows, "update memory")
	if err != nil {
		return memory.Memory{}, err
	}
	if len(ms) == 0 {
		return memory.Memory{}, fmt.Errorf("memory %s: %w", m.ID, memory.ErrNotFound)
	}
	return ms[0], nil
}

// SetStatus changes the lifecycle status, recording a version and an audit entry.
func (s *Store) SetStatus(ctx context.Context, id uuid.UUID, st memory.Status, changeKind string, a memory.AuditEntry) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		return setStatus(ctx, tx, s.owner, id, st, changeKind, a, false)
	})
}

// setStatus changes a memory's status. Only the admin path may move a memory out of quarantine.
func setStatus(ctx context.Context, tx pgx.Tx, owner string, id uuid.UUID, st memory.Status, changeKind string, a memory.AuditEntry, allowQuarantined bool) error {
	cur, err := lockMemory(ctx, tx, id)
	if err != nil {
		return err
	}
	if cur.Status == memory.StatusQuarantined && !allowQuarantined {
		return fmt.Errorf("memory %s: %w", id, memory.ErrQuarantined)
	}
	out, err := writeMemory(ctx, tx, cur, cur.ContentHash, false, &st)
	if err != nil {
		return err
	}
	if err := insertVersion(ctx, tx, out, changeKind); err != nil {
		return err
	}
	a.MemoryID, a.ContentHash = id, cur.ContentHash
	if a.Details == nil {
		a.Details = map[string]any{}
	}
	a.Details["status"] = st.String()
	return insertAudit(ctx, tx, owner, a)
}

// Restore undeletes a memory, or restores the content of a prior version.
func (s *Store) Restore(ctx context.Context, id uuid.UUID, version *int, a memory.AuditEntry) (memory.Memory, error) {
	var out memory.Memory
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockMemory(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Status == memory.StatusQuarantined {
			return fmt.Errorf("memory %s: %w", id, memory.ErrQuarantined)
		}
		next := cur
		textChanged := false
		if version != nil {
			var typ string
			err := tx.QueryRow(ctx, `SELECT title, content, tags, type FROM memory_versions WHERE memory_id = $1 AND version = $2`,
				id, *version).Scan(&next.Title, &next.Content, &next.Tags, &typ)
			if err != nil {
				return mapErr("load version", err)
			}
			if next.Type, err = memory.ParseType(typ); err != nil {
				return err
			}
			textChanged = next.Title != cur.Title || next.Content != cur.Content
		} else if cur.Status != memory.StatusDeleted {
			return fmt.Errorf("%w: memory %s is not deleted", memory.ErrValidation, id)
		}
		status := cur.Status
		if status == memory.StatusDeleted {
			status = memory.StatusActive
			if cur.Source == memory.SourceToolOutput {
				status = memory.StatusUnverified
			}
		}
		hash := memory.ContentHash(next.Title, next.Content)
		out, err = writeMemory(ctx, tx, next, hash, textChanged, &status)
		if err != nil {
			return err
		}
		if err := insertVersion(ctx, tx, out, "restore"); err != nil {
			return err
		}
		a.MemoryID, a.ContentHash = id, hash
		return insertAudit(ctx, tx, s.owner, a)
	})
	return out, err
}

// History lists all versions of a memory, oldest first.
func (s *Store) History(ctx context.Context, id uuid.UUID) ([]memory.Version, error) {
	rows, err := s.pool.Query(ctx, `SELECT version, title, content, tags, type, change_kind, changed_at
		FROM memory_versions WHERE memory_id = $1 ORDER BY version`, id)
	if err != nil {
		return nil, mapErr("history", err)
	}
	type versionRow struct {
		Version    int       `db:"version"`
		Title      string    `db:"title"`
		Content    string    `db:"content"`
		Tags       []string  `db:"tags"`
		Type       string    `db:"type"`
		ChangeKind string    `db:"change_kind"`
		ChangedAt  time.Time `db:"changed_at"`
	}
	raw, err := pgx.CollectRows(rows, pgx.RowToStructByName[versionRow])
	if err != nil {
		return nil, mapErr("history", err)
	}
	out := make([]memory.Version, 0, len(raw))
	for _, r := range raw {
		typ, err := memory.ParseType(r.Type)
		if err != nil {
			return nil, err
		}
		out = append(out, memory.Version{Version: r.Version, Title: r.Title, Content: r.Content, Tags: r.Tags,
			Type: typ, ChangeKind: r.ChangeKind, ChangedAt: r.ChangedAt})
	}
	return out, nil
}

// SetEmbedding stores a vector computed from the text at version, but only while the memory's
// title and content are still that text (status-only changes since then do not invalidate it).
func (s *Store) SetEmbedding(ctx context.Context, id uuid.UUID, version int, vec []float32, model string) error {
	var title, content string
	err := s.pool.QueryRow(ctx, `SELECT title, content FROM memory_versions WHERE memory_id = $1 AND version = $2`,
		id, version).Scan(&title, &content)
	if err != nil {
		return mapErr("load embedded version", err)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE memories SET embedding = $1, embedding_model = $2, embedded_at = now()
		WHERE id = $3 AND content_hash = $4`, pgvector.NewVector(vec), model, id, memory.ContentHash(title, content))
	if err != nil {
		return mapErr("set embedding", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("embedding for %s version %d is stale: %w", id, version, memory.ErrConflict)
	}
	return nil
}
