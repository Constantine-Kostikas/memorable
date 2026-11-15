// Package memorytest provides in-memory fakes of memory's ports for tests.
package memorytest

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/rank"
)

type feedbackRow struct {
	memoryID uuid.UUID
	entry    memory.FeedbackEntry
}

type auditRow struct {
	memoryID uuid.UUID
	record   memory.AuditRecord
}

// Repo is an in-memory memory.Repository and memory.JobRepository.
type Repo struct {
	mu        sync.Mutex
	mems      map[uuid.UUID]*memory.Memory
	profile   map[uuid.UUID]*memory.Memory
	vecs      map[uuid.UUID][]float32
	versions  map[uuid.UUID][]memory.Version
	links     []memory.Link
	feedback  []feedbackRow
	auditLog  []auditRow
	jobs      map[uuid.UUID]*memory.Job
	jobDone   map[uuid.UUID]bool
	events    []string
	projScore map[string]float64

	// NearestOverride, when set, replaces NearestByVector and NearestByTrigram results.
	NearestOverride func() []memory.Candidate
	// Err, when set, is returned by every method.
	Err error
	// Audits records audit actions in order.
	Audits []string
	// OnGet, when set, runs after GetByIDs returns (outside the lock), to simulate a concurrent writer.
	OnGet func()
	// VectorErr, when set, fails vector queries only.
	VectorErr error
}

var (
	_ memory.Repository    = (*Repo)(nil)
	_ memory.JobRepository = (*Repo)(nil)
)

// NewRepo returns an empty fake repository.
func NewRepo() *Repo {
	return &Repo{
		mems: map[uuid.UUID]*memory.Memory{}, profile: map[uuid.UUID]*memory.Memory{},
		vecs: map[uuid.UUID][]float32{}, versions: map[uuid.UUID][]memory.Version{},
		jobs: map[uuid.UUID]*memory.Job{}, jobDone: map[uuid.UUID]bool{}, projScore: map[string]float64{},
	}
}

// AddProfile inserts a profile memory directly.
func (r *Repo) AddProfile(m memory.Memory, vec []float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m.Scope = memory.ScopeProfile
	r.profile[m.ID] = &m
	if vec != nil {
		r.vecs[m.ID] = vec
	}
}

// Put inserts or replaces a context memory directly, bypassing versions and audits.
func (r *Repo) Put(m memory.Memory, vec []float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m.Scope = scopeOf(m)
	r.mems[m.ID] = &m
	if vec != nil {
		r.vecs[m.ID] = vec
	}
}

// Vector returns the stored embedding for id.
func (r *Repo) Vector(id uuid.UUID) []float32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vecs[id]
}

// Events returns logged access event kinds.
func (r *Repo) Events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// PendingJobs returns ids with queued, unfinished jobs.
func (r *Repo) PendingJobs() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []uuid.UUID
	for id := range r.jobs {
		if !r.jobDone[id] {
			out = append(out, id)
		}
	}
	return out
}

// Count returns the number of context memories.
func (r *Repo) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.mems)
}

func scopeOf(m memory.Memory) memory.Scope {
	if m.Project != nil {
		return memory.ScopeProject
	}
	return memory.ScopeGlobal
}

func (r *Repo) addVersion(m memory.Memory, kind string) {
	r.versions[m.ID] = append(r.versions[m.ID], memory.Version{
		Version: m.Version, Title: m.Title, Content: m.Content, Tags: slices.Clone(m.Tags), Type: m.Type,
		ChangeKind: kind, ChangedAt: time.Now(),
	})
}

// Insert implements memory.Repository.
func (r *Repo) Insert(_ context.Context, m *memory.Memory, a memory.AuditEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	return r.insertLocked(m, a)
}

func (r *Repo) insertLocked(m *memory.Memory, a memory.AuditEntry) error {
	if m.Slug != nil {
		for _, o := range r.mems {
			if o.Slug != nil && *o.Slug == *m.Slug && o.Status != memory.StatusDeleted {
				return fmt.Errorf("slug: %w", memory.ErrConflict)
			}
		}
	}
	now := time.Now()
	m.CreatedAt, m.UpdatedAt, m.ScoreUpdatedAt = now, now, now
	m.Scope = scopeOf(*m)
	cp := *m
	r.mems[m.ID] = &cp
	r.addVersion(cp, "create")
	r.recordAudit(m.ID, a)
	return nil
}

