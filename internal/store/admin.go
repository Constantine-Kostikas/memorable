package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
)

// Admin performs privileged operations with the admin role.
type Admin struct {
	pool  *pgxpool.Pool
	owner string
}

// OpenAdmin opens an admin-role pool bound to schema.
func OpenAdmin(ctx context.Context, db config.Database, password, schema, owner string) (*Admin, error) {
	adminDB := db
	adminDB.AppUser = db.AdminUser
	s, err := Open(ctx, adminDB, password, schema, owner)
	if err != nil {
		return nil, err
	}
	return &Admin{pool: s.pool, owner: owner}, nil
}

// Close releases the pool.
func (a *Admin) Close() { a.pool.Close() }

// Ping checks connectivity and credentials.
func (a *Admin) Ping(ctx context.Context) error {
	if err := a.pool.Ping(ctx); err != nil {
		return fmt.Errorf("%w: database: %w", memory.ErrUnavailable, err)
	}
	return nil
}

func (a *Admin) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return (&Store{pool: a.pool}).withTx(ctx, fn)
}

// QuarantinedList returns quarantined memories for human review.
func (a *Admin) QuarantinedList(ctx context.Context) ([]memory.Memory, error) {
	rows, err := a.pool.Query(ctx, `SELECT `+memCols+`, false AS is_profile FROM memories WHERE status = 'quarantined' ORDER BY created_at`)
	if err != nil {
		return nil, mapErr("quarantined list", err)
	}
	return collectMemories(rows, "quarantined list")
}

// Approve releases a memory from quarantine.
func (a *Admin) Approve(ctx context.Context, id uuid.UUID) error {
	return a.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockMemory(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Status != memory.StatusQuarantined {
			return fmt.Errorf("%w: memory %s is not quarantined", memory.ErrValidation, id)
		}
		st := memory.StatusActive
		if cur.Source == memory.SourceToolOutput {
			st = memory.StatusUnverified
		}
		return setStatus(ctx, tx, a.owner, id, st, "status", memory.AuditEntry{Action: "approve"})
	})
}

