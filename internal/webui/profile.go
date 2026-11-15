package webui

import (
	"fmt"
	"net/http"
)

type profileRow struct {
	Href    string
	Title   string
	Slug    string
	Type    string
	Status  string
	Content string
}

type profileView struct {
	Context string
	Rows    []profileRow
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request, rc requestContext) {
	mems, err := rc.Backend.Store.ProfileList(r.Context())
	if err != nil {
		s.fail(w, r, rc, fmt.Errorf("list profile: %w", err))
		return
	}
	view := profileView{Context: rc.Name, Rows: make([]profileRow, 0, len(mems))}
	for _, m := range mems {
		row := profileRow{
			Href: memoryBase(rc.Name, m.ID), Title: m.Title, Type: m.Type.String(), Status: m.Status.String(), Content: m.Content,
		}
		if m.Slug != nil {
			row.Slug = *m.Slug
		}
		view.Rows = append(view.Rows, row)
	}
	page := s.contextPage(r, rc, "Profile", "")
	page.Page, page.Profile = pageProfile, &view
	s.renderTemplate(w, "layout.html", page)
}
