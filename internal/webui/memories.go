package webui

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const (
	memoriesPageSize = 50
	searchResultsK   = 20
	globalProject    = "(global)"
	noRank           = "-"
)

var (
	filterTypes    = []string{"user", "feedback", "project", "reference"}
	filterStatuses = []string{"active", "unverified", "flagged", "superseded", "deleted"}
)

type memoryFilter struct {
	Query   string
	Project string
	Type    string
	Status  string
	Tags    []string
}

type explainView struct {
	VectorRank, FTSRank, TrigramRank string
	Similarity, RRF                  string
	Textual                          bool
	ProjectBoost, PinnedBoost        string
	UsageBoost, Multiplier, Final    string
}

type memoryRow struct {
	Href    string
	Title   string
	Snippet string
	Project string
	Type    string
	Status  string
	Score   string
	Tags    []string
	Explain *explainView
}

type memoriesView struct {
	Context           string
	Filter            memoryFilter
	Projects          []string
	Types             []string
	Statuses          []string
	Rows              []memoryRow
	Searching         bool
	VectorUnavailable bool
	NextURL           string
}

func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request, rc requestContext) {
	filter, cursor, err := parseMemoryFilter(r.URL.Query())
	if err != nil {
		s.fail(w, r, rc.Name, err)
		return
	}
	view := memoriesView{Context: rc.Name, Filter: filter, Searching: filter.Query != ""}
	if view.Searching {
		err = s.searchRows(r, rc, &view)
	} else {
		err = s.listRows(r, rc, &view, cursor)
	}
	if err != nil {
		s.fail(w, r, rc.Name, err)
		return
	}

	w.Header().Set("Vary", "HX-Request")
	if r.Header.Get("HX-Request") == "true" {
		s.renderTemplate(w, "memory_rows.html", view)
		return
	}
	view.Types, view.Statuses = filterTypes, filterStatuses
	view.Projects = s.projectOptions(r, rc, filter.Project)
	page := s.authenticatedPage("Memories", "")
	page.Context, page.Page, page.Memories = rc.Name, pageMemories, &view
	s.renderTemplate(w, "layout.html", page)
}

func (s *Server) listRows(r *http.Request, rc requestContext, view *memoriesView, cursor *uuid.UUID) error {
	f := view.Filter
	req := memory.ListRequest{Tags: f.Tags, Cursor: cursor, Limit: memoriesPageSize, AllProjects: f.Project == ""}
	if f.Project != "" {
		req.Project = &f.Project
	}
	if f.Type != "" {
		t, _ := memory.ParseType(f.Type)
		req.Types = []memory.Type{t}
	}
	status := cmpOr(f.Status, "active")
	st, _ := memory.ParseStatus(status)
	req.Statuses = []memory.Status{st}

	res, err := rc.Backend.Memories.List(r.Context(), req)
	if err != nil {
		return fmt.Errorf("list memories: %w", err)
	}
	view.Rows = rowsOf(rc.Name, res.Items, nil)
	if res.Next != nil {
		view.NextURL = memoriesURL(rc.Name, f, res.Next)
	}
	return nil
}

func (s *Server) searchRows(r *http.Request, rc requestContext, view *memoriesView) error {
	f := view.Filter
	req := memory.SearchRequest{
		Query: f.Query, K: searchResultsK, Tags: f.Tags, Passive: true, Explain: true,
		AllProjects: f.Project == "", IncludeFlagged: f.Status == "flagged", IncludeSuperseded: f.Status == "superseded",
	}
	if f.Project != "" {
		req.Project = &f.Project
	}
	if f.Type != "" {
		t, _ := memory.ParseType(f.Type)
		req.Types = []memory.Type{t}
	}
	res, err := rc.Backend.Memories.Search(r.Context(), req)
	if err != nil {
		return fmt.Errorf("search memories: %w", err)
	}
	items := res.Results
	if f.Status != "" {
		items = slices.DeleteFunc(slices.Clone(items), func(m memory.Summary) bool { return m.Status.String() != f.Status })
	}
	view.Rows = rowsOf(rc.Name, items, res.Explain)
	view.VectorUnavailable = res.VectorUnavailable
	return nil
}