// Purge irreversibly erases a memory and everything that references it; the audit log keeps only its hash.
func (a *Admin) Purge(ctx context.Context, id uuid.UUID) error {
	return a.withTx(ctx, func(tx pgx.Tx) error {
		var hash []byte
		if err := tx.QueryRow(ctx, `SELECT content_hash FROM memories WHERE id = $1`, id).Scan(&hash); err != nil {
			return mapErr("purge", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM access_events WHERE memory_id = $1 OR $1 = ANY(returned_ids)`, id); err != nil {
			return mapErr("purge events", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM memories WHERE id = $1`, id); err != nil {
			return mapErr("purge memory", err)
		}
		return insertAudit(ctx, tx, a.owner, memory.AuditEntry{Action: "purge", MemoryID: id, ContentHash: hash})
	})
}

// Prune deletes access events older than before.
func (a *Admin) Prune(ctx context.Context, before time.Time) (int64, error) {
	tag, err := a.pool.Exec(ctx, `DELETE FROM access_events WHERE created_at < $1`, before)
	if err != nil {
		return 0, mapErr("prune", err)
	}
	return tag.RowsAffected(), nil
}

// ProfileInsert stores a profile memory; vec may be nil when no embedding is available.
func (a *Admin) ProfileInsert(ctx context.Context, m *memory.Memory, vec []float32, model string) error {
	return a.withTx(ctx, func(tx pgx.Tx) error {
		if m.Tags == nil {
			m.Tags = []string{}
		}
		var emb any
		var embModel *string
		if vec != nil {
			emb, embModel = pgvector.NewVector(vec), &model
		}
		err := tx.QueryRow(ctx, `INSERT INTO mem_profile.memories
			(id, owner, slug, type, title, content, tags, source, status, pinned, expires_at, version, content_hash,
			 embedding, embedding_model, embedded_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'active', $9, $10, 1, $11, $12, $13, CASE WHEN $12::vector IS NULL THEN NULL ELSE now() END)
			RETURNING created_at, updated_at`,
			m.ID, m.Owner, m.Slug, m.Type.String(), m.Title, m.Content, m.Tags, m.Source.String(), m.Pinned, m.ExpiresAt,
			m.ContentHash, emb, embModel).Scan(&m.CreatedAt, &m.UpdatedAt)
		if err != nil {
			return mapErr("profile insert", err)
		}
		m.Status, m.Version, m.Scope = memory.StatusActive, 1, memory.ScopeProfile
		_, err = tx.Exec(ctx, `INSERT INTO mem_profile.memory_versions (memory_id, version, owner, title, content, tags, type, change_kind)
			VALUES ($1, 1, $2, $3, $4, $5, $6, 'create')`, m.ID, m.Owner, m.Title, m.Content, m.Tags, m.Type.String())
		return mapErr("profile version", err)
	})
}

// ProfileUpdate applies a patch to a profile memory; vec replaces the embedding when text changed.
func (a *Admin) ProfileUpdate(ctx context.Context, id uuid.UUID, p memory.Patch, vec []float32, model string) (memory.Memory, error) {
	var out memory.Memory
	err := a.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+memCols+`, true AS is_profile FROM mem_profile.memories WHERE id = $1 FOR UPDATE`, id)
		if err != nil {
			return mapErr("profile lock", err)
		}
		ms, err := collectMemories(rows, "profile lock")
		if err != nil {
			return err
		}
		if len(ms) == 0 {
			return fmt.Errorf("profile memory %s: %w", id, memory.ErrNotFound)
		}
		next := applyPatch(ms[0], p)
		var emb any
		if vec != nil {
			emb = pgvector.NewVector(vec)
		}
		rows, err = tx.Query(ctx, `UPDATE mem_profile.memories SET title = $2, content = $3, tags = $4, type = $5, pinned = $6,
				expires_at = $7, content_hash = $8, version = version + 1, updated_at = now(),
				embedding = CASE WHEN $9::vector IS NULL THEN embedding ELSE $9::vector END,
				embedding_model = CASE WHEN $9::vector IS NULL THEN embedding_model ELSE $10 END
			WHERE id = $1 RETURNING `+memCols+`, true AS is_profile`,
			id, next.Title, next.Content, next.Tags, next.Type.String(), next.Pinned, next.ExpiresAt,
			memory.ContentHash(next.Title, next.Content), emb, model)
		if err != nil {
			return mapErr("profile update", err)
		}
		ms, err = collectMemories(rows, "profile update")
		if err != nil {
			return err
		}
		out = ms[0]
		_, err = tx.Exec(ctx, `INSERT INTO mem_profile.memory_versions (memory_id, version, owner, title, content, tags, type, change_kind)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'update')`, out.ID, out.Version, out.Owner, out.Title, out.Content, out.Tags, out.Type.String())
		return mapErr("profile version", err)
	})
	return out, err
}

// ProfileDelete soft-deletes a profile memory.
func (a *Admin) ProfileDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := a.pool.Exec(ctx, `UPDATE mem_profile.memories SET status = 'deleted', deleted_at = now(), updated_at = now()
		WHERE id = $1 AND status <> 'deleted'`, id)
	if err != nil {
		return mapErr("profile delete", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("profile memory %s: %w", id, memory.ErrNotFound)
	}
	return nil
}

// ProfileList returns non-deleted profile memories.
func (a *Admin) ProfileList(ctx context.Context) ([]memory.Memory, error) {
	rows, err := a.pool.Query(ctx, `SELECT `+memCols+`, true AS is_profile FROM mem_profile.memories
		WHERE status <> 'deleted' ORDER BY created_at`)
	if err != nil {
		return nil, mapErr("profile list", err)
	}
	return collectMemories(rows, "profile list")
}