// InsertSuperseding implements memory.Repository.
func (r *Repo) InsertSuperseding(_ context.Context, m *memory.Memory, old uuid.UUID, a memory.AuditEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	o, ok := r.mems[old]
	if !ok {
		return memory.ErrNotFound
	}
	if o.Status == memory.StatusQuarantined {
		return memory.ErrQuarantined
	}
	if err := r.insertLocked(m, a); err != nil {
		return err
	}
	o.Status = memory.StatusSuperseded
	o.Version++
	r.addVersion(*o, "supersede")
	r.links = append(r.links, memory.Link{From: m.ID, To: old, Kind: memory.LinkSupersedes})
	return nil
}

// MarkSuperseded implements memory.Repository.
func (r *Repo) MarkSuperseded(_ context.Context, old, replacement uuid.UUID, a memory.AuditEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	o, ok := r.mems[old]
	if !ok {
		return memory.ErrNotFound
	}
	if o.Status == memory.StatusQuarantined {
		return memory.ErrQuarantined
	}
	if _, ok := r.mems[replacement]; !ok {
		return memory.ErrNotFound
	}
	o.Status = memory.StatusSuperseded
	o.Version++
	r.addVersion(*o, "supersede")
	r.recordAudit(old, a)
	r.links = append(r.links, memory.Link{From: replacement, To: old, Kind: memory.LinkSupersedes})
	return nil
}

func (r *Repo) lookup(id uuid.UUID) (*memory.Memory, bool) {
	if m, ok := r.mems[id]; ok {
		return m, true
	}
	m, ok := r.profile[id]
	return m, ok
}

// GetByIDs implements memory.Repository.
func (r *Repo) GetByIDs(_ context.Context, ids []uuid.UUID) ([]memory.Memory, error) {
	out, err := r.getByIDs(ids)
	if r.OnGet != nil {
		r.OnGet()
	}
	return out, err
}

func (r *Repo) getByIDs(ids []uuid.UUID) ([]memory.Memory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return nil, r.Err
	}
	var out []memory.Memory
	for _, id := range ids {
		if m, ok := r.lookup(id); ok {
			out = append(out, *m)
		}
	}
	return out, nil
}

// GetBySlugs implements memory.Repository.
func (r *Repo) GetBySlugs(_ context.Context, owner string, slugs []string) ([]memory.Memory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return nil, r.Err
	}
	var out []memory.Memory
	for _, set := range []map[uuid.UUID]*memory.Memory{r.mems, r.profile} {
		for _, m := range set {
			if m.Owner == owner && m.Slug != nil && slices.Contains(slugs, *m.Slug) && m.Status != memory.StatusDeleted {
				out = append(out, *m)
			}
		}
	}
	return out, nil
}

// FindByHash implements memory.Repository.
func (r *Repo) FindByHash(_ context.Context, owner string, hash []byte) (*memory.Memory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return nil, r.Err
	}
	for _, m := range r.mems {
		if m.Owner == owner && string(m.ContentHash) == string(hash) &&
			m.Status != memory.StatusDeleted && m.Status != memory.StatusSuperseded {
			cp := *m
			return &cp, nil
		}
	}
	return nil, nil
}

// Update implements memory.Repository.
func (r *Repo) Update(_ context.Context, id uuid.UUID, expected *int, p memory.Patch, hash []byte, a memory.AuditEntry) (memory.Memory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return memory.Memory{}, r.Err
	}
	m, ok := r.mems[id]
	if !ok {
		return memory.Memory{}, memory.ErrNotFound
	}
	if m.Status == memory.StatusQuarantined {
		return memory.Memory{}, memory.ErrQuarantined
	}
	if expected != nil && *expected != m.Version {
		return memory.Memory{}, memory.ErrConflict
	}
	oldTitle, oldContent := m.Title, m.Content
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
	if p.Status != nil {
		m.Status = *p.Status
	}
	if hash == nil {
		hash = memory.ContentHash(m.Title, m.Content)
	}
	m.ContentHash = hash
	if m.Title != oldTitle || m.Content != oldContent {
		delete(r.vecs, id)
	}
	m.Version++
	m.UpdatedAt = time.Now()
	r.addVersion(*m, "update")
	r.recordAudit(id, a)
	return *m, nil
}

