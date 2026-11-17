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
	// Passive skips search logging, so the response carries no SearchID.
	Passive bool
	// Explain fills SearchResponse.Explain with the ranking breakdown of each result.
	Explain bool
	// Project overrides the session project and restricts results to it plus context-global memories.
	Project *string
}

// Explanation shows how one result was retrieved and ranked. Ranks are 1-based and 0 when absent.
type Explanation struct {
	VectorRank  int
	FTSRank     int
	TrigramRank int
	Similarity  float64
	RRF         float64
	Textual     bool
	Boost       rank.Parts
}

// SearchResponse carries ranked compact results and the id of the logged search.
type SearchResponse struct {
	SearchID uuid.UUID
	Results  []Summary
	Explain  map[uuid.UUID]Explanation
	// VectorUnavailable is set when embedding or the vector query failed and results are text-only.
	VectorUnavailable bool
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
	f.Tags, f.Types, f.AllProjects = NormalizeTags(r.Tags), r.Types, r.AllProjects
	if r.Project != nil {
		f.Project, f.AllProjects = r.Project, false
	}
	f.IncludeFlagged, f.IncludeSuperseded = r.IncludeFlagged, r.IncludeSuperseded

	// Vector search is best effort: an unavailable embedder or a vector query error (for example a
	// dimension mismatch after a model change) falls back to text retrieval.
	var vecHits []Candidate
	vectorUnavailable := true
	if vec, err := s.embedOne(ctx, EmbedQuery, r.Query); err == nil {
		if hits, err := s.repo.CandidatesVector(ctx, vec, f, candidateLimit); err == nil {
			vecHits, vectorUnavailable = hits, false
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

	results, parts, err := s.rankResults(ctx, kept, fused, f.Project)
	if err != nil {
		return SearchResponse{}, err
	}
	if len(results) > r.K {
		results = results[:r.K]
	}
	resp := SearchResponse{Results: results, VectorUnavailable: vectorUnavailable}
	if r.Explain {
		resp.Explain = explanations(results, parts, fused, vecHits, ftsHits, triHits)
	}
	if r.Passive {
		return resp, nil
	}
	searchID, err := s.repo.LogSearch(ctx, r.Query, s.settings.Scope.Project, summaryIDs(results))
	if err != nil {
		return SearchResponse{}, err //nolint:wrapcheck // repository errors are already contextual
	}
	resp.SearchID = searchID
	return resp, nil
}

func explanations(results []Summary, parts map[uuid.UUID]rank.Parts, fused map[uuid.UUID]float64,
	vecHits, ftsHits, triHits []Candidate) map[uuid.UUID]Explanation {
	vecRank, fts, tri := rankIndex(vecHits), rankIndex(ftsHits), rankIndex(triHits)
	similarity := map[uuid.UUID]float64{}
	for _, c := range vecHits {
		similarity[c.ID] = c.Similarity
	}
	out := make(map[uuid.UUID]Explanation, len(results))
	for _, r := range results {
		out[r.ID] = Explanation{
			VectorRank:  vecRank[r.ID],
			FTSRank:     fts[r.ID],
			TrigramRank: tri[r.ID],
			Similarity:  similarity[r.ID],
			RRF:         fused[r.ID],
			Textual:     fts[r.ID] > 0 || tri[r.ID] > 0,
			Boost:       parts[r.ID],
		}
	}
	return out
}

func rankIndex(cs []Candidate) map[uuid.UUID]int {
	out := make(map[uuid.UUID]int, len(cs))
	for i, c := range cs {
		out[c.ID] = i + 1
	}
	return out
}

func (s *Service) rankResults(ctx context.Context, ids []uuid.UUID, fused map[uuid.UUID]float64,
	project *string) ([]Summary, map[uuid.UUID]rank.Parts, error) {
	if len(ids) == 0 {
		return []Summary{}, nil, nil
	}
	mems, err := s.repo.Hydrate(ctx, ids, project)
	if err != nil {
		return nil, nil, err //nolint:wrapcheck // repository errors are already contextual
	}
	now, hl := s.now(), s.halfLife()
	out := make([]Summary, 0, len(mems))
	parts := make(map[uuid.UUID]rank.Parts, len(mems))
	for _, m := range mems {
		projectMatch := m.Project != nil && project != nil && *m.Project == *project
		eff := rank.Decay(m.AccessScore, m.ScoreUpdatedAt, now, hl)
		p := rank.BoostParts(fused[m.ID], projectMatch, m.Pinned, eff)
		parts[m.ID] = p
		out = append(out, summarize(m, p.Final))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ID.String() < out[j].ID.String()
		}
		return out[i].Score > out[j].Score
	})
	return out, parts, nil
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
