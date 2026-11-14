package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/scan"
)

// Outcome is the result of writing one memory.
type Outcome uint8

// Write outcomes.
const (
	OutcomeUnknown Outcome = iota
	OutcomeCreated
	OutcomeDuplicate
	OutcomeNeedsDecision
	OutcomeQuarantined
	OutcomeRejected
)

var outcomeNames = enumNames{"unknown", "created", "duplicate", "needs_decision", "quarantined", "rejected"}

func (o Outcome) String() string { return outcomeNames.name(uint8(o)) }

// MarshalText implements encoding.TextMarshaler.
func (o Outcome) MarshalText() ([]byte, error) { return []byte(o.String()), nil }

// WriteResult reports what happened to one item of a write.
type WriteResult struct {
	Outcome    Outcome
	Memory     *Memory
	Candidates []Summary
	Reason     string
}

// Write creates up to MaxBatch memories. Batch-level errors are ErrReadOnly, ErrRateLimited, and ErrValidation
// for an empty or oversized batch; everything else is reported per item.
func (s *Service) Write(ctx context.Context, items []NewMemory) ([]WriteResult, error) {
	if err := s.writable(); err != nil {
		return nil, err
	}
	if len(items) == 0 || len(items) > MaxBatch {
		return nil, fmt.Errorf("%w: a write takes 1 to %d memories, got %d", ErrValidation, MaxBatch, len(items))
	}
	if !s.limiter.AllowN(s.now(), len(items)) {
		return nil, fmt.Errorf("%w: limit is %d per minute", ErrRateLimited, s.settings.WritesPerMinute)
	}
	results := make([]WriteResult, 0, len(items))
	for _, n := range items {
		r, err := s.writeOne(ctx, n)
		if err != nil {
			return results, err
		}
		results = append(results, r)
	}
	return results, nil
}

func ruleList(fs []scan.Finding) string {
	names := make([]string, 0, len(fs))
	for _, f := range fs {
		names = append(names, f.Rule)
	}
	return strings.Join(names, ", ")
}

func (s *Service) scanText(title, content string, tags []string) (secret, injection []scan.Finding) {
	text := title + "\n" + content + "\n" + strings.Join(tags, " ")
	return s.secrets.Scan(text), s.inject(text)
}

func (s *Service) writeOne(ctx context.Context, n NewMemory) (WriteResult, error) {
	if err := n.Validate(); err != nil {
		return WriteResult{Outcome: OutcomeRejected, Reason: err.Error()}, nil //nolint:nilerr // validation failures are per-item results
	}
	secrets, injections := s.scanText(n.Title, n.Content, n.Tags)
	if len(secrets) > 0 {
		return WriteResult{Outcome: OutcomeRejected, Reason: "secret detected: " + ruleList(secrets)}, nil
	}
	m := s.buildMemory(n)
	if len(injections) > 0 {
		m.Status = StatusQuarantined
		if err := s.insert(ctx, &m, n.Supersedes); err != nil {
			return WriteResult{}, err
		}
		return WriteResult{Outcome: OutcomeQuarantined, Memory: &m,
			Reason: "quarantined pending human review: " + ruleList(injections)}, nil
	}

	existing, err := s.repo.FindByHash(ctx, s.settings.Owner, m.ContentHash)
	if err != nil {
		return WriteResult{}, err //nolint:wrapcheck // repository errors are already contextual
	}
	if existing != nil {
		return WriteResult{Outcome: OutcomeDuplicate, Memory: existing, Reason: "identical memory exists"}, nil
	}

	vec, embedErr := s.embedOne(ctx, EmbedDocument, DocumentText(m.Title, m.Content))
	res, done, err := s.checkSimilar(ctx, n, m, vec, embedErr == nil)
	if err != nil || done {
		return res, err
	}

	if err := s.insert(ctx, &m, n.Supersedes); err != nil {
		if errors.Is(err, ErrConflict) && m.Slug != nil {
			return WriteResult{Outcome: OutcomeRejected, Reason: fmt.Sprintf("slug %q is already used by another memory", *m.Slug)}, nil
		}
		return WriteResult{}, err
	}
	s.storeEmbedding(ctx, m, vec)
	return WriteResult{Outcome: OutcomeCreated, Memory: &m}, nil
}

