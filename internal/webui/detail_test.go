package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

type call struct {
	Name string
	Args []any
}

type detailMemories struct {
	Memories
	mu       sync.Mutex
	calls    []call
	getReqs  []memory.GetRequest
	fetched  []memory.Fetched
	getErr   error
	history  []memory.Version
	activity memory.Activity
	bySlug   map[string]memory.Fetched
	byID     map[uuid.UUID]memory.Fetched
	mutErr   error
	updated  memory.Memory
	listReqs []memory.ListRequest
	searches []memory.SearchRequest
}

func (d *detailMemories) rows() []memory.Summary {
	var rows []memory.Summary
	for _, f := range d.fetched {
		if f.Err == nil {
			m := f.Memory
			rows = append(rows, memory.Summary{ID: m.ID, Title: m.Title, Type: m.Type, Scope: m.Scope, Status: m.Status, Project: m.Project, UpdatedAt: m.UpdatedAt})
		}
	}
	return rows
}

func (d *detailMemories) List(_ context.Context, r memory.ListRequest) (memory.ListResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.listReqs = append(d.listReqs, r)
	return memory.ListResponse{Items: d.rows()}, nil
}

func (d *detailMemories) Search(_ context.Context, r memory.SearchRequest) (memory.SearchResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.searches = append(d.searches, r)
	return memory.SearchResponse{Results: d.rows()}, nil
}

func (d *detailMemories) lastSearches() []memory.SearchRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]memory.SearchRequest(nil), d.searches...)
}

func (d *detailMemories) Review(context.Context) (memory.ReviewQueue, error) {
	return memory.ReviewQueue{}, nil
}

func (d *detailMemories) record(name string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, call{Name: name, Args: args})
}

func (d *detailMemories) called(name string) []call {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []call
	for _, c := range d.calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func (d *detailMemories) Get(_ context.Context, r memory.GetRequest) ([]memory.Fetched, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.getReqs = append(d.getReqs, r)
	if len(r.Slugs) > 0 {
		return []memory.Fetched{d.bySlug[r.Slugs[0]]}, nil
	}
	if f, ok := d.byID[r.IDs[0]]; ok {
		return []memory.Fetched{f}, nil
	}
	if len(d.fetched) == 1 && d.fetched[0].Memory.ID != r.IDs[0] && d.fetched[0].Err == nil {
		return []memory.Fetched{{Ref: r.IDs[0].String(), Err: memory.ErrNotFound}}, nil
	}
	return d.fetched, d.getErr
}

func (d *detailMemories) History(context.Context, uuid.UUID) ([]memory.Version, error) {
	d.record("History")
	return d.history, nil
}

func (d *detailMemories) Activity(context.Context, uuid.UUID) (memory.Activity, error) {
	d.record("Activity")
	return d.activity, nil
}

func (d *detailMemories) Update(_ context.Context, id uuid.UUID, expected *int, p memory.Patch) (memory.Memory, error) {
	d.record("Update", id, *expected, p)
	return d.updated, d.mutErr
}

func (d *detailMemories) Delete(_ context.Context, id uuid.UUID) error {
	d.record("Delete", id)
	return d.mutErr
}

func (d *detailMemories) Restore(_ context.Context, id uuid.UUID, v *int) (memory.Memory, error) {
	d.record("Restore", id, v)
	return memory.Memory{}, d.mutErr
}

func (d *detailMemories) Feedback(_ context.Context, id uuid.UUID, k memory.FeedbackKind, note string) error {
	d.record("Feedback", id, k, note)
	return d.mutErr
}

func (d *detailMemories) Link(_ context.Context, l memory.Link, remove bool) error {
	d.record("Link", l, remove)
	return d.mutErr
}

func (d *detailMemories) Supersede(_ context.Context, old, replacement uuid.UUID) error {
	d.record("Supersede", old, replacement)
	return d.mutErr
}

func (d *detailMemories) Activate(_ context.Context, id uuid.UUID) error {
	d.record("Activate", id)
	return d.mutErr
}

func (d *detailMemories) lastGets() []memory.GetRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]memory.GetRequest(nil), d.getReqs...)
}

type detailFixture struct {
	h   http.Handler
	c   *http.Cookie
	s   *Server
	mem *detailMemories
	id  uuid.UUID
}

func (f detailFixture) path(suffix string) string {
	return "/c/personal/memories/" + f.id.String() + suffix
}

