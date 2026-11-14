package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/rank"
)

const candidateLimit = 50

// SearchRequest is a hybrid search query.
type SearchRequest struct {
	Query             string
	K                 int
	Tags              []string
	Types             []Type
	AllProjects       bool
	IncludeFlagged    bool
	IncludeSuperseded bool
}

// SearchResponse carries ranked compact results and the id of the logged search.
type SearchResponse struct {
	SearchID uuid.UUID
	Results  []Summary
}

func (r *SearchRequest) normalize() error {
	r.Query = strings.TrimSpace(r.Query)
	if r.Query == "" {
		return fmt.Errorf("%w: query is required", ErrValidation)
	}
	if n := utf8.RuneCountInString(r.Query); n > MaxQueryRunes {
		return fmt.Errorf("%w: query has %d characters, limit is %d", ErrValidation, n, MaxQueryRunes)
	}
	if r.K == 0 {
		r.K = DefaultK
	}
	if r.K < 1 || r.K > MaxK {
		return fmt.Errorf("%w: k must be between 1 and %d", ErrValidation, MaxK)
	}
	return nil
}

// Search runs vector, full-text, and trigram retrieval, fuses them, applies boosts and the relevance cutoff.
// The vector list is skipped when the embedder is unavailable.
func (s *Service) Search(ctx context.Context, r SearchRequest) (SearchResponse, error) {
	if err := r.normalize(); err != nil {
		return SearchResponse{}, err
	}
	f := s.filter()
	f.Tags, f.Types, f.AllProjects = r.Tags, r.Types, r.AllProjects
	f.IncludeFlagged, f.IncludeSuperseded = r.IncludeFlagged, r.IncludeSuperseded

	var vecHits []Candidate
	if vec, err := s.embedOne(ctx, EmbedQuery, r.Query); err == nil {
		if vecHits, err = s.repo.CandidatesVector(ctx, vec, f, candidateLimit); err != nil {
			return SearchResponse{}, err //nolint:wrapcheck // repository errors are already contextual
		}
	}
	ftsHits, err := s.repo.CandidatesFTS(ctx, r.Query, f, candidateLimit)
	if err != nil {
		return SearchResponse{}, err //nolint:wrapcheck // repository errors are already contextual
	}
	triHits, err := s.repo.CandidatesTrigram(ctx, r.Query, f, candidateLimit)
	if err != nil {
		return SearchResponse{}, err //nolint:wrapcheck // repository errors are already contextual
	}

	fused := rank.RRF(candidateIDs(vecHits), candidateIDs(ftsHits), candidateIDs(triHits))
	textual := map[uuid.UUID]bool{}
	for _, c := range append(ftsHits, triHits...) {
		textual[c.ID] = true
	}
	vecSim := map[uuid.UUID]float64{}
	for _, c := range vecHits {
		vecSim[c.ID] = c.Similarity
	}
	kept := make([]uuid.UUID, 0, len(fused))
	for id := range fused {
		if textual[id] || vecSim[id] >= s.settings.Search.MinSimilarity {
			kept = append(kept, id)
		}
	}

	results, err := s.rankResults(ctx, kept, fused)
	if err != nil {
		return SearchResponse{}, err
	}
	if len(results) > r.K {
		results = results[:r.K]
	}
	searchID, err := s.repo.LogSearch(ctx, r.Query, s.settings.Scope.Project, summaryIDs(results))
	if err != nil {
		return SearchResponse{}, err //nolint:wrapcheck // repository errors are already contextual
	}
	return SearchResponse{SearchID: searchID, Results: results}, nil
}

func (s *Service) rankResults(ctx context.Context, ids []uuid.UUID, fused map[uuid.UUID]float64) ([]Summary, error) {
	if len(ids) == 0 {
		return []Summary{}, nil
	}
	mems, err := s.repo.Hydrate(ctx, ids, s.settings.Scope.Project)
	if err != nil {
		return nil, err //nolint:wrapcheck // repository errors are already contextual
	}
	now, hl, project := s.now(), s.halfLife(), s.settings.Scope.Project
	out := make([]Summary, 0, len(mems))
	for _, m := range mems {
		projectMatch := m.Project != nil && project != nil && *m.Project == *project
		eff := rank.Decay(m.AccessScore, m.ScoreUpdatedAt, now, hl)
		out = append(out, summarize(m, rank.Boost(fused[m.ID], projectMatch, m.Pinned, eff)))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ID.String() < out[j].ID.String()
		}
		return out[i].Score > out[j].Score
	})
	return out, nil
}

func candidateIDs(cs []Candidate) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func summaryIDs(ss []Summary) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}
