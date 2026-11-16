package webui

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/adminops"
	"github.com/constantine-kostikas/memorable/internal/memory"
)

type profileForm struct {
	Title   string
	Content string
	Tags    string
	Type    string
	Types   []string
	Pinned  bool
}

type profileRow struct {
	ID        string
	Href      string
	UpdateURL string
	DeleteURL string
	Title     string
	Slug      string
	Type      string
	Status    string
	Content   string
	Form      profileForm
	Error     string
}

type profileView struct {
	Context  string
	CSRF     string
	Admin    bool
	AddURL   string
	Add      profileForm
	AddError string
	Notice   string
	Error    string
	Rows     []profileRow
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request, rc requestContext) {
	s.respondProfile(w, r, rc, http.StatusOK, nil)
}

// respondProfile renders the profile list; edit adjusts the view (notices, errors, kept forms).
// The add, edit, and delete forms appear only while admin is unlocked.
func (s *Server) respondProfile(w http.ResponseWriter, r *http.Request, rc requestContext, status int, edit func(*profileView)) {
	mems, err := rc.Backend.Store.ProfileList(r.Context())
	if err != nil {
		s.failResponse(w, r, rc, fmt.Errorf("list profile: %w", err))
		return
	}
	base := contextBase(rc.Name) + "/profile"
	view := profileView{
		Context: rc.Name, CSRF: s.session.csrf, Admin: s.admin.minutesLeft() > 0, AddURL: base,
		Add: profileForm{Type: memory.TypeUser.String(), Types: filterTypes}, Rows: make([]profileRow, 0, len(mems)),
	}
	for _, m := range mems {
		row := profileRow{
			ID: m.ID.String(), Href: memoryBase(rc.Name, m.ID), UpdateURL: base + "/" + m.ID.String() + "/update",
			DeleteURL: base + "/" + m.ID.String() + "/delete", Title: m.Title, Type: m.Type.String(), Status: m.Status.String(),
			Content: m.Content,
			Form:    profileForm{Title: m.Title, Content: m.Content, Tags: strings.Join(m.Tags, ", "), Type: m.Type.String(), Types: filterTypes, Pinned: m.Pinned},
		}
		if m.Slug != nil {
			row.Slug = *m.Slug
		}
		view.Rows = append(view.Rows, row)
	}
	if edit != nil {
		edit(&view)
	}
	w.Header().Set("Vary", "HX-Request")
	if isFragmentRequest(r) {
		s.renderStatus(w, status, "profile.html", view)
		return
	}
	page := s.contextPage(r, rc, "Profile", "")
	page.Page, page.Profile = pageProfile, &view
	s.renderStatus(w, status, "layout.html", page)
}

func profileFormFrom(r *http.Request) profileForm {
	return profileForm{
		Title: r.PostFormValue("title"), Content: r.PostFormValue("content"), Tags: r.PostFormValue("tags"),
		Type: r.PostFormValue("type"), Types: filterTypes, Pinned: r.PostFormValue("pinned") != "",
	}
}

func (f profileForm) input() (adminops.ProfileInput, error) {
	typ, err := memory.ParseType(f.Type)
	if err != nil {
		return adminops.ProfileInput{}, fmt.Errorf("%w: unknown type", memory.ErrValidation)
	}
	return adminops.ProfileInput{Title: f.Title, Content: f.Content, Tags: splitTags(f.Tags), Type: typ, Pinned: f.Pinned}, nil
}

func (s *Server) handleProfileAdd(w http.ResponseWriter, r *http.Request, rc requestContext, ops AdminOps) {
	form := profileFormFrom(r)
	in, err := form.input()
	if err == nil {
		_, err = ops.ProfileAdd(r.Context(), in)
	}
	if err != nil {
		status, message := s.actionStatus(r, fmt.Errorf("add profile memory: %w", err))
		s.respondProfile(w, r, rc, status, func(v *profileView) { v.Add, v.AddError = form, message })
		return
	}
	s.respondProfile(w, r, rc, http.StatusOK, func(v *profileView) { v.Notice = "Profile memory added." })
}

func (s *Server) handleProfileUpdate(w http.ResponseWriter, r *http.Request, rc requestContext, ops AdminOps) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.profileFailure(w, r, rc, uuid.Nil, profileForm{}, memory.ErrNotFound)
		return
	}
	form := profileFormFrom(r)
	in, err := form.input()
	if err == nil {
		_, err = ops.ProfileEdit(r.Context(), id, in)
	}
	if err != nil {
		s.profileFailure(w, r, rc, id, form, fmt.Errorf("edit profile memory: %w", err))
		return
	}
	s.respondProfile(w, r, rc, http.StatusOK, func(v *profileView) { v.Notice = "Profile memory saved." })
}

func (s *Server) handleProfileDelete(w http.ResponseWriter, r *http.Request, rc requestContext, ops AdminOps) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.profileFailure(w, r, rc, uuid.Nil, profileForm{}, memory.ErrNotFound)
		return
	}
	if err := ops.ProfileDelete(r.Context(), id); err != nil {
		s.profileFailure(w, r, rc, uuid.Nil, profileForm{}, fmt.Errorf("delete profile memory: %w", err))
		return
	}
	s.respondProfile(w, r, rc, http.StatusOK, func(v *profileView) { v.Notice = "Profile memory deleted." })
}

// profileFailure shows err next to the row it concerns, keeping the submitted form, or at the top
// of the page when no listed row matches.
func (s *Server) profileFailure(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID, form profileForm, err error) {
	status, message := s.actionStatus(r, err)
	s.respondProfile(w, r, rc, status, func(v *profileView) {
		for i := range v.Rows {
			if id != uuid.Nil && v.Rows[i].ID == id.String() {
				v.Rows[i].Form, v.Rows[i].Error = form, message
				return
			}
		}
		v.Error = message
	})
}
