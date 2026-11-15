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
	pageDetail   = "detail"
	pageEdit     = "edit"
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

type feedbackView struct {
	Kind      string
	Note      string
	Project   string
	CreatedAt string
}

type auditView struct {
	Action    string
	Process   string
	Cwd       string
	Details   string
	CreatedAt string
}

type detailView struct {
	Context     string
	ID          string
	CSRF        string
	Base        string
	EditURL     string
	ListURL     string
	Quarantined bool
	ReadOnly    bool
	Title       string
	Content     string
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
	CanActivate bool
	CanDelete   bool
	CanRestore  bool
	CanPurge    bool
	Confirm     string
	Versions    []versionView
	Links       []linkView
	Feedback    []feedbackView
	Audit       []auditView
	Notice      string
	Error       string
}

type editView struct {
	Context   string
	ID        string
	CSRF      string
	Base      string
	DetailURL string
	Title     string
	Content   string
	Tags      string
	Type      string
	Types     []string
	Pinned    bool
	ExpiresAt string
	Version   string
	Error     string
	Conflict  bool
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

func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request, rc requestContext, id uuid.UUID) {
	fetched, err := s.fetchPassive(r, rc, id)
	if err != nil && !errors.Is(err, memory.ErrQuarantined) {
		s.failResponse(w, r, rc, err)
		return
	}
	if err != nil || fetched.Memory.Scope == memory.ScopeProfile {
		s.handleDetail(w, r, rc, id)
		return
	}
	m := fetched.Memory
	edit := editView{
		Context: rc.Name, ID: id.String(), CSRF: s.session.csrf, Base: memoryBase(rc.Name, id), DetailURL: memoryBase(rc.Name, id),
		Title: m.Title, Content: m.Content, Tags: strings.Join(m.Tags, ", "), Type: m.Type.String(),
		Types: filterTypes, Pinned: m.Pinned, Version: fmt.Sprint(m.Version),
	}
	if m.ExpiresAt != nil {
		edit.ExpiresAt = m.ExpiresAt.UTC().Format(time.RFC3339)
	}
	s.respondEdit(w, r, rc, edit, http.StatusOK)
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
		Context: rc.Name, ID: id.String(), CSRF: s.session.csrf, Base: base, EditURL: base + "/edit",
		ListURL: s.contextHome(rc.Name),
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
	view.Feedback = feedbackViews(activity.Feedback)
	view.Audit = auditViews(activity.Audit)
	return view, nil
}

func fillMemory(view *detailView, m memory.Memory) {
	view.Title, view.Content = m.Title, m.Content
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

func feedbackViews(entries []memory.FeedbackEntry) []feedbackView {
	views := make([]feedbackView, 0, len(entries))
	for _, e := range entries {
		v := feedbackView{Kind: e.Kind.String(), Note: e.Note, CreatedAt: formatTime(e.CreatedAt)}
		if e.Project != nil {
			v.Project = *e.Project
		}
		views = append(views, v)
	}
	return views
}

func auditViews(records []memory.AuditRecord) []auditView {
	views := make([]auditView, 0, len(records))
	for _, rec := range records {
		keys := make([]string, 0, len(rec.Details))
		for k := range rec.Details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", k, rec.Details[k]))
		}
		views = append(views, auditView{
			Action: rec.Action, Process: rec.Process, Cwd: rec.Cwd, Details: strings.Join(parts, " "), CreatedAt: formatTime(rec.CreatedAt),
		})
	}
	return views
}

func isFragmentRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"
}

func (s *Server) respondDetail(w http.ResponseWriter, r *http.Request, rc requestContext, view detailView, status int) {
	w.Header().Set("Vary", "HX-Request")
	if isFragmentRequest(r) {
		s.renderStatus(w, status, "detail.html", view)
		return
	}
	page := s.contextPage(r, rc, cmpOr(view.Title, "Memory"), "")
	page.Page, page.Detail = pageDetail, &view
	s.renderStatus(w, status, "layout.html", page)
}

func (s *Server) respondEdit(w http.ResponseWriter, r *http.Request, rc requestContext, view editView, status int) {
	w.Header().Set("Vary", "HX-Request")
	if isFragmentRequest(r) {
		s.renderStatus(w, status, "edit_form.html", view)
		return
	}
	page := s.contextPage(r, rc, "Edit memory", "")
	page.Page, page.Edit = pageEdit, &view
	s.renderStatus(w, status, "layout.html", page)
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
	w.Header().Set("Vary", "HX-Request")
	s.renderStatus(w, status, "error.html", pageData{Title: "Error", Message: message})
}