func (f detailFixture) post(suffix string, form url.Values, opts ...requestOption) *httptest.ResponseRecorder {
	if form.Get("csrf") == "" {
		form.Set("csrf", f.s.session.csrf)
	}
	opts = append([]requestOption{withCookie(f.c), withHeader("Content-Type", "application/x-www-form-urlencoded")}, opts...)
	return send(f.h, http.MethodPost, f.path(suffix), form.Encode(), opts...)
}

func newDetailFixture(t *testing.T, mutate func(*detailMemories, uuid.UUID)) detailFixture {
	t.Helper()
	id := uuid.New()
	model := "nomic-embed-text"
	other := uuid.New()
	mem := &detailMemories{
		fetched: []memory.Fetched{{
			Ref: id.String(),
			Memory: memory.Memory{
				ID: id, Title: "Port map", Content: "line one\n" + hostile, Type: memory.TypeProject, Status: memory.StatusActive,
				Scope: memory.ScopeProject, Tags: []string{"go", "ops"}, Version: 2, EmbeddingModel: &model,
				CreatedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
			},
			Links: []memory.Link{{From: id, To: other, Kind: memory.LinkRelated}},
		}},
		history: []memory.Version{
			{Version: 2, Title: "Port map", Content: "line one\nline two", ChangeKind: "update", ChangedAt: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)},
			{Version: 1, Title: "Port map", Content: "line one\nold line", ChangeKind: "create", ChangedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)},
		},
		activity: memory.Activity{
			Feedback: []memory.FeedbackEntry{{Kind: memory.FeedbackUseful, Note: "saved me " + hostile, CreatedAt: time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)}},
			Audit:    []memory.AuditRecord{{Action: "update", Process: "ui:42@2026-11-15T10:00:00Z", Details: map[string]any{"via": "ui"}, CreatedAt: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}},
		},
		bySlug: map[string]memory.Fetched{},
		byID:   map[uuid.UUID]memory.Fetched{},
	}
	if mutate != nil {
		mutate(mem, id)
	}
	cfg := testConfig(&backendRecorder{})
	cfg.Backend = func(context.Context, string) (Backend, error) { return Backend{Memories: mem, Store: fakeStore{}}, nil }
	s, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	h := s.Handler(testPort)
	return detailFixture{h: h, c: login(t, s, h), s: s, mem: mem, id: id}
}

func TestDetailTabs(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "<html")
	assert.Contains(t, body, "Port map")
	assert.Contains(t, body, "Embedded with nomic-embed-text")
	assert.Contains(t, body, "line one\n&lt;img src=x onerror=alert(1)&gt;</textarea>")
	assert.NotContains(t, body, hostile)
	assert.Contains(t, body, "&lt;img src=x onerror=alert(1)&gt;")
	assert.Contains(t, body, "Version 1")
	assert.Contains(t, body, "- old line")
	assert.Contains(t, body, "&#43; line two")
	assert.Contains(t, body, "related")
	assert.Contains(t, body, "saved me")
	assert.Contains(t, body, "useful")
	assert.Contains(t, body, "ui:42@2026-11-15T10:00:00Z")
	assert.Contains(t, body, "via=ui")
	assert.Equal(t, 2, len(f.mem.called("History"))+len(f.mem.called("Activity")))
}

func TestDetailIsPassive(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	gets := f.mem.lastGets()
	require.Len(t, gets, 1)
	assert.True(t, gets[0].Passive)
	assert.Equal(t, []uuid.UUID{f.id}, gets[0].IDs)
}

func TestDetailQuarantinedHidden(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, id uuid.UUID) {
		m.fetched = []memory.Fetched{{Ref: id.String(), Memory: memory.Memory{ID: id, Status: memory.StatusQuarantined}, Err: memory.ErrQuarantined}}
	})

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "quarantined")
	assert.NotContains(t, body, "line one")
	assert.NotContains(t, body, "<pre")
	assert.Empty(t, f.mem.called("Activity"))
	assert.Empty(t, f.mem.called("History"))
}

func TestDetailInvalidIDIsNotFound(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, "/c/personal/memories/not-a-uuid", "", withCookie(f.c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, f.mem.lastGets())
}

func TestDetailNotFound(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, id uuid.UUID) {
		m.fetched = []memory.Fetched{{Ref: id.String(), Err: memory.ErrNotFound}}
	})

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, f.mem.called("Activity"))
}

