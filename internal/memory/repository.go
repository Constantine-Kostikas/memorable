package memory

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AuditEntry describes a change for the append-only audit log; it never carries content.
type AuditEntry struct {
	Action      string
	MemoryID    uuid.UUID
	ContentHash []byte
	Cwd         string
	Process     string
	Details     map[string]any
}

// Version is a historical snapshot of a memory.
type Version struct {
	Version    int
	Title      string
	Content    string
	Tags       []string
	Type       Type
	ChangeKind string
	ChangedAt  time.Time
}

// Link kinds.
const (
	LinkRelated     = "related"
	LinkSupersedes  = "supersedes"
	LinkContradicts = "contradicts"
)

// Link connects two memories.
type Link struct {
	From uuid.UUID
	To   uuid.UUID
	Kind string
}

// Repository persists memories. Implemented by store.Store.
type Repository interface {
	Insert(ctx context.Context, m *Memory, a AuditEntry) error
	InsertSuperseding(ctx context.Context, m *Memory, old uuid.UUID, a AuditEntry) error
	GetByIDs(ctx context.Context, ids []uuid.UUID) ([]Memory, error)
	GetBySlugs(ctx context.Context, owner string, slugs []string) ([]Memory, error)
	FindByHash(ctx context.Context, owner string, hash []byte) (*Memory, error)
	Update(ctx context.Context, id uuid.UUID, expectedVersion *int, p Patch, hash []byte, a AuditEntry) (Memory, error)
	SetStatus(ctx context.Context, id uuid.UUID, s Status, changeKind string, a AuditEntry) error
	Restore(ctx context.Context, id uuid.UUID, version *int, a AuditEntry) (Memory, error)
	History(ctx context.Context, id uuid.UUID) ([]Version, error)
	SetEmbedding(ctx context.Context, id uuid.UUID, version int, vec []float32, model string) error
	RecordAccess(ctx context.Context, id uuid.UUID, project *string, weight float64, now time.Time, halfLife time.Duration) error
	AddFeedback(ctx context.Context, id uuid.UUID, k FeedbackKind, note string, project *string) error
	AddLink(ctx context.Context, l Link) error
	RemoveLink(ctx context.Context, l Link) error
	LinksFor(ctx context.Context, ids []uuid.UUID) ([]Link, error)
	LogSearch(ctx context.Context, query string, project *string, returned []uuid.UUID) (uuid.UUID, error)
	LogGet(ctx context.Context, id uuid.UUID, searchID *uuid.UUID, project *string) error
}
