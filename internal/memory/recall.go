package memory

import (
	"context"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/rank"
)

// recallSearchK is how many search results recall considers before applying the relevance cutoff.
const recallSearchK = 10

// Calibration sources reported by ActiveCalibration.
const (
	CalibrationUsage   = "usage"
	CalibrationDefault = "default"
)

// slashCommand matches a prompt that starts with a Claude Code command such as "/review"; a path like
// "/etc/hosts" has a second slash and does not match.
var slashCommand = regexp.MustCompile(`^/[A-Za-z][\w:-]*(\s|$)`)

// RecallRequest is one user prompt seen by the UserPromptSubmit hook.
type RecallRequest struct {
	Prompt    string
	SessionID string
}

// RecalledMemory is a memory worth pointing Claude to, with its calibrated relevance between 0 and 1.
type RecalledMemory struct {
	Summary   Summary
	Relevance float64
}

// RecallResponse lists the memories to point to. SearchID is the logged inject event, which Claude passes
// to memory_get so the fetch counts as evidence of relevance. Both are zero when nothing is shown.
type RecallResponse struct {
	SearchID uuid.UUID
	Items    []RecalledMemory
}

// Recall finds memories relevant to a prompt. It favors precision: it shows nothing unless the vector
// search worked and a calibration exists, and only memories at or above the configured relevance.
// It never counts an access.
func (s *Service) Recall(ctx context.Context, r RecallRequest) (RecallResponse, error) {
	cfg := s.settings.Recall
	prompt := strings.TrimSpace(r.Prompt)
	if len(strings.Fields(prompt)) < max(cfg.MinWords, 1) || slashCommand.MatchString(prompt) {
		return RecallResponse{}, nil
	}
	query := truncateRunes(prompt, MaxQueryRunes)

	cal, _, ok, err := s.ActiveCalibration(ctx)
	if err != nil || !ok {
		return RecallResponse{}, err
	}
	res, err := s.Search(ctx, SearchRequest{Query: query, K: recallSearchK, Passive: true, Explain: true})
	if err != nil || res.VectorUnavailable {
		return RecallResponse{}, err
	}
	var seen []uuid.UUID
	if r.SessionID != "" {
		if seen, err = s.repo.SessionShown(ctx, r.SessionID); err != nil {
			return RecallResponse{}, err //nolint:wrapcheck // repository errors are already contextual
		}
	}

	var items []RecalledMemory
	for _, sm := range res.Results {
		if sm.Status != StatusActive || sm.Source == SourceToolOutput || slices.Contains(seen, sm.ID) {
			continue
		}
		if p := cal.Relevance(signalsOf(res.Explain[sm.ID])); p >= cfg.MinRelevance {
			items = append(items, RecalledMemory{Summary: sm, Relevance: p})
		}
	}
	if len(items) == 0 {
		return RecallResponse{}, nil
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Relevance > items[j].Relevance })
	items = items[:min(len(items), max(cfg.MaxItems, 1))]

	shown := make([]Summary, 0, len(items))
	for _, it := range items {
		shown = append(shown, it.Summary)
	}
	var stored *string
	// Scan the whole prompt: a secret cut by the truncation would no longer match its rule.
	if len(s.secrets.Scan(prompt)) == 0 {
		stored = &query
	}
	id, err := s.repo.LogEvent(ctx, Event{Kind: EventInject, Query: stored, Project: s.settings.Scope.Project,
		SessionID: r.SessionID, Returned: summaryIDs(shown), Details: s.eventDetails(OriginHook, shown, res.Explain)})
	if err != nil {
		return RecallResponse{}, err //nolint:wrapcheck // repository errors are already contextual
	}
	return RecallResponse{SearchID: id, Items: items}, nil
}

// ActiveCalibration returns the calibration in force for the configured embedding model: the newest
// usage fit, else the built-in default. ok is false when neither exists, which disables recall.
func (s *Service) ActiveCalibration(ctx context.Context) (rank.Calibration, string, bool, error) {
	model, dims := s.emb.Model(), s.emb.Dimensions()
	stored, err := s.repo.LatestCalibration(ctx, model, dims)
	if err != nil {
		return rank.Calibration{}, "", false, err //nolint:wrapcheck // repository errors are already contextual
	}
	if stored != nil {
		return stored.Coefficients, CalibrationUsage, true, nil
	}
	if c, ok := rank.DefaultCalibration(model, dims); ok {
		return c, CalibrationDefault, true, nil
	}
	return rank.Calibration{}, "", false, nil
}

// LogPreload records the memories a SessionStart hook showed, so recall does not repeat them. hookSource is
// the SessionStart source: startup, clear, and compact start a fresh context; resume does not.
func (s *Service) LogPreload(ctx context.Context, sessionID, hookSource string, ids []uuid.UUID) error {
	_, err := s.repo.LogEvent(ctx, Event{Kind: EventPreload, Project: s.settings.Scope.Project, SessionID: sessionID,
		Returned: ids, Details: EventDetails{Origin: OriginHook, HookSource: hookSource}})
	return err //nolint:wrapcheck // repository errors are already contextual
}

// eventDetails records who searched, with which model, and the signals of each shown context memory.
// Profile memories carry no signals: their fetches are never logged, so they would always look irrelevant.
func (s *Service) eventDetails(origin string, shown []Summary, explain map[uuid.UUID]Explanation) EventDetails {
	d := EventDetails{Origin: origin, Model: s.emb.Model(), Dimensions: s.emb.Dimensions()}
	for _, sm := range shown {
		if sm.Scope == ScopeProfile {
			continue
		}
		sig := signalsOf(explain[sm.ID])
		d.Signals = append(d.Signals, ResultSignals{ID: sm.ID, Similarity: sig.Similarity, FTS: sig.FTS,
			Trigram: sig.Trigram, VectorRank: sig.VectorRank})
	}
	return d
}

func signalsOf(e Explanation) rank.Signals {
	return rank.Signals{Similarity: e.Similarity, FTS: e.FTSRank > 0, Trigram: e.TrigramRank > 0, VectorRank: e.VectorRank}
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}
