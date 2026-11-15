package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

// Activity returns a memory's feedback and audit entries, newest first.
func (s *Store) Activity(ctx context.Context, id uuid.UUID) (memory.Activity, error) {
	feedback, err := s.feedbackFor(ctx, id)
	if err != nil {
		return memory.Activity{}, err
	}
	audit, err := s.auditFor(ctx, id)
	if err != nil {
		return memory.Activity{}, err
	}
	return memory.Activity{Feedback: feedback, Audit: audit}, nil
}

func (s *Store) feedbackFor(ctx context.Context, id uuid.UUID) ([]memory.FeedbackEntry, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind, note, project, created_at FROM memory_feedback
		WHERE memory_id = $1 AND owner = $2 ORDER BY created_at DESC, id DESC`, id, s.owner)
	if err != nil {
		return nil, mapErr("feedback activity", err)
	}
	type feedbackRow struct {
		Kind      string    `db:"kind"`
		Note      string    `db:"note"`
		Project   *string   `db:"project"`
		CreatedAt time.Time `db:"created_at"`
	}
	raw, err := pgx.CollectRows(rows, pgx.RowToStructByName[feedbackRow])
	if err != nil {
		return nil, mapErr("feedback activity", err)
	}
	out := make([]memory.FeedbackEntry, 0, len(raw))
	for _, r := range raw {
		kind, err := memory.ParseFeedbackKind(r.Kind)
		if err != nil {
			return nil, err //nolint:wrapcheck // parse errors already name the value
		}
		out = append(out, memory.FeedbackEntry{Kind: kind, Note: r.Note, Project: r.Project, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

func (s *Store) auditFor(ctx context.Context, id uuid.UUID) ([]memory.AuditRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT action, coalesce(process, '') AS process, coalesce(cwd, '') AS cwd, details, created_at
		FROM audit_log WHERE memory_id = $1 AND owner = $2 ORDER BY created_at DESC, id DESC`, id, s.owner)
	if err != nil {
		return nil, mapErr("audit activity", err)
	}
	type auditRow struct {
		Action    string         `db:"action"`
		Process   string         `db:"process"`
		Cwd       string         `db:"cwd"`
		Details   map[string]any `db:"details"`
		CreatedAt time.Time      `db:"created_at"`
	}
	raw, err := pgx.CollectRows(rows, pgx.RowToStructByName[auditRow])
	if err != nil {
		return nil, mapErr("audit activity", err)
	}
	out := make([]memory.AuditRecord, 0, len(raw))
	for _, r := range raw {
		out = append(out, memory.AuditRecord{Action: r.Action, Process: r.Process, Cwd: r.Cwd, Details: r.Details, CreatedAt: r.CreatedAt})
	}
	return out, nil
}