func TestDetailFragmentForHtmx(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "<html")
	assert.Contains(t, rec.Body.String(), `<section id="detail-pane"`)
}

func TestDetailFullPageRendersShell(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path("?q=go"), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "<html")
	assert.Contains(t, body, `id="memory-rows"`)
	assert.Contains(t, body, `id="detail-pane"`)
	assert.Regexp(t, `<a href="`+regexp.QuoteMeta(f.path("?q=go"))+`"[^>]*aria-current="true"`, body)
	searches := f.mem.lastSearches()
	require.Len(t, searches, 1)
	assert.Equal(t, "go", searches[0].Query)
}

func TestDetailFragmentForPane(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path("?q=go"), "", withCookie(f.c),
		withHeader("HX-Request", "true"), withHeader("HX-Target", "detail-pane"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.True(t, strings.HasPrefix(body, `<section id="detail-pane"`), body[:min(len(body), 80)])
	assert.NotContains(t, body, `id="memory-rows"`)
	assert.Empty(t, f.mem.lastSearches(), "a pane swap does not rebuild the list")
}

func TestDetailPaneErrorKeepsPaneID(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, id uuid.UUID) {
		m.fetched = []memory.Fetched{{Ref: id.String(), Err: memory.ErrNotFound}}
	})

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c),
		withHeader("HX-Request", "true"), withHeader("HX-Target", "detail-pane"))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.True(t, strings.HasPrefix(rec.Body.String(), `<section id="detail-pane"`), "later selections still find the pane")
	assert.Contains(t, rec.Body.String(), "Memory not found.")
}

func TestHistoryRestoreGetsFullPage(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c),
		withHeader("HX-Request", "true"), withHeader("HX-History-Restore-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "<html")
	assert.Contains(t, body, `id="memory-rows"`)
	assert.Contains(t, body, `id="detail-pane"`)
}

func TestEditURLRedirects(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path("/edit"), "", withCookie(f.c))

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, f.path(""), rec.Header().Get("Location"))
}

func TestLineDiffDeterministic(t *testing.T) {
	t.Parallel()

	lines, ok := lineDiff("a\nb\nc", "a\nx\nc")

	require.True(t, ok)
	got := make([]string, 0, len(lines))
	for _, l := range lines {
		got = append(got, l.Prefix+l.Text)
	}
	assert.Equal(t, []string{"  a", "- b", "+ x", "  c"}, got)
	_, ok = lineDiff(strings.Repeat("a\n", maxDiffLines+1), "a")
	assert.False(t, ok)
}

func TestDetailProfileHidesMutations(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.fetched[0].Memory.Scope = memory.ScopeProfile
	})

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Port map")
	for _, action := range []string{"/edit", "/delete", "/supersede", "/link", "/unlink", "/feedback", "/restore", "/activate"} {
		assert.NotContains(t, body, f.path(action), action)
	}
	edit := send(f.h, http.MethodGet, f.path("/edit"), "", withCookie(f.c))
	assert.Equal(t, http.StatusSeeOther, edit.Code)
}

func TestHtmxConfigMeta(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	m := regexp.MustCompile(`name="htmx-config" content='([^']*)'`).FindStringSubmatch(rec.Body.String())
	require.Len(t, m, 2)
	var cfg struct {
		IncludeIndicatorStyles bool `json:"includeIndicatorStyles"`
		AllowEval              bool `json:"allowEval"`
		HistoryCacheSize       *int `json:"historyCacheSize"`
		ResponseHandling       []struct {
			Code  string `json:"code"`
			Swap  bool   `json:"swap"`
			Error bool   `json:"error"`
		} `json:"responseHandling"`
	}
	require.NoError(t, json.Unmarshal([]byte(m[1]), &cfg))
	assert.False(t, cfg.IncludeIndicatorStyles)
	assert.False(t, cfg.AllowEval)
	require.NotNil(t, cfg.HistoryCacheSize, "historyCacheSize must be set")
	assert.Zero(t, *cfg.HistoryCacheSize, "no page snapshots, memory content, or CSRF tokens in sessionStorage")
	swapped := func(status string) (swap, isErr bool) {
		for _, rule := range cfg.ResponseHandling {
			if regexp.MustCompile("^(?:" + rule.Code + ")$").MatchString(status) {
				return rule.Swap, rule.Error
			}
		}
		return false, false
	}
	for _, status := range []string{"404", "409", "422", "429", "500", "503"} {
		swap, isErr := swapped(status)
		assert.True(t, swap && isErr, status)
	}
	for _, status := range []string{"403"} {
		swap, isErr := swapped(status)
		assert.False(t, swap, status)
		assert.True(t, isErr, status)
	}
	swap, _ := swapped("200")
	assert.True(t, swap)
}

