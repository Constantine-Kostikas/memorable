// Package adminops holds the privileged operations shared by the admin CLI commands and the web UI.
package adminops

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const day = 24 * time.Hour

// Admin is the subset of store.Admin that Ops needs.
type Admin interface {
	QuarantinedList(ctx context.Context) ([]memory.Memory, error)
	Approve(ctx context.Context, id uuid.UUID) error
	Purge(ctx context.Context, id uuid.UUID) error
	PruneCount(ctx context.Context, before time.Time) (int64, error)
	Prune(ctx context.Context, before time.Time) (int64, error)
	ProfileInsert(ctx context.Context, m *memory.Memory, vec []float32, model string) error
	ProfileUpdate(ctx context.Context, id uuid.UUID, p memory.Patch, vec []float32, model string) (memory.Memory, error)
	ProfileDelete(ctx context.Context, id uuid.UUID) error
	ProfileList(ctx context.Context) ([]memory.Memory, error)
}

// ProfileInput is the editable content of a profile memory.
type ProfileInput struct {
	Title   string
	Content string
	Tags    []string
	Type    memory.Type
	Pinned  bool
	// ExpiresAt changes the expiry on edit, mirroring memory.Patch: nil leaves it unchanged,
	// a pointer to nil clears it. ProfileAdd ignores it.
	ExpiresAt **time.Time
}

// Ops runs admin operations against an Admin.
type Ops struct {
	admin         Admin
	emb           memory.Embedder
	sec           memory.SecretScanner
	retentionDays int
	now           func() time.Time
}

// New returns Ops. emb may be nil, in which case profile memories are stored without embeddings.
func New(a Admin, emb memory.Embedder, sec memory.SecretScanner, retentionDays int, now func() time.Time) *Ops {
	return &Ops{admin: a, emb: emb, sec: sec, retentionDays: retentionDays, now: now}
}

// QuarantinedList returns quarantined memories awaiting review.
func (o *Ops) QuarantinedList(ctx context.Context) ([]memory.Memory, error) {
	return o.admin.QuarantinedList(ctx) //nolint:wrapcheck // shown to the user as is
}

// Approve releases a memory from quarantine.
func (o *Ops) Approve(ctx context.Context, id uuid.UUID) error {
	return o.admin.Approve(ctx, id) //nolint:wrapcheck // shown to the user as is
}

// Purge irreversibly erases a memory.
func (o *Ops) Purge(ctx context.Context, id uuid.UUID) error {
	return o.admin.Purge(ctx, id) //nolint:wrapcheck // shown to the user as is
}

func (o *Ops) cutoff() time.Time {
	return o.now().Add(-time.Duration(o.retentionDays) * day)
}

// PruneCount reports how many access events Prune would delete.
func (o *Ops) PruneCount(ctx context.Context) (int64, error) {
	return o.admin.PruneCount(ctx, o.cutoff()) //nolint:wrapcheck // shown to the user as is
}

// Prune deletes access events older than the retention period.
func (o *Ops) Prune(ctx context.Context) (int64, error) {
	return o.admin.Prune(ctx, o.cutoff()) //nolint:wrapcheck // shown to the user as is
}

// ProfileList returns non-deleted profile memories.
func (o *Ops) ProfileList(ctx context.Context) ([]memory.Memory, error) {
	return o.admin.ProfileList(ctx) //nolint:wrapcheck // shown to the user as is
}

// ProfileDelete soft-deletes a profile memory.
func (o *Ops) ProfileDelete(ctx context.Context, id uuid.UUID) error {
	return o.admin.ProfileDelete(ctx, id) //nolint:wrapcheck // shown to the user as is
}

