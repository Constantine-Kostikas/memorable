package memory

import (
	"context"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/rank"
)

const staleAfter = 60 * 24 * time.Hour

// GetRequest fetches memories by id or slug.
type GetRequest struct {
	IDs      []uuid.UUID
	Slugs    []string
	SearchID *uuid.UUID
}

// Fetched is one requested memory. Ref is the id or slug that was asked for; Err is set when it
// could not be returned (not found, or quarantined, in which case Memory carries only ID and Status).
type Fetched struct {
	Ref    string
	Memory Memory
	Links  []Link
	Err    error
}

// Get returns full memories and counts each returned context memory as an access.
func (s *Service) Get(ctx context.Context, r GetRequest) ([]Fetched, error) {
	if n := len(r.IDs) + len(r.Slugs); n == 0 || n > MaxBatch {
		return nil, fmt.Errorf("%w: get takes 1 to %d ids or slugs, got %d", ErrValidation, MaxBatch, n)
	}
	var found []Memory
	if len(r.IDs) > 0 {
		ms, err := s.repo.GetByIDs(ctx, r.IDs)
		if err != nil {
			return nil, err //nolint:wrapcheck // repository errors are already contextual
		}
		found = append(found, ms...)
	}
	if len(r.Slugs) > 0 {
		ms, err := s.repo.GetBySlugs(ctx, s.settings.Owner, r.Slugs)
		if err != nil {
			return nil, err //nolint:wrapcheck // repository errors are already contextual
		}
		found = append(found, ms...)
	}
	byID := map[uuid.UUID]Memory{}
	bySlug := map[string]Memory{}
	for _, m := range found {
		byID[m.ID] = m
		if m.Slug != nil {
			bySlug[*m.Slug] = m
		}
	}

	out := make([]Fetched, 0, len(r.IDs)+len(r.Slugs))
	for _, id := range r.IDs {
		out = append(out, s.fetched(id.String(), byID, id))
	}
	for _, slug := range r.Slugs {
		m, ok := bySlug[slug]
		if !ok {
			out = append(out, Fetched{Ref: slug, Err: fmt.Errorf("slug %q: %w", slug, ErrNotFound)})
			continue
		}
		out = append(out, s.fetched(slug, byID, m.ID))
	}

	returned := make([]uuid.UUID, 0, len(out))
	for _, f := range out {
		if f.Err == nil {
			returned = append(returned, f.Memory.ID)
		}
	}
	if len(returned) == 0 {
		return out, nil
	}
	links, err := s.repo.LinksFor(ctx, returned)
	if err != nil {
		return nil, err //nolint:wrapcheck // repository errors are already contextual
	}
	now := s.now()
	for i := range out {
		if out[i].Err != nil {
			continue
		}
		m := out[i].Memory
		for _, l := range links {
			if l.From == m.ID || l.To == m.ID {
				out[i].Links = append(out[i].Links, l)
			}
		}
		if m.Scope == ScopeProfile {
			continue
		}
		if err := s.repo.RecordAccess(ctx, m.ID, s.settings.Scope.Project, 1, now, s.halfLife()); err != nil {
			return nil, err //nolint:wrapcheck // repository errors are already contextual
		}
		if err := s.repo.LogGet(ctx, m.ID, r.SearchID, s.settings.Scope.Project); err != nil {
			return nil, err //nolint:wrapcheck // repository errors are already contextual
		}
	}
	return out, nil
}

func (s *Service) fetched(ref string, byID map[uuid.UUID]Memory, id uuid.UUID) Fetched {
	m, ok := byID[id]
	if !ok {
		return Fetched{Ref: ref, Memory: Memory{ID: id}, Err: fmt.Errorf("memory %s: %w", id, ErrNotFound)}
	}
	if m.Status == StatusQuarantined {
		return Fetched{Ref: ref, Memory: Memory{ID: id, Status: m.Status, Scope: m.Scope},
			Err: fmt.Errorf("memory %s: %w", id, ErrQuarantined)}
	}
	return Fetched{Ref: ref, Memory: m}
}

// Top returns pinned memories, then the highest decayed access scores, for the current scope.
func (s *Service) Top(ctx context.Context, limit int) ([]Summary, error) {
	if limit < 1 || limit > MaxK {
		return nil, fmt.Errorf("%w: limit must be between 1 and %d", ErrValidation, MaxK)
	}
	mems, err := s.repo.Top(ctx, s.filter(), limit)
	if err != nil {
		return nil, err //nolint:wrapcheck // repository errors are already contextual
	}
	return s.scored(mems), nil
}

func (s *Service) scored(mems []Memory) []Summary {
	now, hl := s.now(), s.halfLife()
	out := make([]Summary, 0, len(mems))
	for _, m := range mems {
		out = append(out, summarize(m, rank.Decay(m.AccessScore, m.ScoreUpdatedAt, now, hl)))
	}
	return out
}

// ListRequest browses memories.
type ListRequest struct {
	Tags        []string
	Types       []Type
	Statuses    []Status
	AllProjects bool
	Cursor      *uuid.UUID
	Limit       int
}

// ListResponse is one page of memories.
type ListResponse struct {
	Items []Summary
	Next  *uuid.UUID
}

// List pages through memories; quarantined memories are listed by title only through the CLI review.
func (s *Service) List(ctx context.Context, r ListRequest) (ListResponse, error) {
	if r.Limit == 0 {
		r.Limit = MaxK
	}
	if r.Limit < 1 || r.Limit > 100 {
		return ListResponse{}, fmt.Errorf("%w: limit must be between 1 and 100", ErrValidation)
	}
	statuses := make([]Status, 0, len(r.Statuses))
	for _, st := range r.Statuses {
		if st != StatusQuarantined {
			statuses = append(statuses, st)
		}
	}
	if r.Statuses == nil {
		statuses = nil
	}
	f := s.filter()
	f.Tags, f.Types, f.AllProjects = r.Tags, r.Types, r.AllProjects
	mems, next, err := s.repo.List(ctx, f, statuses, r.Cursor, r.Limit)
	if err != nil {
		return ListResponse{}, err //nolint:wrapcheck // repository errors are already contextual
	}
	return ListResponse{Items: s.scored(mems), Next: next}, nil
}

// Review returns the review queue.
func (s *Service) Review(ctx context.Context) (ReviewQueue, error) {
	return s.repo.Review(ctx, s.now(), staleAfter, s.halfLife()) //nolint:wrapcheck // repository errors are already contextual
}

// Preload selects the session-start set: pinned first, then by decayed score, active and not tool output,
// within the item and character budget. Profile memories are included.
func (s *Service) Preload(ctx context.Context) ([]Summary, error) {
	mems, err := s.repo.Top(ctx, s.filter(), s.settings.Preload.MaxItems*3)
	if err != nil {
		return nil, err //nolint:wrapcheck // repository errors are already contextual
	}
	sort.SliceStable(mems, func(i, j int) bool { return mems[i].Pinned && !mems[j].Pinned })
	now, hl := s.now(), s.halfLife()
	out := make([]Summary, 0, s.settings.Preload.MaxItems)
	chars := 0
	for _, m := range mems {
		if m.Status != StatusActive || m.Source == SourceToolOutput {
			continue
		}
		sm := summarize(m, rank.Decay(m.AccessScore, m.ScoreUpdatedAt, now, hl))
		size := utf8.RuneCountInString(sm.Title) + utf8.RuneCountInString(sm.Snippet)
		if chars+size > s.settings.Preload.MaxChars {
			continue
		}
		chars += size
		out = append(out, sm)
		if len(out) == s.settings.Preload.MaxItems {
			break
		}
	}
	return out, nil
}