// projectOptions lists the context's projects for the filter; a stats failure only drops the choices.
func (s *Server) projectOptions(r *http.Request, rc requestContext, selected string) []string {
	var projects []string
	stats, err := rc.Backend.Store.Stats(r.Context())
	if err != nil {
		s.logger.WarnContext(r.Context(), "load project filter options", "context", rc.Name, "error", err)
	}
	for name := range stats.ByProject {
		if name != globalProject {
			projects = append(projects, name)
		}
	}
	if selected != "" && !slices.Contains(projects, selected) {
		projects = append(projects, selected)
	}
	sort.Strings(projects)
	return projects
}

func parseMemoryFilter(q url.Values) (memoryFilter, *uuid.UUID, error) {
	f := memoryFilter{
		Query:   strings.TrimSpace(q.Get("q")),
		Project: strings.TrimSpace(q.Get("project")),
		Type:    q.Get("type"),
		Status:  q.Get("status"),
	}
	for _, tag := range q["tag"] {
		if tag = strings.TrimSpace(tag); tag != "" {
			f.Tags = append(f.Tags, tag)
		}
	}
	if f.Type != "" && !slices.Contains(filterTypes, f.Type) {
		return f, nil, fmt.Errorf("%w: unknown type", memory.ErrValidation)
	}
	if f.Status != "" && !slices.Contains(filterStatuses, f.Status) {
		return f, nil, fmt.Errorf("%w: unknown status", memory.ErrValidation)
	}
	raw := q.Get("cursor")
	if raw == "" {
		return f, nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return f, nil, fmt.Errorf("%w: invalid cursor", memory.ErrValidation)
	}
	return f, &id, nil
}

func memoriesURL(contextName string, f memoryFilter, cursor *uuid.UUID) string {
	v := url.Values{}
	for key, val := range map[string]string{"q": f.Query, "project": f.Project, "type": f.Type, "status": f.Status} {
		if val != "" {
			v.Set(key, val)
		}
	}
	for _, tag := range f.Tags {
		v.Add("tag", tag)
	}
	if cursor != nil {
		v.Set("cursor", cursor.String())
	}
	return "/c/" + url.PathEscape(contextName) + "/memories?" + v.Encode()
}

func rowsOf(contextName string, items []memory.Summary, explain map[uuid.UUID]memory.Explanation) []memoryRow {
	rows := make([]memoryRow, 0, len(items))
	for _, m := range items {
		row := memoryRow{
			Href:    "/c/" + url.PathEscape(contextName) + "/memories/" + m.ID.String(),
			Title:   m.Title,
			Snippet: m.Snippet,
			Project: globalProject,
			Type:    m.Type.String(),
			Status:  m.Status.String(),
			Score:   fmt.Sprintf("%.2f", m.Score),
			Tags:    m.Tags,
		}
		if m.Project != nil && m.Scope != memory.ScopeGlobal {
			row.Project = *m.Project
		}
		if x, ok := explain[m.ID]; ok {
			row.Explain = explainOf(x)
		}
		rows = append(rows, row)
	}
	return rows
}

func explainOf(x memory.Explanation) *explainView {
	rankText := func(n int) string {
		if n == 0 {
			return noRank
		}
		return fmt.Sprint(n)
	}
	return &explainView{
		VectorRank: rankText(x.VectorRank), FTSRank: rankText(x.FTSRank), TrigramRank: rankText(x.TrigramRank),
		Similarity: fmt.Sprintf("%.2f", x.Similarity), RRF: fmt.Sprintf("%.3f", x.RRF), Textual: x.Textual,
		ProjectBoost: fmt.Sprintf("%.2f", x.Boost.Project), PinnedBoost: fmt.Sprintf("%.2f", x.Boost.Pinned),
		UsageBoost: fmt.Sprintf("%.2f", x.Boost.Usage), Multiplier: fmt.Sprintf("%.2f", x.Boost.Multiplier),
		Final: fmt.Sprintf("%.4f", x.Boost.Final),
	}
}

func cmpOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// fail maps err to a short safe message; unexpected errors are logged once and never shown.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, contextName string, err error) {
	status, message := http.StatusInternalServerError, "Something went wrong. See the memorable ui log."
	switch {
	case errors.Is(err, memory.ErrValidation):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, memory.ErrNotFound):
		status, message = http.StatusNotFound, "Memory not found."
	case errors.Is(err, memory.ErrUnavailable):
		status, message = http.StatusServiceUnavailable, "Memory store unavailable. Run memorable doctor."
	}
	if status >= http.StatusInternalServerError {
		s.logger.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "error", err)
	}
	page := s.authenticatedPage("Error", message)
	page.Context = contextName
	s.renderError(w, status, page)
}

func (s *Server) renderTemplate(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Error("render template", "template", name, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}