// ProfileAdd validates, secret-scans, embeds, and stores a new profile memory.
func (o *Ops) ProfileAdd(ctx context.Context, in ProfileInput) (memory.Memory, error) {
	in.Tags = memory.NormalizeTags(in.Tags)
	n := memory.NewMemory{Title: in.Title, Content: in.Content, Type: in.Type, Tags: in.Tags, Source: memory.SourceUserStated, Pinned: in.Pinned}
	if err := n.Validate(); err != nil {
		return memory.Memory{}, err //nolint:wrapcheck // already contextual
	}
	if err := o.scanText(in.Title, in.Content, in.Tags); err != nil {
		return memory.Memory{}, err
	}
	m := memory.Memory{ID: uuid.Must(uuid.NewV7()), Type: in.Type, Title: in.Title, Content: in.Content,
		Tags: in.Tags, Source: memory.SourceUserStated, Pinned: in.Pinned, ContentHash: memory.ContentHash(in.Title, in.Content)}
	vec, model := o.embedding(ctx, in.Title, in.Content)
	if err := o.admin.ProfileInsert(ctx, &m, vec, model); err != nil {
		return memory.Memory{}, err //nolint:wrapcheck // shown to the user as is
	}
	if vec != nil {
		m.EmbeddingModel = &model
	}
	return m, nil
}

// ProfileEdit applies the differences between in and the stored profile memory.
// Nothing is written when in matches the stored memory.
func (o *Ops) ProfileEdit(ctx context.Context, id uuid.UUID, in ProfileInput) (memory.Memory, error) {
	in.Tags = memory.NormalizeTags(in.Tags)
	mems, err := o.admin.ProfileList(ctx)
	if err != nil {
		return memory.Memory{}, err //nolint:wrapcheck // shown to the user as is
	}
	i := slices.IndexFunc(mems, func(m memory.Memory) bool { return m.ID == id })
	if i < 0 {
		return memory.Memory{}, fmt.Errorf("profile memory %s: %w", id, memory.ErrNotFound)
	}
	cur := mems[i]
	patch := patchFrom(cur, in)
	if isEmpty(patch) {
		return cur, nil
	}
	if err := patch.Validate(); err != nil {
		return memory.Memory{}, err //nolint:wrapcheck // already contextual
	}
	if err := o.scanText(in.Title, in.Content, in.Tags); err != nil {
		return memory.Memory{}, err
	}
	var vec []float32
	model := ""
	if patch.Title != nil || patch.Content != nil {
		vec, model = o.embedding(ctx, in.Title, in.Content)
	}
	return o.admin.ProfileUpdate(ctx, id, patch, vec, model) //nolint:wrapcheck // shown to the user as is
}

func patchFrom(cur memory.Memory, in ProfileInput) memory.Patch {
	var p memory.Patch
	if in.Title != cur.Title {
		p.Title = &in.Title
	}
	if in.Content != cur.Content {
		p.Content = &in.Content
	}
	if !slices.Equal(in.Tags, cur.Tags) {
		p.Tags = &in.Tags
	}
	if in.Type != cur.Type {
		p.Type = &in.Type
	}
	if in.Pinned != cur.Pinned {
		p.Pinned = &in.Pinned
	}
	p.ExpiresAt = in.ExpiresAt
	return p
}

func isEmpty(p memory.Patch) bool {
	return p.Title == nil && p.Content == nil && p.Tags == nil && p.Type == nil && p.Pinned == nil && p.ExpiresAt == nil
}

// embedding returns nil when no embedder is configured or it is unavailable: text search still works.
func (o *Ops) embedding(ctx context.Context, title, content string) ([]float32, string) {
	if o.emb == nil {
		return nil, ""
	}
	vecs, err := o.emb.Embed(ctx, memory.EmbedDocument, []string{memory.DocumentText(title, content)})
	if err != nil || len(vecs) == 0 {
		return nil, ""
	}
	return vecs[0], o.emb.Model()
}

func (o *Ops) scanText(title, content string, tags []string) error {
	text := title + "\n" + content
	for _, t := range tags {
		text += " " + t
	}
	if fs := o.sec.Scan(text); len(fs) > 0 {
		return fmt.Errorf("%w: %s", memory.ErrSecretDetected, fs[0].Rule)
	}
	return nil
}
