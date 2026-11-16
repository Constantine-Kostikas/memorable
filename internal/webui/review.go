package webui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const (
	pageReview  = "review"
	pageProfile = "profile"
	pageStats   = "stats"
)

const (
	conflictPreviewLines = 12
	previewEllipsis      = "…"
	returnToReview       = "review"
)

type reviewSection struct {
	Heading string
	Empty   string
	Rows    []memoryRow
}

// conflictSide is one memory of a conflict. Hidden sides (quarantined) and Unavailable sides
// (failed to load) carry only their id and offer no actions.
type conflictSide struct {
	ID          string
	Title       string
	Type        string
	TypeClass   string
	Status      string
	Href        string
	Preview     string
	Hidden      bool
	Unavailable bool
	DeleteURL   string
	CSRF        string
	// KeepURL supersedes the other side with this one; empty when either side cannot be acted on.
	KeepURL string
}

type conflictView struct {
	Kind     string
	From, To conflictSide
}

type reviewView struct {
	Context          string
	CSRF             string
	Notice           string
	Error            string
	Sections         []reviewSection
	Conflicts        []conflictView
	QuarantinedCount int
	// Quarantined is the admin view of quarantined memories; nil while admin is absent or locked.
	Quarantined *quarantinedView
}

func reviewTotal(q memory.ReviewQueue) int {
	return len(q.Flagged) + len(q.Expired) + len(q.Stale) + len(q.Unverified) + len(q.Conflicts) + q.QuarantinedCount
}

// contextPage builds a page inside a context with the nav Review count filled in. A failure to
// compute the count is logged once and shows no count instead of failing the page.
func (s *Server) contextPage(r *http.Request, rc requestContext, title, message string) pageData {
	page := s.authenticatedPage(title, message)
	page.Context = rc.Name
	queue, err := rc.Backend.Memories.Review(r.Context())
	if err != nil {
		s.logger.WarnContext(r.Context(), "load review count", "context", rc.Name, "error", err)
		page.ReviewCountUnknown = true
		return page
	}
	page.ReviewCount = reviewTotal(queue)
	return page
}

// handleReview shows the review queue.
func (s *Server) handleReview(w http.ResponseWriter, r *http.Request, rc requestContext) {
	s.respondReview(w, r, rc, http.StatusOK, nil)
}

// respondReview renders the review queue, the whole page or, for htmx, the #review fragment; edit
// adjusts the view (notices, errors). With admin unlocked, quarantined memories are listed in full;
// acquiring admin access first means an expiry found here already shows as locked.
func (s *Server) respondReview(w http.ResponseWriter, r *http.Request, rc requestContext, status int, edit func(*reviewView)) {
	var quarantined *quarantinedView
	if s.admin != nil {
		ops, err := s.adminOps(r, rc)
		switch {
		case err == nil:
			view := s.loadQuarantined(r, rc, ops)
			quarantined = &view
		case errors.Is(err, errAdminUnavailable):
			quarantined = &quarantinedView{Context: rc.Name, Error: adminUnavailableMessage(rc.Name)}
		}
	}
	page, err := s.reviewPage(r, rc)
	if err != nil {
		s.failResponse(w, r, rc, err)
		return
	}
	page.Review.Quarantined = quarantined
	if edit != nil {
		edit(page.Review)
	}
	w.Header().Set("Vary", "HX-Request")
	if isFragmentRequest(r) {
		s.renderStatus(w, status, "review.html", page.Review)
		return
	}
	s.renderStatus(w, status, "layout.html", page)
}

func (s *Server) reviewPage(r *http.Request, rc requestContext) (pageData, error) {
	queue, err := rc.Backend.Memories.Review(r.Context())
	if err != nil {
		return pageData{}, fmt.Errorf("load review queue: %w", err)
	}
	now := s.cfg.Now()
	view := reviewView{
		Context: rc.Name, CSRF: s.session.csrf,
		Sections: []reviewSection{
			{Heading: "Flagged", Empty: "Nothing is flagged.", Rows: reviewRows(rc.Name, queue.Flagged, flaggedNote, now)},
			{Heading: "Expired", Empty: "Nothing has expired.", Rows: reviewRows(rc.Name, queue.Expired, expiredNote, now)},
			{Heading: "Stale", Empty: "Nothing is stale.", Rows: reviewRows(rc.Name, queue.Stale, staleNote, now)},
			{Heading: "Unverified", Empty: "Nothing is unverified.", Rows: reviewRows(rc.Name, queue.Unverified, unverifiedNote, now)},
		},
		Conflicts:        s.conflictViews(r, rc, queue.Conflicts),
		QuarantinedCount: queue.QuarantinedCount,
	}
	page := s.authenticatedPage("Review", "")
	page.Context, page.Page, page.Review = rc.Name, pageReview, &view
	page.ReviewCount = reviewTotal(queue)
	return page, nil
}

