package memory

import (
	"context"
	"maps"
	"time"

	"golang.org/x/time/rate"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/scan"
	"github.com/constantine-kostikas/memorable/internal/scope"
)

const embedTimeout = 2 * time.Second

// SecretScanner finds secrets in text.
type SecretScanner interface {
	Scan(text string) []scan.Finding
}

// InjectionScanner finds prompt-injection patterns in text.
type InjectionScanner func(text string) []scan.Finding

// Settings binds a Service to a session.
type Settings struct {
	Owner           string
	Scope           scope.Resolution
	Search          config.Search
	Preload         config.Preload
	WritesPerMinute int
	Process         string
	Cwd             string
	AuditDetails    map[string]any
}

// Service implements memorable's business rules.
type Service struct {
	repo      Repository
	jobs      JobRepository
	emb       Embedder
	secrets   SecretScanner
	inject    InjectionScanner
	settings  Settings
	now       func() time.Time
	limiter   *rate.Limiter
	unlimited bool
}

// Option customizes a Service.
type Option func(*Service)

// WithClock overrides the time source.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithoutRateLimit disables the per-minute write limit; for human-driven bulk imports only.
func WithoutRateLimit() Option {
	return func(s *Service) { s.unlimited = true }
}

// NewService wires a Service.
func NewService(repo Repository, jobs JobRepository, emb Embedder, secrets SecretScanner, inject InjectionScanner, s Settings, opts ...Option) *Service {
	svc := &Service{repo: repo, jobs: jobs, emb: emb, secrets: secrets, inject: inject, settings: s, now: time.Now}
	for _, o := range opts {
		o(svc)
	}
	perMinute := max(s.WritesPerMinute, 1)
	svc.limiter = rate.NewLimiter(rate.Limit(float64(perMinute)/60.0), perMinute)
	if svc.unlimited {
		svc.limiter = rate.NewLimiter(rate.Inf, 0)
	}
	return svc
}

// Settings returns the session settings.
func (s *Service) Settings() Settings { return s.settings }

func (s *Service) halfLife() time.Duration {
	return time.Duration(s.settings.Search.HalfLifeDays) * 24 * time.Hour
}

func (s *Service) filter() Filter {
	return Filter{Owner: s.settings.Owner, Project: s.settings.Scope.Project, Now: s.now()}
}

func (s *Service) audit(action string) AuditEntry {
	return AuditEntry{Action: action, Cwd: s.settings.Cwd, Process: s.settings.Process, Details: maps.Clone(s.settings.AuditDetails)}
}

func (s *Service) writable() error {
	if s.settings.Scope.ReadOnly {
		return ErrReadOnly
	}
	return nil
}

func (s *Service) embedOne(ctx context.Context, kind EmbedKind, text string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	vecs, err := s.emb.Embed(ctx, kind, []string{text})
	if err != nil {
		return nil, err //nolint:wrapcheck // embedder errors already carry ErrUnavailable context
	}
	return vecs[0], nil
}

func summarize(m Memory, score float64) Summary {
	return Summary{
		ID: m.ID, Slug: m.Slug, Project: m.Project, Title: m.Title, Snippet: Snippet(m.Content, SnippetRunes),
		Type: m.Type, Tags: m.Tags, Scope: m.Scope, Status: m.Status, Score: score,
	}
}
