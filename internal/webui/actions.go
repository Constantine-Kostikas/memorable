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
		return http.StatusUnprocessableEntity, "This memory cannot be changed here."
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
// A form posted from the Review page (return=review) is answered with the Review page instead.
func (s *Server) action(do actionFunc) func(http.ResponseWriter, *http.Request, requestContext, uuid.UUID) {
	return func(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID) {
		if err := r.ParseForm(); err != nil {
			s.failResponse(w, r, rc, fmt.Errorf("%w: malformed form", memory.ErrValidation))
			return
		}
		notice, err := do(r, rc, id)
		status, message := http.StatusOK, ""
		if err != nil {
			status, message = s.actionStatus(r, err)
			notice = ""
		}
		if r.PostFormValue("return") == returnToReview {
			s.respondReview(w, r, rc, status, func(v *reviewView) { v.Notice, v.Error = notice, message })
			return
		}
		view, loadErr := s.loadDetail(r, rc, id)
		if loadErr != nil {
			s.failResponse(w, r, rc, loadErr)
			return
		}
		view.Notice, view.Error = notice, message
		if err == nil {
			view.refreshRow()
		}
		s.respondDetail(w, r, rc, view, status)
	}
}

// handleDelete deletes from the workspace pane and moves the pane on to the memory the client names in
// next, its list neighbor, or to the empty pane when there is none. The deleted row leaves the list out of
// band. Review deletions and failures answer as every other action does.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID) {
	if err := r.ParseForm(); err != nil {
		s.failResponse(w, r, rc, fmt.Errorf("%w: malformed form", memory.ErrValidation))
		return
	}
	if r.PostFormValue("return") == returnToReview {
		s.action(s.doDelete)(w, r, rc, id)
		return
	}
	notice, err := s.doDelete(r, rc, id)
	if err != nil {
		s.respondActionError(w, r, rc, id, err)
		return
	}
	filter, _, filterErr := parseMemoryFilter(paneFilterQuery(r, rc.Name))
	if filterErr != nil {
		filter = memoryFilter{}
	}
	listURL := memoriesURL(rc.Name, filter, nil)
	next, nextErr := uuid.Parse(r.PostFormValue("next"))
	hasNext := nextErr == nil && next != id
	if !isFragmentRequest(r) {
		target := listURL
		if hasNext {
			target = withQuery(memoryBase(rc.Name, next), filterValues(filter))
		}
		http.Redirect(w, r, target, http.StatusSeeOther) //nolint:gosec // G710: a same-origin path built from a configured context name, a parsed UUID, and re-encoded filter values.
		return
	}
	removal := deletedRow{ID: id.String(), Notice: notice}
	if hasNext {
		view, loadErr := s.loadDetail(r, rc, next)
		if loadErr == nil {
			view.ListURL, view.FilterQuery = listURL, withQuery("", filterValues(filter))
			removal.Detail = &view
			w.Header().Set("HX-Push-Url", view.Base+view.FilterQuery)
		} else {
			s.logger.WarnContext(r.Context(), "load next memory after delete", "context", rc.Name, "next", next, "error", loadErr)
		}
	}
	if removal.Detail == nil {
		w.Header().Set("HX-Push-Url", listURL)
	}
	w.Header().Set("Vary", "HX-Request, HX-Current-URL")
	s.renderStatus(w, http.StatusOK, "delete_response", removal)
}

// deletedRow answers a pane deletion: the next memory's pane, or the empty pane when Detail is nil.
type deletedRow struct {
	ID     string
	Notice string
	Error  string
	Detail *detailView
}

// respondActionError re-renders the memory's pane around a failed action, the error shown as a toast.
func (s *Server) respondActionError(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID, err error) {
	status, message := s.actionStatus(r, err)
	view, loadErr := s.loadDetail(r, rc, id)
	if loadErr != nil {
		s.failResponse(w, r, rc, loadErr)
		return
	}
	view.Error = message
	s.respondDetail(w, r, rc, view, status)
}