// SetStatus implements memory.Repository.
func (r *Repo) SetStatus(_ context.Context, id uuid.UUID, s memory.Status, kind string, a memory.AuditEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	m, ok := r.mems[id]
	if !ok {
		return memory.ErrNotFound
	}
	if m.Status == memory.StatusQuarantined {
		return memory.ErrQuarantined
	}
	m.Status = s
	m.Version++
	if s == memory.StatusDeleted {
		now := time.Now()
		m.DeletedAt = &now
	} else {
		m.DeletedAt = nil
	}
	r.addVersion(*m, kind)
	r.recordAudit(id, a)
	return nil
}

// Restore implements memory.Repository.
func (r *Repo) Restore(_ context.Context, id uuid.UUID, version *int, a memory.AuditEntry) (memory.Memory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return memory.Memory{}, r.Err
	}
	m, ok := r.mems[id]
	if !ok {
		return memory.Memory{}, memory.ErrNotFound
	}
	if m.Status == memory.StatusQuarantined {
		return memory.Memory{}, memory.ErrQuarantined
	}
	if version == nil && m.Status != memory.StatusDeleted {
		return memory.Memory{}, memory.ErrValidation
	}
	if version != nil {
		found := false
		for _, v := range r.versions[id] {
			if v.Version == *version {
				m.Title, m.Content, m.Tags, m.Type = v.Title, v.Content, v.Tags, v.Type
				found = true
			}
		}
		if !found {
			return memory.Memory{}, memory.ErrNotFound
		}
		delete(r.vecs, id)
	}
	if m.Status == memory.StatusDeleted {
		m.Status = memory.StatusActive
		m.DeletedAt = nil
	}
	m.ContentHash = memory.ContentHash(m.Title, m.Content)
	m.Version++
	r.addVersion(*m, "restore")
	r.recordAudit(id, a)
	return *m, nil
}

// History implements memory.Repository.
func (r *Repo) History(_ context.Context, id uuid.UUID) ([]memory.Version, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return nil, r.Err
	}
	if _, ok := r.mems[id]; !ok {
		return nil, memory.ErrNotFound
	}
	return slices.Clone(r.versions[id]), nil
}

// SetEmbedding implements memory.Repository.
func (r *Repo) SetEmbedding(_ context.Context, id uuid.UUID, version int, vec []float32, model string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	m, ok := r.mems[id]
	if !ok {
		return nil
	}
	for _, v := range r.versions[id] {
		if v.Version == version && string(memory.ContentHash(v.Title, v.Content)) == string(m.ContentHash) {
			r.vecs[id] = vec
			m.EmbeddingModel = &model
			return nil
		}
	}
	return memory.ErrConflict
}

// RecordAccess implements memory.Repository.
func (r *Repo) RecordAccess(_ context.Context, id uuid.UUID, project *string, weight float64, now time.Time, hl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	m, ok := r.mems[id]
	if !ok {
		return nil
	}
	m.AccessScore = rank.Bump(m.AccessScore, m.ScoreUpdatedAt, now, hl, weight)
	m.ScoreUpdatedAt = now
	m.AccessCount++
	m.LastAccessedAt = &now
	if project != nil {
		r.projScore[id.String()+*project] += weight
	}
	return nil
}

// AddFeedback implements memory.Repository.
func (r *Repo) AddFeedback(_ context.Context, id uuid.UUID, k memory.FeedbackKind, note string, project *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.feedback = append(r.feedback, feedbackRow{memoryID: id,
		entry: memory.FeedbackEntry{Kind: k, Note: note, Project: project, CreatedAt: time.Now()}})
	return r.Err
}