func flaggedNote(memory.Memory) string    { return "Flagged by feedback." }
func unverifiedNote(memory.Memory) string { return "Not yet verified." }

func expiredNote(m memory.Memory) string {
	if m.ExpiresAt == nil {
		return "Expired."
	}
	return "Expired " + formatTime(*m.ExpiresAt) + "."
}

func staleNote(m memory.Memory) string { return "Last updated " + formatTime(m.UpdatedAt) + "." }

// reviewRows adapts review items to list rows. Review has no detail pane, so the rows link to the
// workspace as plain links.
func reviewRows(contextName string, mems []memory.Memory, note func(memory.Memory) string, now time.Time) []memoryRow {
	rows := make([]memoryRow, 0, len(mems))
	for _, m := range mems {
		rows = append(rows, memoryRow{
			Href: memoryBase(contextName, m.ID), Title: m.Title, Project: projectOf(m),
			Type: m.Type.String(), TypeClass: typeClass(m.Type.String()), Status: m.Status.String(),
			Age: relativeAge(now, m.UpdatedAt), Note: note(m), Standalone: true,
		})
	}
	return rows
}

func projectOf(m memory.Memory) string {
	if m.Project != nil && m.Scope != memory.ScopeGlobal {
		return *m.Project
	}
	return globalProject
}

// conflictViews loads both sides of each conflict with one passive Get. A failed Get only marks
// its sides unavailable; it is logged and never fails the page.
func (s *Server) conflictViews(r *http.Request, rc requestContext, links []memory.Link) []conflictView {
	views := make([]conflictView, 0, len(links))
	for _, l := range links {
		res, err := rc.Backend.Memories.Get(r.Context(), memory.GetRequest{IDs: []uuid.UUID{l.From, l.To}, Passive: true})
		if err != nil {
			s.logger.WarnContext(r.Context(), "load conflict", "context", rc.Name, "from", l.From, "to", l.To, "error", err)
		}
		from, to := conflictSideOf(rc.Name, l.From, res), conflictSideOf(rc.Name, l.To, res)
		from.CSRF, to.CSRF = s.session.csrf, s.session.csrf
		if actionable(from) && actionable(to) {
			from.KeepURL = memoryBase(rc.Name, l.To) + "/supersede"
			to.KeepURL = memoryBase(rc.Name, l.From) + "/supersede"
		}
		views = append(views, conflictView{Kind: l.Kind, From: from, To: to})
	}
	return views
}

func actionable(side conflictSide) bool { return !side.Hidden && !side.Unavailable }

// conflictSideOf finds id among the fetched results by id, never by position. A quarantined side
// carries only its id and status, so nothing else is read from it.
func conflictSideOf(contextName string, id uuid.UUID, res []memory.Fetched) conflictSide {
	base := memoryBase(contextName, id)
	side := conflictSide{ID: id.String(), Href: base, Unavailable: true}
	for _, f := range res {
		if f.Ref != id.String() && f.Memory.ID != id {
			continue
		}
		switch {
		case errors.Is(f.Err, memory.ErrQuarantined):
			return conflictSide{ID: id.String(), Href: base, Hidden: true, Status: memory.StatusQuarantined.String()}
		case f.Err != nil || f.Memory.ID != id:
			return side
		}
		m := f.Memory
		return conflictSide{
			ID: id.String(), Href: base, Title: m.Title, Type: m.Type.String(), TypeClass: typeClass(m.Type.String()),
			Status: m.Status.String(), Preview: previewOf(m.Content), DeleteURL: base + "/delete",
		}
	}
	return side
}

// previewOf is the first conflictPreviewLines lines of content, with an ellipsis line when cut.
func previewOf(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) <= conflictPreviewLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(append(lines[:conflictPreviewLines:conflictPreviewLines], previewEllipsis), "\n")
}
