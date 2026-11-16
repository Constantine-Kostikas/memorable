package webui

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const (
	detailPaneID = "detail-pane"
	maxDiffLines = 1000
	timeLayout   = "2006-01-02 15:04 MST"
)

type diffLine struct {
	Op     string
	Prefix string
	Text   string
}

type versionView struct {
	Version    int
	Title      string
	Content    string
	Tags       []string
	Type       string
	ChangeKind string
	ChangedAt  string
	Current    bool
	HasDiff    bool
	DiffNote   string
	Diff       []diffLine
}

type linkView struct {
	Kind     string
	Outgoing bool
	OtherID  string
	Href     string
}

// activityEntry is one feedback or audit event on a memory, merged into one newest-first list.
type activityEntry struct {
	At      time.Time
	When    string
	Kind    string
	Summary string
}

// editFields is the editor's form state: the stored memory on load, the submitted values after a failed save.
type editFields struct {
	Title   string
	Content string
	Tags    string
	Type    string
	// ExpiresAt is the date part the date input shows; ExpiresAtOriginal is the full stored timestamp it came
	// from, so an untouched date keeps its time on save.
	ExpiresAt         string
	ExpiresAtOriginal string
	Pinned            bool
	Version           int
}

type detailView struct {
	Context     string
	ID          string
	CSRF        string
	Base        string
	Lookup      string
	ListURL     string
	FilterQuery string
	Quarantined bool
	ReadOnly    bool
	Editable    bool
	Title       string
	Type        string
	Status      string
	Scope       string
	Project     string
	Slug        string
	Tags        []string
	Pinned      bool
	Version     int
	ExpiresAt   string
	CreatedAt   string
	UpdatedAt   string
	Embedding   string
	Form        editFields
	Stored      editFields
	Unsaved     bool
	Types       []string
	CanActivate bool
	CanDelete   bool
	CanRestore  bool
	CanPurge    bool
	Confirm     string
	Versions    []versionView
	Links       []linkView
	Activity    []activityEntry
	Notice      string
	Error       string
	FormError   string
	Conflict    bool
	// Row, set after a successful pane action, refreshes the memory's list row out of band.
	Row *memoryRow
	// listRow is the memory's list row as loaded; quarantined memories have none.
	listRow *memoryRow
}

// refreshRow asks the pane response to also replace the memory's list row, so the list shows the change.
func (v *detailView) refreshRow() {
	v.Row = v.listRow
}

func memoryBase(contextName string, id uuid.UUID) string {
	return "/c/" + url.PathEscape(contextName) + "/memories/" + id.String()
}

// withMemoryID parses the {id} path value; anything that is not a UUID is a plain 404.
func (s *Server) withMemoryID(h func(http.ResponseWriter, *http.Request, requestContext, uuid.UUID)) func(http.ResponseWriter, *http.Request, requestContext) {
	return func(w http.ResponseWriter, r *http.Request, rc requestContext) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			s.failResponse(w, r, rc, memory.ErrNotFound)
			return
		}
		h(w, r, rc, id)
	}
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID) {
	view, err := s.loadDetail(r, rc, id)
	if err != nil {
		s.failResponse(w, r, rc, err)
		return
	}
	s.respondDetail(w, r, rc, view, http.StatusOK)
}

// handleEdit keeps old /edit links working, list filter included: the detail pane is where a memory is changed.
func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID) {
	filter, _, err := parseMemoryFilter(r.URL.Query())
	if err != nil {
		filter = memoryFilter{}
	}
	target := withQuery(memoryBase(rc.Name, id), filterValues(filter))
	http.Redirect(w, r, target, http.StatusSeeOther) //nolint:gosec // G710: a same-origin path built from a configured context name, a parsed UUID, and re-encoded filter values.
}

func (s *Server) fetchPassive(r *http.Request, rc requestContext, id uuid.UUID) (memory.Fetched, error) {
	res, err := rc.Backend.Memories.Get(r.Context(), memory.GetRequest{IDs: []uuid.UUID{id}, Passive: true})
	if err != nil {
		return memory.Fetched{}, fmt.Errorf("get memory: %w", err)
	}
	if len(res) != 1 {
		return memory.Fetched{}, fmt.Errorf("get memory: %w", memory.ErrNotFound)
	}
	return res[0], res[0].Err
}