// Activity implements memory.Repository; entries are newest first.
func (r *Repo) Activity(_ context.Context, id uuid.UUID) (memory.Activity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return memory.Activity{}, r.Err
	}
	var out memory.Activity
	for _, f := range slices.Backward(r.feedback) {
		if f.memoryID == id {
			out.Feedback = append(out.Feedback, f.entry)
		}
	}
	for _, a := range slices.Backward(r.auditLog) {
		if a.memoryID == id {
			out.Audit = append(out.Audit, a.record)
		}
	}
	return out, nil
}

func (r *Repo) recordAudit(id uuid.UUID, a memory.AuditEntry) {
	r.Audits = append(r.Audits, a.Action)
	r.auditLog = append(r.auditLog, auditRow{memoryID: id, record: memory.AuditRecord{
		Action: a.Action, Process: a.Process, Cwd: a.Cwd, Details: maps.Clone(a.Details), CreatedAt: time.Now()}})
}

// AddLink implements memory.Repository.
func (r *Repo) AddLink(_ context.Context, l memory.Link) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.links, l) {
		r.links = append(r.links, l)
	}
	return r.Err
}

// RemoveLink implements memory.Repository.
func (r *Repo) RemoveLink(_ context.Context, l memory.Link) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.links = slices.DeleteFunc(r.links, func(x memory.Link) bool { return x == l })
	return r.Err
}

// LinksFor implements memory.Repository.
func (r *Repo) LinksFor(_ context.Context, ids []uuid.UUID) ([]memory.Link, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []memory.Link
	for _, l := range r.links {
		if slices.Contains(ids, l.From) || slices.Contains(ids, l.To) {
			out = append(out, l)
		}
	}
	return out, r.Err
}

// LogSearch implements memory.Repository.
func (r *Repo) LogSearch(context.Context, string, *string, []uuid.UUID) (uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "search")
	return uuid.Must(uuid.NewV7()), r.Err
}

// LogGet implements memory.Repository.
func (r *Repo) LogGet(context.Context, uuid.UUID, *uuid.UUID, *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "get")
	return r.Err
}

func visible(m *memory.Memory, f memory.Filter) bool {
	switch m.Status {
	case memory.StatusActive:
	case memory.StatusUnverified:
		if m.Scope == memory.ScopeProfile {
			return false
		}
	case memory.StatusFlagged:
		if !f.IncludeFlagged {
			return false
		}
	case memory.StatusSuperseded:
		if !f.IncludeSuperseded {
			return false
		}
	default:
		return false
	}
	if m.Owner != f.Owner || (m.ExpiresAt != nil && !m.ExpiresAt.After(f.Now)) {
		return false
	}
	if m.Scope != memory.ScopeProfile && !f.AllProjects && m.Project != nil && (f.Project == nil || *m.Project != *f.Project) {
		return false
	}
	for _, t := range f.Tags {
		if !slices.Contains(m.Tags, t) {
			return false
		}
	}
	return len(f.Types) == 0 || slices.Contains(f.Types, m.Type)
}

func (r *Repo) each(f memory.Filter, fn func(*memory.Memory)) {
	for _, set := range []map[uuid.UUID]*memory.Memory{r.mems, r.profile} {
		for _, m := range set {
			if visible(m, f) {
				fn(m)
			}
		}
	}
}

func top(cs []memory.Candidate, limit int) []memory.Candidate {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Similarity == cs[j].Similarity {
			return cs[i].ID.String() < cs[j].ID.String()
		}
		return cs[i].Similarity > cs[j].Similarity
	})
	if len(cs) > limit {
		cs = cs[:limit]
	}
	return cs
}

// Cosine returns the cosine similarity of two vectors.
func Cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// CandidatesVector implements memory.Repository.
func (r *Repo) CandidatesVector(_ context.Context, vec []float32, f memory.Filter, limit int) ([]memory.Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.VectorErr != nil {
		return nil, r.VectorErr
	}
	var cs []memory.Candidate
	r.each(f, func(m *memory.Memory) {
		if v, ok := r.vecs[m.ID]; ok {
			cs = append(cs, memory.Candidate{ID: m.ID, Similarity: Cosine(vec, v)})
		}
	})
	return top(cs, limit), r.Err
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r != '_' && r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r <= 127
	})
}

