package webui

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const (
	conflictMessage    = "This memory changed since you opened it."
	quarantinedMessage = "Saved, but quarantined: it needs approval (memorable approve or ui --admin)."
	secretPrefix       = "secret detected: "
)

// actionStatus maps err to a status and a short safe message; unexpected errors are logged once and never shown.
func (s *Server) actionStatus(r *http.Request, err error) (int, string) {
	switch {
	case errors.Is(err, memory.ErrConflict):
		return http.StatusConflict, conflictMessage
	case errors.Is(err, memory.ErrSecretDetected):
		return http.StatusUnprocessableEntity, "Rejected: looks like a secret (" + secretRules(err) + ")."
	case errors.Is(err, memory.ErrValidation):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, memory.ErrNotFound):
		return http.StatusNotFound, "Memory not found."
	case errors.Is(err, memory.ErrQuarantined):
		return http.StatusUnprocessableEntity, "This memory is quarantined: it needs approval (memorable approve or ui --admin)."
	case errors.Is(err, memory.ErrReadOnly):
		return http.StatusForbidden, "This memory cannot be changed here."
	case errors.Is(err, memory.ErrRateLimited):
		return http.StatusTooManyRequests, "Write rate limit reached. Try again shortly."
	case errors.Is(err, memory.ErrUnavailable):
		return http.StatusServiceUnavailable, "Memory store unavailable. Run memorable doctor."
	}
	s.logger.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "error", err)
	return http.StatusInternalServerError, "Something went wrong. See the memorable ui log."
}

func secretRules(err error) string {
	_, rules, found := strings.Cut(err.Error(), secretPrefix)
	if !found {
		return "unknown rule"
	}
	return rules
}

type actionFunc func(r *http.Request, rc requestContext, id uuid.UUID) (notice string, err error)

// action runs a mutation and answers with the re-rendered detail, or the detail with an inline error.
func (s *Server) action(do actionFunc) func(http.ResponseWriter, *http.Request, requestContext, uuid.UUID) {
	return func(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID) {
		if err := r.ParseForm(); err != nil {
			s.failResponse(w, r, rc.Name, fmt.Errorf("%w: malformed form", memory.ErrValidation))
			return
		}
		notice, err := do(r, rc, id)
		status, message := http.StatusOK, ""
		if err != nil {
			status, message = s.actionStatus(r, err)
			notice = ""
		}
		view, loadErr := s.loadDetail(r, rc, id)
		if loadErr != nil {
			s.failResponse(w, r, rc.Name, loadErr)
			return
		}
		view.Notice, view.Error = notice, message
		s.respondDetail(w, r, rc, view, status)
	}
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID) {
	if err := r.ParseForm(); err != nil {
		s.failResponse(w, r, rc.Name, fmt.Errorf("%w: malformed form", memory.ErrValidation))
		return
	}
	edit := editView{
		Context: rc.Name, ID: id.String(), CSRF: s.session.csrf, Base: memoryBase(rc.Name, id), DetailURL: memoryBase(rc.Name, id),
		Title: r.PostFormValue("title"), Content: r.PostFormValue("content"), Tags: r.PostFormValue("tags"),
		Type: r.PostFormValue("type"), Types: filterTypes, Pinned: r.PostFormValue("pinned") != "",
		ExpiresAt: strings.TrimSpace(r.PostFormValue("expires_at")), Version: r.PostFormValue("version"),
	}
	expected, patch, err := patchFromEdit(edit)
	var updated memory.Memory
	if err == nil {
		updated, err = rc.Backend.Memories.Update(r.Context(), id, &expected, patch)
	}
	if err != nil {
		status, message := s.actionStatus(r, err)
		edit.Error, edit.Conflict = message, errors.Is(err, memory.ErrConflict)
		s.respondEdit(w, r, rc, edit, status)
		return
	}
	view, err := s.loadDetail(r, rc, id)
	if err != nil {
		s.failResponse(w, r, rc.Name, err)
		return
	}
	view.Notice = "Saved."
	if updated.Status == memory.StatusQuarantined {
		view.Notice = quarantinedMessage
	}
	s.respondDetail(w, r, rc, view, http.StatusOK)
}

func patchFromEdit(e editView) (int, memory.Patch, error) {
	expected, err := strconv.Atoi(e.Version)
	if err != nil {
		return 0, memory.Patch{}, fmt.Errorf("%w: the form is missing the version it was loaded at; reload the page", memory.ErrValidation)
	}
	typ, err := memory.ParseType(e.Type)
	if err != nil {
		return 0, memory.Patch{}, fmt.Errorf("%w: unknown type", memory.ErrValidation)
	}
	expires, err := parseExpiry(e.ExpiresAt)
	if err != nil {
		return 0, memory.Patch{}, err
	}
	tags := splitTags(e.Tags)
	return expected, memory.Patch{
		Title: &e.Title, Content: &e.Content, Tags: &tags, Type: &typ, Pinned: &e.Pinned, ExpiresAt: &expires,
	}, nil
}