// loadDetail reads the memory passively. History and Activity run only after a successful Get,
// so a quarantined memory shows its status and nothing else.
func (s *Server) loadDetail(r *http.Request, rc requestContext, id uuid.UUID) (detailView, error) {
	base := memoryBase(rc.Name, id)
	view := detailView{
		Context: rc.Name, ID: id.String(), CSRF: s.session.csrf, Base: base, Types: filterTypes,
		Lookup: contextBase(rc.Name) + "/lookup",
	}
	fetched, err := s.fetchPassive(r, rc, id)
	if errors.Is(err, memory.ErrQuarantined) {
		view.Quarantined = true
		view.Status = memory.StatusQuarantined.String()
		return view, nil
	}
	if err != nil {
		return view, err
	}
	m := fetched.Memory
	fillMemory(&view, m)
	view.listRow = &memoryRow{
		ID: id.String(), Title: m.Title, Project: view.Project, Type: view.Type, TypeClass: typeClass(view.Type),
		Status: view.Status, Age: relativeAge(s.cfg.Now(), m.UpdatedAt), Current: true, OOB: true,
	}
	view.CanPurge = !view.ReadOnly && s.admin.minutesLeft() > 0
	view.Confirm = purgeConfirmation(m)
	history, err := rc.Backend.Memories.History(r.Context(), id)
	if err != nil {
		return view, fmt.Errorf("load history: %w", err)
	}
	activity, err := rc.Backend.Memories.Activity(r.Context(), id)
	if err != nil {
		return view, fmt.Errorf("load activity: %w", err)
	}
	view.Versions = versionViews(history, m.Version)
	view.Links = linkViews(rc.Name, id, fetched.Links)
	view.Activity = activityEntries(activity)
	return view, nil
}

func fillMemory(view *detailView, m memory.Memory) {
	view.Title = m.Title
	view.Type, view.Status, view.Scope = m.Type.String(), m.Status.String(), m.Scope.String()
	view.Project = globalProject
	if m.Project != nil && m.Scope != memory.ScopeGlobal {
		view.Project = *m.Project
	}
	if m.Slug != nil {
		view.Slug = *m.Slug
	}
	view.Tags, view.Pinned, view.Version = m.Tags, m.Pinned, m.Version
	view.CreatedAt, view.UpdatedAt = formatTime(m.CreatedAt), formatTime(m.UpdatedAt)
	if m.ExpiresAt != nil {
		view.ExpiresAt = formatTime(*m.ExpiresAt)
	}
	view.Embedding = "Not embedded yet: text search only."
	if m.EmbeddingModel != nil {
		view.Embedding = fmt.Sprintf("Embedded with %s (version %d).", *m.EmbeddingModel, m.Version)
	}
	view.ReadOnly = m.Scope == memory.ScopeProfile
	view.Editable = !view.ReadOnly && m.Status != memory.StatusDeleted && m.Status != memory.StatusSuperseded
	view.Form = editFields{
		Title: m.Title, Content: m.Content, Tags: strings.Join(m.Tags, ", "), Type: m.Type.String(),
		Pinned: m.Pinned, Version: m.Version,
	}
	if m.ExpiresAt != nil {
		view.Form.ExpiresAt = m.ExpiresAt.UTC().Format(time.DateOnly)
		view.Form.ExpiresAtOriginal = m.ExpiresAt.UTC().Format(time.RFC3339)
	}
	view.Stored = view.Form
	view.CanActivate = m.Status == memory.StatusFlagged || m.Status == memory.StatusUnverified
	view.CanRestore = m.Status == memory.StatusDeleted
	view.CanDelete = m.Status != memory.StatusDeleted
}

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func versionViews(history []memory.Version, current int) []versionView {
	sorted := append([]memory.Version(nil), history...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })
	views := make([]versionView, 0, len(sorted))
	for i, v := range sorted {
		view := versionView{
			Version: v.Version, Title: v.Title, Content: v.Content, Tags: v.Tags, Type: v.Type.String(),
			ChangeKind: v.ChangeKind, ChangedAt: formatTime(v.ChangedAt), Current: v.Version == current,
		}
		if i > 0 {
			view.HasDiff = true
			view.Diff, view.DiffNote = versionDiff(sorted[i-1], v)
		}
		views = append(views, view)
	}
	for i, j := 0, len(views)-1; i < j; i, j = i+1, j-1 {
		views[i], views[j] = views[j], views[i]
	}
	return views
}

func versionDiff(prev, next memory.Version) ([]diffLine, string) {
	lines, ok := lineDiff(prev.Title+"\n"+prev.Content, next.Title+"\n"+next.Content)
	if !ok {
		return nil, "Diff too large to show."
	}
	return lines, ""
}