// CandidatesFTS implements memory.Repository with whole-word matching.
func (r *Repo) CandidatesFTS(_ context.Context, query string, f memory.Filter, limit int) ([]memory.Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := words(query)
	var cs []memory.Candidate
	r.each(f, func(m *memory.Memory) {
		doc := words(m.Title + " " + m.Content)
		hits := 0
		for _, w := range q {
			if slices.Contains(doc, w) {
				hits++
			}
		}
		if hits > 0 {
			cs = append(cs, memory.Candidate{ID: m.ID, Similarity: float64(hits) / float64(len(q))})
		}
	})
	return top(cs, limit), r.Err
}

// CandidatesTrigram implements memory.Repository with case-insensitive substring matching on titles.
func (r *Repo) CandidatesTrigram(_ context.Context, query string, f memory.Filter, limit int) ([]memory.Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(query))
	var cs []memory.Candidate
	r.each(f, func(m *memory.Memory) {
		if q != "" && strings.Contains(strings.ToLower(m.Title), q) {
			cs = append(cs, memory.Candidate{ID: m.ID, Similarity: 0.9})
		}
	})
	return top(cs, limit), r.Err
}

func dupCandidate(m *memory.Memory) bool {
	return m.Status != memory.StatusDeleted && m.Status != memory.StatusSuperseded
}

// NearestByVector implements memory.Repository.
func (r *Repo) NearestByVector(_ context.Context, owner string, vec []float32, limit int) ([]memory.Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.NearestOverride != nil {
		return r.NearestOverride(), r.Err
	}
	if r.VectorErr != nil {
		return nil, r.VectorErr
	}
	var cs []memory.Candidate
	for id, m := range r.mems {
		if v, ok := r.vecs[id]; ok && m.Owner == owner && dupCandidate(m) {
			cs = append(cs, memory.Candidate{ID: id, Similarity: Cosine(vec, v)})
		}
	}
	return top(cs, limit), r.Err
}

// NearestByTrigram implements memory.Repository with exact-content matching.
func (r *Repo) NearestByTrigram(_ context.Context, owner, content string, limit int) ([]memory.Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.NearestOverride != nil {
		return r.NearestOverride(), r.Err
	}
	var cs []memory.Candidate
	for id, m := range r.mems {
		if m.Owner == owner && dupCandidate(m) && strings.EqualFold(m.Content, content) {
			cs = append(cs, memory.Candidate{ID: id, Similarity: 1})
		}
	}
	return top(cs, limit), r.Err
}

// Hydrate implements memory.Repository.
func (r *Repo) Hydrate(_ context.Context, ids []uuid.UUID, project *string) ([]memory.Memory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []memory.Memory
	for _, id := range ids {
		if m, ok := r.lookup(id); ok {
			cp := *m
			if project != nil {
				if s, ok := r.projScore[id.String()+*project]; ok {
					cp.AccessScore = s
				}
			}
			out = append(out, cp)
		}
	}
	return out, r.Err
}