func (s *Service) buildMemory(n NewMemory) Memory {
	m := Memory{
		ID: uuid.Must(uuid.NewV7()), Owner: s.settings.Owner, Slug: n.Slug, Type: n.Type,
		Title: n.Title, Content: n.Content, Tags: n.Tags, Source: n.Source, Status: StatusActive,
		Pinned: n.Pinned, ExpiresAt: n.ExpiresAt, Version: 1, ContentHash: ContentHash(n.Title, n.Content),
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if !n.Global {
		m.Project = s.settings.Scope.Project
	}
	if n.Source == SourceToolOutput {
		m.Status = StatusUnverified
	}
	return m
}

// checkSimilar returns done=true when the write must stop with res.
func (s *Service) checkSimilar(ctx context.Context, n NewMemory, m Memory, vec []float32, haveVec bool) (WriteResult, bool, error) {
	var near []Candidate
	var err error
	if haveVec {
		near, err = s.repo.NearestByVector(ctx, s.settings.Owner, vec, 5)
	} else {
		near, err = s.repo.NearestByTrigram(ctx, s.settings.Owner, m.Content, 5)
	}
	if err != nil {
		return WriteResult{}, true, err //nolint:wrapcheck // repository errors are already contextual
	}
	var conflicts []Candidate
	for _, c := range near {
		if n.Supersedes != nil && c.ID == *n.Supersedes {
			continue
		}
		switch {
		case c.Similarity >= s.settings.Search.DuplicateThreshold:
			got, err := s.repo.GetByIDs(ctx, []uuid.UUID{c.ID})
			if err != nil {
				return WriteResult{}, true, err //nolint:wrapcheck // repository errors are already contextual
			}
			if len(got) == 1 {
				return WriteResult{Outcome: OutcomeDuplicate, Memory: &got[0],
					Reason: fmt.Sprintf("near-identical memory exists (similarity %.2f)", c.Similarity)}, true, nil
			}
		case c.Similarity >= s.settings.Search.ConflictThreshold:
			conflicts = append(conflicts, c)
		}
	}
	if len(conflicts) == 0 || n.OnSimilar == OnSimilarKeepBoth {
		return WriteResult{}, false, nil
	}
	ids := make([]uuid.UUID, 0, len(conflicts))
	score := map[uuid.UUID]float64{}
	for _, c := range conflicts {
		ids = append(ids, c.ID)
		score[c.ID] = c.Similarity
	}
	mems, err := s.repo.GetByIDs(ctx, ids)
	if err != nil {
		return WriteResult{}, true, err //nolint:wrapcheck // repository errors are already contextual
	}
	cands := make([]Summary, 0, len(mems))
	for _, cm := range mems {
		cands = append(cands, summarize(cm, score[cm.ID]))
	}
	return WriteResult{Outcome: OutcomeNeedsDecision, Candidates: cands,
		Reason: "similar memories exist: update or supersede one of them, or retry with on_similar=keep_both"}, true, nil
}

func (s *Service) insert(ctx context.Context, m *Memory, supersedes *uuid.UUID) error {
	if supersedes != nil {
		return s.repo.InsertSuperseding(ctx, m, *supersedes, s.audit("supersede")) //nolint:wrapcheck // contextual
	}
	return s.repo.Insert(ctx, m, s.audit("create")) //nolint:wrapcheck // repository errors are already contextual
}

// storeEmbedding saves vec for m's current version, or queues a job when there is none or saving fails.
func (s *Service) storeEmbedding(ctx context.Context, m Memory, vec []float32) {
	if vec != nil && s.repo.SetEmbedding(ctx, m.ID, m.Version, vec, s.emb.Model()) == nil {
		return
	}
	// A failed enqueue leaves the memory searchable by text; reembed --pending or a later update repairs it.
	_ = s.jobs.EnqueueEmbedding(ctx, m.ID, m.Version)
}