func editorForm(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<form id="memory-editor"`)
	require.GreaterOrEqual(t, start, 0, "the pane has the editor form")
	end := strings.Index(body[start:], "</form>")
	require.Positive(t, end)
	return body[start : start+end]
}

func TestDetailIsAnEditor(t *testing.T) {
	t.Parallel()
	expires := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.fetched[0].Memory.ExpiresAt = &expires
		m.fetched[0].Memory.Pinned = true
	})

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	editor := editorForm(t, body)
	assert.Contains(t, editor, `data-editor`)
	assert.Contains(t, editor, `data-title="Port map"`)
	assert.Contains(t, editor, `action="`+f.path("/update")+`"`)
	assert.Contains(t, editor, `name="title" value="Port map"`)
	assert.Contains(t, editor, "line one\n&lt;img src=x onerror=alert(1)&gt;</textarea>")
	assert.Contains(t, editor, `name="tags" value="go, ops"`)
	assert.Contains(t, editor, `<option value="project" selected>`)
	assert.Contains(t, editor, `name="expires_at" value="2026-12-31T00:00:00Z"`)
	assert.Regexp(t, `name="pinned"[^>]*checked`, editor)
	assert.Contains(t, editor, `name="version" value="2"`)
	assert.Contains(t, editor, `name="csrf" value="`+f.s.session.csrf+`"`)
	assert.Regexp(t, `<span[^>]*data-dirty-marker[^>]*hidden`, editor)
	assert.NotContains(t, editor[1:], "<form", "no form is nested in the editor")
	assert.Regexp(t, `<button[^>]*form="memory-editor"[^>]*data-save`, body)
	assert.Regexp(t, `<a href="`+regexp.QuoteMeta(f.path(""))+`"[^>]*data-discard>Discard</a>`, body)
	for _, section := range []string{"content", "versions", "links", "activity"} {
		assert.Contains(t, body, `<a role="tab" href="#`+section+`"`, section)
		assert.Contains(t, body, `id="`+section+`"`, section)
	}
}

func TestPaneHasExpandToggle(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	toggle := regexp.MustCompile(`<button[^>]*data-expand-toggle[^>]*>`).FindString(rec.Body.String())
	require.NotEmpty(t, toggle, "the pane has an Expand toggle")
	assert.Contains(t, toggle, `type="button"`)
	assert.Contains(t, toggle, `aria-pressed="false"`)
	assert.Regexp(t, `\shidden[\s>]`, toggle, "hidden until app.js reveals it, so it is absent without JS")
}

func TestDetailHasEditorWithoutJS(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	save := regexp.MustCompile(`<button[^>]*data-save[^>]*>`).FindString(rec.Body.String())
	require.NotEmpty(t, save)
	assert.Contains(t, save, `type="submit"`)
	assert.NotContains(t, save, "disabled")
}

func TestReadOnlyStatesDisableFields(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*memory.Memory){
		"deleted":    func(m *memory.Memory) { m.Status = memory.StatusDeleted },
		"superseded": func(m *memory.Memory) { m.Status = memory.StatusSuperseded },
		"profile":    func(m *memory.Memory) { m.Scope = memory.ScopeProfile },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { mutate(&m.fetched[0].Memory) })

			rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			editor := editorForm(t, body)
			assert.Regexp(t, `<input[^>]*name="title"[^>]*disabled`, editor)
			assert.Regexp(t, `<textarea[^>]*name="content"[^>]*disabled`, editor)
			assert.NotContains(t, body, "data-save")
			assert.NotContains(t, body, f.path("/update"))
		})
	}
}

func TestDeletedMemoryKeepsRestore(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { m.fetched[0].Memory.Status = memory.StatusDeleted })

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `action="`+f.path("/restore")+`"`)
}

func TestActivityMergesFeedbackAndAudit(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	t2, t3 := t1.Add(time.Hour), t1.Add(2*time.Hour)

	entries := activityEntries(memory.Activity{
		Feedback: []memory.FeedbackEntry{{Kind: memory.FeedbackWrong, Note: "stale", CreatedAt: t2}},
		Audit: []memory.AuditRecord{
			{Action: "create", Process: "mcp:1", CreatedAt: t1},
			{Action: "update", Process: "ui:2", Details: map[string]any{"via": "ui"}, CreatedAt: t3},
		},
	})

	require.Len(t, entries, 3)
	assert.Equal(t, []time.Time{t3, t2, t1}, []time.Time{entries[0].At, entries[1].At, entries[2].At})
	assert.Equal(t, formatTime(t3), entries[0].When)
	assert.Contains(t, entries[0].Summary, "update")
	assert.Contains(t, entries[0].Summary, "via=ui")
	assert.Contains(t, entries[1].Summary, "wrong")
	assert.Contains(t, entries[1].Summary, "stale")
	assert.NotEqual(t, entries[0].Kind, entries[1].Kind)
}

func TestDangerActionsSeparated(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	body := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c)).Body.String()

	more := regexp.MustCompile(`(?s)<details[^>]*>\s*<summary[^>]*>More actions</summary>.*?</details>`).FindString(body)
	require.NotEmpty(t, more, "a More actions disclosure")
	divider := strings.Index(more, "<hr")
	deleteAt := strings.Index(more, `action="`+f.path("/delete")+`"`)
	require.Positive(t, divider)
	assert.Greater(t, deleteAt, divider, "Delete comes after the divider")
	for _, action := range []string{"/feedback", "/supersede"} {
		at := strings.Index(more, `action="`+f.path(action)+`"`)
		assert.True(t, at >= 0 && at < divider, action)
	}
	assert.Equal(t, 1, strings.Count(body, `action="`+f.path("/delete")+`"`), "Delete only in the menu")
	assert.NotContains(t, body, "/purge", "no purge without admin")

	slug := "port-map"
	m := memory.Memory{ID: uuid.New(), Slug: &slug, Title: "Port map", Status: memory.StatusActive, Scope: memory.ScopeProject}
	admin := newAdminFixture(t, func(_ *Config, _ *adminFactory, mem *detailMemories, _ *pageStore) {
		mem.byID[m.ID] = memory.Fetched{Ref: m.ID.String(), Memory: m}
	})
	page := getPage(admin.h, admin.c, "/c/personal/memories/"+m.ID.String()).Body.String()
	activity := page[strings.Index(page, `id="activity"`):]
	zone := activity[strings.Index(activity, "Danger zone"):]
	assert.Contains(t, zone, `action="/c/personal/memories/`+m.ID.String()+`/purge"`)
	assert.Contains(t, zone, `name="confirm"`)
	assert.Equal(t, 1, strings.Count(page, "/purge\""), "Purge only in the danger zone")
}

func TestPaneIgnoresMalformedFilter(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path("?type=bogus&cursor=x"), "", withCookie(f.c),
		withHeader("HX-Request", "true"), withHeader("HX-Target", "detail-pane"))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `href="/c/personal/memories" class="underline">Back to memories`)
}

func TestPaneErrorHasBackLink(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, id uuid.UUID) {
		m.fetched = []memory.Fetched{{Ref: id.String(), Err: memory.ErrNotFound}}
	})

	rec := send(f.h, http.MethodGet, f.path("?q=go"), "", withCookie(f.c),
		withHeader("HX-Request", "true"), withHeader("HX-Target", "detail-pane"))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), `href="/c/personal/memories?q=go" class="underline">Back to memories`)
}

func TestPaneFormsCarryListFilter(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	body := send(f.h, http.MethodGet, f.path("?q=go&type=project"), "", withCookie(f.c)).Body.String()

	for _, action := range []string{"/update", "/feedback", "/delete", "/supersede", "/link"} {
		assert.Contains(t, body, `action="`+f.path(action)+`?q=go&amp;type=project"`, action)
	}
}

func TestToastRegionInLayout(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	body := f.post("/feedback", url.Values{"kind": {"useful"}}).Body.String()

	region := body[strings.Index(body, `<div id="toasts" role="status" aria-live="polite"`):]
	assert.Contains(t, region[:strings.Index(region, "</main>")+1], "Feedback recorded.")
	assert.NotContains(t, body, "hx-swap-oob", "full pages render toasts inline")
	assert.Regexp(t, `<button type="button"[^>]*data-toast-dismiss`, body)
}
