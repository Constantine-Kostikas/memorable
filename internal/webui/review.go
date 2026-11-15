package webui

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const (
	pageReview  = "review"
	pageProfile = "profile"
	pageStats   = "stats"
)

type reviewItem struct {
	Href    string
	Title   string
	Project string
	Type    string
	Status  string
	Note    string
}

type reviewSection struct {
	Heading string
	Empty   string
	Items   []reviewItem
}

type conflictItem struct {
	Kind     string
	FromHref string
	FromID   string
	ToHref   string
	ToID     string
}

type reviewView struct {
	Context          string
	Sections         []reviewSection
	Conflicts        []conflictItem
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

// handleReview shows the review queue. With admin unlocked, quarantined memories are listed in
// full; acquiring admin access first means an expiry found here already shows as locked.
func (s *Server) handleReview(w http.ResponseWriter, r *http.Request, rc requestContext) {
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
		s.fail(w, r, rc, err)
		return
	}
	page.Review.Quarantined = quarantined
	s.renderTemplate(w, "layout.html", page)
}

func (s *Server) reviewPage(r *http.Request, rc requestContext) (pageData, error) {
	queue, err := rc.Backend.Memories.Review(r.Context())
	if err != nil {
		return pageData{}, fmt.Errorf("load review queue: %w", err)
	}
	view := reviewView{
		Context: rc.Name,
		Sections: []reviewSection{
			{Heading: "Flagged", Empty: "Nothing is flagged.", Items: reviewItems(rc.Name, queue.Flagged, flaggedNote)},
			{Heading: "Expired", Empty: "Nothing has expired.", Items: reviewItems(rc.Name, queue.Expired, expiredNote)},
			{Heading: "Stale", Empty: "Nothing is stale.", Items: reviewItems(rc.Name, queue.Stale, staleNote)},
			{Heading: "Unverified", Empty: "Nothing is unverified.", Items: reviewItems(rc.Name, queue.Unverified, unverifiedNote)},
		},
		Conflicts:        conflictItems(rc.Name, queue.Conflicts),
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

func reviewItems(contextName string, mems []memory.Memory, note func(memory.Memory) string) []reviewItem {
	items := make([]reviewItem, 0, len(mems))
	for _, m := range mems {
		items = append(items, reviewItem{
			Href: memoryBase(contextName, m.ID), Title: m.Title, Project: projectOf(m),
			Type: m.Type.String(), Status: m.Status.String(), Note: note(m),
		})
	}
	return items
}

func projectOf(m memory.Memory) string {
	if m.Project != nil && m.Scope != memory.ScopeGlobal {
		return *m.Project
	}
	return globalProject
}

func conflictItems(contextName string, links []memory.Link) []conflictItem {
	items := make([]conflictItem, 0, len(links))
	for _, l := range links {
		items = append(items, conflictItem{
			Kind: l.Kind, FromHref: memoryBase(contextName, l.From), FromID: shortID(l.From),
			ToHref: memoryBase(contextName, l.To), ToID: shortID(l.To),
		})
	}
	return items
}

func shortID(id uuid.UUID) string { return id.String()[:8] }
