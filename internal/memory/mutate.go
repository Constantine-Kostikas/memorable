package memory

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

func (s *Service) loadOwn(ctx context.Context, id uuid.UUID) (Memory, error) {
	ms, err := s.repo.GetByIDs(ctx, []uuid.UUID{id})
	if err != nil {
		return Memory{}, err //nolint:wrapcheck // repository errors are already contextual
	}
	if len(ms) == 0 {
		return Memory{}, fmt.Errorf("memory %s: %w", id, ErrNotFound)
	}
	if ms[0].Scope == ScopeProfile {
		return Memory{}, fmt.Errorf("%w: profile memories are managed with `memorable profile`", ErrReadOnly)
	}
	return ms[0], nil
}

// Update applies a patch; text changes are rescanned and re-embedded.
func (s *Service) Update(ctx context.Context, id uuid.UUID, expected *int, p Patch) (Memory, error) {
	if err := s.writable(); err != nil {
		return Memory{}, err
	}
	if err := p.Validate(); err != nil {
		return Memory{}, err
	}
	cur, err := s.loadOwn(ctx, id)
	if err != nil {
		return Memory{}, err
	}
	next := cur
	if p.Title != nil {
		next.Title = *p.Title
	}
	if p.Content != nil {
		next.Content = *p.Content
	}
	if p.Tags != nil {
		next.Tags = *p.Tags
	}
	secrets, injections := s.scanText(next.Title, next.Content, next.Tags)
	if len(secrets) > 0 {
		return Memory{}, fmt.Errorf("%w: %s", ErrSecretDetected, ruleList(secrets))
	}
	hash := ContentHash(next.Title, next.Content)
	out, err := s.repo.Update(ctx, id, expected, p, hash, s.audit("update"))
	if err != nil {
		return Memory{}, err //nolint:wrapcheck // repository errors are already contextual
	}
	if len(injections) > 0 && out.Status != StatusQuarantined {
		if err := s.repo.SetStatus(ctx, id, StatusQuarantined, "status", s.audit("quarantine")); err != nil {
			return Memory{}, err //nolint:wrapcheck // repository errors are already contextual
		}
		out.Status = StatusQuarantined
		out.Version++
	}
	if next.Title != cur.Title || next.Content != cur.Content {
		s.reembed(ctx, out)
	}
	return out, nil
}

func (s *Service) reembed(ctx context.Context, m Memory) {
	vec, err := s.embedOne(ctx, EmbedDocument, DocumentText(m.Title, m.Content))
	if err != nil {
		vec = nil
	}
	s.storeEmbedding(ctx, m, vec)
}

// Delete soft-deletes a memory.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.writable(); err != nil {
		return err
	}
	if _, err := s.loadOwn(ctx, id); err != nil {
		return err
	}
	return s.repo.SetStatus(ctx, id, StatusDeleted, "delete", s.audit("delete")) //nolint:wrapcheck // contextual
}

// Restore undeletes a memory, or restores a prior version's text.
func (s *Service) Restore(ctx context.Context, id uuid.UUID, version *int) (Memory, error) {
	if err := s.writable(); err != nil {
		return Memory{}, err
	}
	out, err := s.repo.Restore(ctx, id, version, s.audit("restore"))
	if err != nil {
		return Memory{}, err //nolint:wrapcheck // repository errors are already contextual
	}
	if version != nil {
		s.reembed(ctx, out)
	}
	return out, nil
}

// History lists versions of a memory.
func (s *Service) History(ctx context.Context, id uuid.UUID) ([]Version, error) {
	return s.repo.History(ctx, id) //nolint:wrapcheck // repository errors are already contextual
}

// Feedback records a verdict: useful counts as a weighted access; wrong and outdated flag the memory.
func (s *Service) Feedback(ctx context.Context, id uuid.UUID, k FeedbackKind, note string) error {
	if err := s.writable(); err != nil {
		return err
	}
	if k == FeedbackUnknown {
		return fmt.Errorf("%w: feedback kind is required", ErrValidation)
	}
	if _, err := s.loadOwn(ctx, id); err != nil {
		return err
	}
	if err := s.repo.AddFeedback(ctx, id, k, note, s.settings.Scope.Project); err != nil {
		return err //nolint:wrapcheck // repository errors are already contextual
	}
	if k == FeedbackUseful {
		return s.repo.RecordAccess(ctx, id, s.settings.Scope.Project, 2, s.now(), s.halfLife()) //nolint:wrapcheck // contextual
	}
	return s.repo.SetStatus(ctx, id, StatusFlagged, "status", s.audit("flag")) //nolint:wrapcheck // contextual
}

// Link adds or removes a link between two memories.
func (s *Service) Link(ctx context.Context, l Link, remove bool) error {
	if err := s.writable(); err != nil {
		return err
	}
	switch l.Kind {
	case LinkRelated, LinkSupersedes, LinkContradicts:
	default:
		return fmt.Errorf("%w: link kind %q must be related, supersedes, or contradicts", ErrValidation, l.Kind)
	}
	if l.From == l.To {
		return fmt.Errorf("%w: a memory cannot link to itself", ErrValidation)
	}
	if remove {
		return s.repo.RemoveLink(ctx, l) //nolint:wrapcheck // repository errors are already contextual
	}
	return s.repo.AddLink(ctx, l) //nolint:wrapcheck // repository errors are already contextual
}

// Activate marks a flagged or unverified memory as active after review.
func (s *Service) Activate(ctx context.Context, id uuid.UUID) error {
	if err := s.writable(); err != nil {
		return err
	}
	m, err := s.loadOwn(ctx, id)
	if err != nil {
		return err
	}
	if m.Status != StatusFlagged && m.Status != StatusUnverified {
		return fmt.Errorf("%w: only flagged or unverified memories can be activated", ErrValidation)
	}
	return s.repo.SetStatus(ctx, id, StatusActive, "status", s.audit("activate")) //nolint:wrapcheck // contextual
}