// Top implements memory.Repository.
func (r *Repo) Top(_ context.Context, f memory.Filter, limit int) ([]memory.Memory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var pinned, rest []memory.Memory
	r.each(f, func(m *memory.Memory) {
		switch {
		case m.Pinned:
			pinned = append(pinned, *m)
		case m.Scope != memory.ScopeProfile:
			rest = append(rest, *m)
		}
	})
	sort.SliceStable(rest, func(i, j int) bool {
		return rest[i].AccessScore > rest[j].AccessScore
	})
	out := append(pinned, rest...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, r.Err
}

// List implements memory.Repository.
func (r *Repo) List(_ context.Context, f memory.Filter, statuses []memory.Status, cursor *uuid.UUID, limit int) ([]memory.Memory, *uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []memory.Memory
	for _, m := range r.mems {
		ok := visible(m, f)
		if statuses != nil {
			cp := *m
			cp.Status = memory.StatusActive
			ok = slices.Contains(statuses, m.Status) && visible(&cp, f)
		}
		if ok && (cursor == nil || m.ID.String() > cursor.String()) {
			all = append(all, *m)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID.String() < all[j].ID.String() })
	if len(all) <= limit {
		return all, nil, r.Err
	}
	next := all[limit-1].ID
	return all[:limit], &next, r.Err
}

// Review implements memory.Repository.
func (r *Repo) Review(_ context.Context, now time.Time, _, _ time.Duration) (memory.ReviewQueue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var q memory.ReviewQueue
	for _, m := range r.mems {
		switch {
		case m.Status == memory.StatusFlagged:
			q.Flagged = append(q.Flagged, *m)
		case m.Status == memory.StatusUnverified:
			q.Unverified = append(q.Unverified, *m)
		case m.Status == memory.StatusQuarantined:
			q.QuarantinedCount++
		case m.ExpiresAt != nil && !m.ExpiresAt.After(now):
			q.Expired = append(q.Expired, *m)
		}
	}
	for _, l := range r.links {
		if l.Kind == memory.LinkContradicts {
			q.Conflicts = append(q.Conflicts, l)
		}
	}
	return q, r.Err
}

// EnqueueEmbedding implements memory.JobRepository.
func (r *Repo) EnqueueEmbedding(_ context.Context, id uuid.UUID, version int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[id] = &memory.Job{MemoryID: id, Version: version}
	r.jobDone[id] = false
	return r.Err
}

// ClaimJobs implements memory.JobRepository.
func (r *Repo) ClaimJobs(_ context.Context, n int, _ time.Time) ([]memory.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []memory.Job
	for id, j := range r.jobs {
		if r.jobDone[id] || len(out) >= n {
			continue
		}
		for _, v := range r.versions[id] {
			if v.Version == j.Version {
				j.Title, j.Content = v.Title, v.Content
			}
		}
		out = append(out, *j)
	}
	return out, r.Err
}

// CompleteJob implements memory.JobRepository.
func (r *Repo) CompleteJob(_ context.Context, id uuid.UUID, version int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok && j.Version == version {
		r.jobDone[id] = true
	}
	return r.Err
}

// FailJob implements memory.JobRepository.
func (r *Repo) FailJob(_ context.Context, id uuid.UUID, _ int, _ string, _ time.Time, giveUp bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		j.Attempts++
	}
	r.jobDone[id] = giveUp
	return r.Err
}

// Embedder is a deterministic fake: each distinct word sets one dimension, so shared words mean similar vectors.
type Embedder struct {
	Dims int
	// Down makes every call fail with memory.ErrUnavailable.
	Down bool
	// Vectors overrides the vector for exact input texts (after any query prefix is removed).
	Vectors map[string][]float32
	mu      sync.Mutex
	Calls   int
}

var _ memory.Embedder = (*Embedder)(nil)

// NewEmbedder returns a working fake embedder with 64 dimensions.
func NewEmbedder() *Embedder { return &Embedder{Dims: 64, Vectors: map[string][]float32{}} }

// Embed implements memory.Embedder.
func (e *Embedder) Embed(_ context.Context, _ memory.EmbedKind, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Calls++
	if e.Down {
		return nil, fmt.Errorf("%w: fake embedder down", memory.ErrUnavailable)
	}
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		if v, ok := e.Vectors[t]; ok {
			out = append(out, v)
			continue
		}
		v := make([]float32, e.Dims)
		for _, w := range words(t) {
			if w == "title" || w == "text" {
				continue
			}
			h := fnv.New32a()
			_, _ = h.Write([]byte(w))
			v[int(h.Sum32())%e.Dims] = 1
		}
		out = append(out, v)
	}
	return out, nil
}

// Model implements memory.Embedder.
func (e *Embedder) Model() string { return "fake" }

// Dimensions implements memory.Embedder.
func (e *Embedder) Dimensions() int { return e.Dims }

// ErrBoom is a generic injected failure.
var ErrBoom = errors.New("boom")