// handleUpdate saves the editor. On any error the pane re-renders around the submitted values, including
// the submitted version, so a stale form conflicts again instead of overwriting a newer save.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID) {
	if err := r.ParseForm(); err != nil {
		s.failResponse(w, r, rc, fmt.Errorf("%w: malformed form", memory.ErrValidation))
		return
	}
	rawVersion := r.PostFormValue("version")
	submitted := editFields{
		Title: r.PostFormValue("title"), Content: r.PostFormValue("content"), Tags: r.PostFormValue("tags"),
		Type: r.PostFormValue("type"), Pinned: r.PostFormValue("pinned") != "",
		ExpiresAt:         strings.TrimSpace(r.PostFormValue("expires_at")),
		ExpiresAtOriginal: strings.TrimSpace(r.PostFormValue("expires_at_original")),
	}
	expected, patch, err := patchFromEdit(submitted, rawVersion)
	var updated memory.Memory
	if err == nil {
		updated, err = rc.Backend.Memories.Update(r.Context(), id, &expected, patch)
	}
	view, loadErr := s.loadDetail(r, rc, id)
	if err != nil {
		if loadErr != nil {
			// The reload failed too (typically the store is down): keep the typed text in a bare editor.
			view.Editable, view.Title, view.Type = true, submitted.Title, submitted.Type
		}
		status, message := s.actionStatus(r, err)
		submitted.Version, _ = strconv.Atoi(rawVersion)
		view.Form, view.FormError, view.Conflict = submitted, message, errors.Is(err, memory.ErrConflict)
		view.Unsaved = true
		s.respondDetail(w, r, rc, view, status)
		return
	}
	if loadErr != nil {
		s.failResponse(w, r, rc, loadErr)
		return
	}
	view.Notice = "Saved."
	view.refreshRow()
	if updated.Status == memory.StatusQuarantined {
		view.Notice = quarantinedMessage
	}
	s.respondDetail(w, r, rc, view, http.StatusOK)
}

func patchFromEdit(e editFields, rawVersion string) (int, memory.Patch, error) {
	expected, err := strconv.Atoi(rawVersion)
	if err != nil {
		return 0, memory.Patch{}, fmt.Errorf("%w: the form is missing the version it was loaded at; reload the page", memory.ErrValidation)
	}
	typ, err := memory.ParseType(e.Type)
	if err != nil {
		return 0, memory.Patch{}, fmt.Errorf("%w: unknown type", memory.ErrValidation)
	}
	tags := splitTags(e.Tags)
	patch := memory.Patch{Title: &e.Title, Content: &e.Content, Tags: &tags, Type: &typ, Pinned: &e.Pinned}
	if expiryUntouched(e) {
		return expected, patch, nil
	}
	expires, err := parseExpiry(e.ExpiresAt)
	if err != nil {
		return 0, memory.Patch{}, err
	}
	patch.ExpiresAt = &expires
	return expected, patch, nil
}

// expiryUntouched reports whether the date input still shows the stored expiry's date, so saving
// leaves the stored timestamp, time of day included, instead of moving it to midnight.
func expiryUntouched(e editFields) bool {
	if e.ExpiresAt == "" {
		return false
	}
	original, err := time.Parse(time.RFC3339, e.ExpiresAtOriginal)
	return err == nil && original.UTC().Format(time.DateOnly) == e.ExpiresAt
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

// resolveRef turns an id or slug into the id of an existing, linkable memory. Both forms resolve
// through a passive Get, which logs no access and turns a missing target into a 404 instead of a store error.
func (s *Server) resolveRef(r *http.Request, rc requestContext, ref string) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return uuid.Nil, fmt.Errorf("%w: an id or slug is required", memory.ErrValidation)
	}
	req := memory.GetRequest{Slugs: []string{ref}, Passive: true}
	if id, err := uuid.Parse(ref); err == nil {
		req = memory.GetRequest{IDs: []uuid.UUID{id}, Passive: true}
	}
	res, err := rc.Backend.Memories.Get(r.Context(), req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve reference: %w", err)
	}
	if len(res) != 1 {
		return uuid.Nil, fmt.Errorf("resolve reference: %w", memory.ErrNotFound)
	}
	if res[0].Err != nil {
		return uuid.Nil, fmt.Errorf("resolve reference: %w", res[0].Err)
	}
	if res[0].Memory.Scope == memory.ScopeProfile {
		return uuid.Nil, fmt.Errorf("%w: profile memories cannot be linked or superseded here", memory.ErrValidation)
	}
	return res[0].Memory.ID, nil
}

func (s *Server) registerMemoryRoutes(mux *http.ServeMux) {
	const base = "/c/{context}/memories/{id}"
	mux.HandleFunc("GET "+base, s.inContext(s.withMemoryID(s.handleDetail)))
	mux.HandleFunc("GET "+base+"/edit", s.inContext(s.withMemoryID(s.handleEdit)))
	mux.HandleFunc("POST "+base+"/update", s.inContext(s.withMemoryID(s.handleUpdate)))
	mux.HandleFunc("POST "+base+"/delete", s.inContext(s.withMemoryID(s.handleDelete)))
	mux.HandleFunc("POST "+base+"/preview", s.inContext(s.withMemoryID(s.handlePreview)))
	mux.HandleFunc("GET "+base+"/compare", s.inContext(s.withMemoryID(s.handleCompare)))
	for name, do := range map[string]actionFunc{
		"feedback": s.doFeedback, "restore": s.doRestore, "activate": s.doActivate,
		"link": s.doLink(false), "unlink": s.doLink(true), "supersede": s.doSupersede,
	} {
		mux.HandleFunc("POST "+base+"/"+name, s.inContext(s.withMemoryID(s.action(do))))
	}
}
