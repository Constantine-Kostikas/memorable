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

	CandidatesVector(ctx context.Context, vec []float32, f Filter, limit int) ([]Candidate, error)
	CandidatesFTS(ctx context.Context, query string, f Filter, limit int) ([]Candidate, error)
	CandidatesTrigram(ctx context.Context, query string, f Filter, limit int) ([]Candidate, error)
	NearestByVector(ctx context.Context, owner string, vec []float32, limit int) ([]Candidate, error)
	NearestByTrigram(ctx context.Context, owner, content string, limit int) ([]Candidate, error)
	Hydrate(ctx context.Context, ids []uuid.UUID, project *string) ([]Memory, error)
	Top(ctx context.Context, f Filter, limit int) ([]Memory, error)
	List(ctx context.Context, f Filter, statuses []Status, cursor *uuid.UUID, limit int) ([]Memory, *uuid.UUID, error)
	Review(ctx context.Context, now time.Time, staleAfter, halfLife time.Duration) (ReviewQueue, error)
}

// Filter scopes candidate and listing queries.
type Filter struct {
	Owner             string
	Project           *string
	AllProjects       bool
	Tags              []string
	Types             []Type
	IncludeFlagged    bool
	IncludeSuperseded bool
	Now               time.Time
}

// Candidate is a search hit before fusion.
type Candidate struct {
	ID         uuid.UUID
	Similarity float64
}

// ReviewQueue groups memories that need human attention.
type ReviewQueue struct {
	Flagged          []Memory
	Expired          []Memory
	Stale            []Memory
	Unverified       []Memory
	Conflicts        []Link
	QuarantinedCount int
}

// Job is a pending embedding job with the text to embed.
type Job struct {
	MemoryID uuid.UUID
	Version  int
	Attempts int
	Title    string
	Content  string
}

// JobRepository queues embedding work. Implemented by store.Store.
type JobRepository interface {
	EnqueueEmbedding(ctx context.Context, id uuid.UUID, version int) error
	ClaimJobs(ctx context.Context, n int, now time.Time) ([]Job, error)
	CompleteJob(ctx context.Context, id uuid.UUID) error
	FailJob(ctx context.Context, id uuid.UUID, errText string, next time.Time, giveUp bool) error
}

// EmbedKind selects the prompt format for an embedding.
type EmbedKind uint8

// Embed kinds.
const (
	EmbedUnknown EmbedKind = iota
	EmbedQuery
	EmbedDocument
)

// Embedder turns text into vectors. Implemented by embed.Ollama.
type Embedder interface {
	Embed(ctx context.Context, kind EmbedKind, texts []string) ([][]float32, error)
	Model() string
	Dimensions() int
}

// DocumentText formats a memory for document embedding.
func DocumentText(title, content string) string {
	return "title: " + title + " | text: " + content
}