// lineDiff is a line-based LCS diff; ok is false when either side is too long to diff cheaply.
func lineDiff(oldText, newText string) (lines []diffLine, ok bool) {
	a, b := strings.Split(oldText, "\n"), strings.Split(newText, "\n")
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		return nil, false
	}
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			lines = append(lines, diffLine{Op: "=", Prefix: "  ", Text: a[i]})
			i++
			j++
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			lines = append(lines, diffLine{Op: "-", Prefix: "- ", Text: a[i]})
			i++
		default:
			lines = append(lines, diffLine{Op: "+", Prefix: "+ ", Text: b[j]})
			j++
		}
	}
	return lines, true
}

func linkViews(contextName string, id uuid.UUID, links []memory.Link) []linkView {
	views := make([]linkView, 0, len(links))
	for _, l := range links {
		other, outgoing := l.From, false
		if l.From == id {
			other, outgoing = l.To, true
		}
		views = append(views, linkView{Kind: l.Kind, Outgoing: outgoing, OtherID: other.String(), Href: memoryBase(contextName, other)})
	}
	return views
}

func activityEntries(activity memory.Activity) []activityEntry {
	entries := make([]activityEntry, 0, len(activity.Feedback)+len(activity.Audit))
	for _, e := range activity.Feedback {
		parts := []string{e.Kind.String()}
		if e.Project != nil {
			parts = append(parts, *e.Project)
		}
		if e.Note != "" {
			parts = append(parts, e.Note)
		}
		entries = append(entries, activityEntry{At: e.CreatedAt, Kind: "feedback", Summary: strings.Join(parts, " · ")})
	}
	for _, rec := range activity.Audit {
		parts := []string{rec.Action, rec.Process}
		if rec.Cwd != "" {
			parts = append(parts, rec.Cwd)
		}
		if details := auditDetails(rec.Details); details != "" {
			parts = append(parts, details)
		}
		entries = append(entries, activityEntry{At: rec.CreatedAt, Kind: "audit", Summary: strings.Join(parts, " · ")})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })
	for i := range entries {
		entries[i].When = formatTime(entries[i].At)
	}
	return entries
}

func auditDetails(details map[string]any) string {
	keys := make([]string, 0, len(details))
	for k := range details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, details[k]))
	}
	return strings.Join(parts, " ")
}

func isFragmentRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"
}

// respondDetail answers an htmx request with the detail pane alone and any other request with the
// workspace: the list rebuilt from the query string's filter beside the memory. The pane alone never
// renders the list, so a malformed filter does not fail it; it only drops the filter from the pane's links.
func (s *Server) respondDetail(w http.ResponseWriter, r *http.Request, rc requestContext, view detailView, status int) {
	filter, _, err := parseMemoryFilter(r.URL.Query())
	fragment := isFragmentRequest(r)
	switch {
	case err != nil && fragment:
		filter = memoryFilter{}
	case err != nil:
		s.failResponse(w, r, rc, err)
		return
	}
	view.ListURL = memoriesURL(rc.Name, filter, nil)
	view.FilterQuery = withQuery("", filterValues(filter))
	w.Header().Set("Vary", "HX-Request")
	if fragment {
		if view.Row != nil {
			row := *view.Row
			row.Href = withQuery(view.Base, filterValues(filter))
			view.Row = &row
		}
		s.renderStatus(w, status, "detail_response", view)
		return
	}
	// The full page rebuilds the list, so no row goes out of band.
	view.Row = nil
	list := memoriesView{Context: rc.Name, Filter: filter, SelectedID: view.ID, Selected: &view}
	if err := s.loadRows(r, rc, &list, nil); err != nil {
		s.failResponse(w, r, rc, err)
		return
	}
	s.renderStatus(w, status, "layout.html", s.workspacePage(r, rc, &list, cmpOr(view.Title, "Memory")))
}

// listURLFor is the list URL for the Back link: the request's filter, or the plain list when it is malformed.
func listURLFor(r *http.Request, rc requestContext) string {
	filter, _, err := parseMemoryFilter(r.URL.Query())
	if err != nil {
		filter = memoryFilter{}
	}
	return memoriesURL(rc.Name, filter, nil)
}

func (s *Server) renderStatus(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Error("render template", "template", name, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// failResponse is fail for pages and for htmx swaps: a fragment request gets only the error box.
func (s *Server) failResponse(w http.ResponseWriter, r *http.Request, rc requestContext, err error) {
	if !isFragmentRequest(r) {
		s.fail(w, r, rc, err)
		return
	}
	status, message := s.actionStatus(r, err)
	w.Header().Set("Vary", "HX-Request, HX-Target")
	name := "error.html"
	if r.Header.Get("HX-Target") == detailPaneID {
		name = "pane_error.html"
	}
	s.renderStatus(w, status, name, pageData{Title: "Error", Message: message, ListURL: listURLFor(r, rc)})
}