func parseExpiry(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("%w: expires_at must be RFC 3339 (2026-12-31T00:00:00Z) or a date (2026-12-31)", memory.ErrValidation)
}

func splitTags(raw string) []string {
	tags := []string{}
	for _, tag := range strings.Split(raw, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func (s *Server) doFeedback(r *http.Request, rc requestContext, id uuid.UUID) (string, error) {
	kind, err := memory.ParseFeedbackKind(r.PostFormValue("kind"))
	if err != nil {
		return "", fmt.Errorf("%w: feedback kind must be useful, wrong, or outdated", memory.ErrValidation)
	}
	if err := rc.Backend.Memories.Feedback(r.Context(), id, kind, r.PostFormValue("note")); err != nil {
		return "", fmt.Errorf("feedback: %w", err)
	}
	return "Feedback recorded.", nil
}

func (s *Server) doDelete(r *http.Request, rc requestContext, id uuid.UUID) (string, error) {
	if err := rc.Backend.Memories.Delete(r.Context(), id); err != nil {
		return "", fmt.Errorf("delete: %w", err)
	}
	return "Memory deleted.", nil
}

func (s *Server) doRestore(r *http.Request, rc requestContext, id uuid.UUID) (string, error) {
	var version *int
	if raw := strings.TrimSpace(r.PostFormValue("version")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return "", fmt.Errorf("%w: version must be a number", memory.ErrValidation)
		}
		version = &v
	}
	if _, err := rc.Backend.Memories.Restore(r.Context(), id, version); err != nil {
		return "", fmt.Errorf("restore: %w", err)
	}
	if version != nil {
		return fmt.Sprintf("Restored version %d.", *version), nil
	}
	return "Memory restored.", nil
}

func (s *Server) doActivate(r *http.Request, rc requestContext, id uuid.UUID) (string, error) {
	if err := rc.Backend.Memories.Activate(r.Context(), id); err != nil {
		return "", fmt.Errorf("activate: %w", err)
	}
	return "Memory activated.", nil
}

func (s *Server) doLink(remove bool) actionFunc {
	return func(r *http.Request, rc requestContext, id uuid.UUID) (string, error) {
		to, err := s.resolveRef(r, rc, r.PostFormValue("to"))
		if err != nil {
			return "", err
		}
		link := memory.Link{From: id, To: to, Kind: r.PostFormValue("kind")}
		if err := rc.Backend.Memories.Link(r.Context(), link, remove); err != nil {
			return "", fmt.Errorf("link: %w", err)
		}
		if remove {
			return "Link removed.", nil
		}
		return "Link added.", nil
	}
}

func (s *Server) doSupersede(r *http.Request, rc requestContext, id uuid.UUID) (string, error) {
	replacement, err := s.resolveRef(r, rc, r.PostFormValue("replacement"))
	if err != nil {
		return "", err
	}
	if err := rc.Backend.Memories.Supersede(r.Context(), id, replacement); err != nil {
		return "", fmt.Errorf("supersede: %w", err)
	}
	return "Memory superseded.", nil
}

// resolveRef turns an id or slug into an id. Slugs resolve through a passive Get, which logs no access.
func (s *Server) resolveRef(r *http.Request, rc requestContext, ref string) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return uuid.Nil, fmt.Errorf("%w: an id or slug is required", memory.ErrValidation)
	}
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}
	res, err := rc.Backend.Memories.Get(r.Context(), memory.GetRequest{Slugs: []string{ref}, Passive: true})
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve slug: %w", err)
	}
	if len(res) != 1 {
		return uuid.Nil, fmt.Errorf("resolve slug: %w", memory.ErrNotFound)
	}
	if res[0].Err != nil {
		return uuid.Nil, fmt.Errorf("resolve slug: %w", res[0].Err)
	}
	return res[0].Memory.ID, nil
}

func (s *Server) registerMemoryRoutes(mux *http.ServeMux) {
	const base = "/c/{context}/memories/{id}"
	mux.HandleFunc("GET "+base, s.inContext(s.withMemoryID(s.handleDetail)))
	mux.HandleFunc("GET "+base+"/edit", s.inContext(s.withMemoryID(s.handleEdit)))
	mux.HandleFunc("POST "+base+"/update", s.inContext(s.withMemoryID(s.handleUpdate)))
	for name, do := range map[string]actionFunc{
		"feedback": s.doFeedback, "delete": s.doDelete, "restore": s.doRestore, "activate": s.doActivate,
		"link": s.doLink(false), "unlink": s.doLink(true), "supersede": s.doSupersede,
	} {
		mux.HandleFunc("POST "+base+"/"+name, s.inContext(s.withMemoryID(s.action(do))))
	}
}
