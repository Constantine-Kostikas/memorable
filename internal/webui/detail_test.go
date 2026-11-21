package webui

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	// getErrByID fails a Get whose first id is a key, so a test can fail only the linked-memory reads.
	getErrByID     map[uuid.UUID]error
	mutErr         error
	updated        memory.Memory
	listReqs       []memory.ListRequest
	searches       []memory.SearchRequest
	results        []memory.Summary
	tagCounts      []memory.TagCount
	tagAllProjects bool
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
	if d.results != nil {
		return memory.SearchResponse{Results: d.results}, nil
	}
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
	if err, ok := d.getErrByID[r.IDs[0]]; ok {
		return nil, err
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
	if d.mutErr == nil {
		d.applyToFetched(id, func(m *memory.Memory) {
			if p.Title != nil {
				m.Title = *p.Title
			}
			if p.Type != nil {
				m.Type = *p.Type
			}
		})
	}
	return d.updated, d.mutErr
}

// applyToFetched changes the stored memory the way a successful mutation would, so a reload shows it.
func (d *detailMemories) applyToFetched(id uuid.UUID, change func(*memory.Memory)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.fetched {
		if d.fetched[i].Memory.ID == id {
			change(&d.fetched[i].Memory)
		}
	}
}

func (d *detailMemories) Delete(_ context.Context, id uuid.UUID) error {
	d.record("Delete", id)
	if d.mutErr == nil {
		d.applyToFetched(id, func(m *memory.Memory) { m.Status = memory.StatusDeleted })
	}
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
	require.Len(t, gets, 2)
	assert.Equal(t, []uuid.UUID{f.id}, gets[0].IDs)
	assert.Equal(t, []uuid.UUID{linkedID(f)}, gets[1].IDs)
	for _, g := range gets {
		assert.True(t, g.Passive)
	}
}

func linkedID(f detailFixture) uuid.UUID {
	return f.mem.fetched[0].Links[0].To
}

func linksSection(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `id="links"`)
	end := strings.Index(body, `id="activity"`)
	require.True(t, start >= 0 && end > start, "links section not found")
	return body[start:end]
}

func TestDetailLinkShowsLinkedTitleAndPeek(t *testing.T) {
	t.Parallel()
	project := "memorable"
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		other := m.fetched[0].Links[0].To
		m.byID[other] = memory.Fetched{Ref: other.String(), Memory: memory.Memory{
			ID: other, Title: "Deploy checklist " + hostile, Content: "first   line\n\nsecond line", Type: memory.TypeProject,
			Status: memory.StatusFlagged, Scope: memory.ScopeProject, Project: &project, UpdatedAt: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
		}}
	})

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	links := linksSection(t, rec.Body.String())
	assert.Contains(t, links, `aria-describedby="link-peek-0">Deploy checklist &lt;img src=x onerror=alert(1)&gt;</a>`)
	assert.Contains(t, links, `role="tooltip" id="link-peek-0"`)
	assert.Contains(t, links, "first line second line")
	assert.Contains(t, links, "chip-status-flagged")
	assert.Contains(t, links, "memorable · ")
	assert.Contains(t, links, linkedID(f).String())
	assert.NotContains(t, links, hostile)
}

func TestDetailLinkToQuarantinedHidesItsTitle(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		other := m.fetched[0].Links[0].To
		m.byID[other] = memory.Fetched{Ref: other.String(), Memory: memory.Memory{ID: other, Title: "secret title", Status: memory.StatusQuarantined}, Err: memory.ErrQuarantined}
	})

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	links := linksSection(t, rec.Body.String())
	assert.Contains(t, links, ">Quarantined memory</a>")
	assert.Contains(t, links, "quarantined, content hidden")
	assert.NotContains(t, links, "secret title")
}

func TestDetailLinkFallsBackToIDWhenUnavailable(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*detailMemories, uuid.UUID){
		"not found": nil,
		"get failed": func(m *detailMemories, other uuid.UUID) {
			m.getErrByID = map[uuid.UUID]error{other: errors.New("boom")}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
				if mutate != nil {
					mutate(m, m.fetched[0].Links[0].To)
				}
			})

			rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

			require.Equal(t, http.StatusOK, rec.Code)
			links := linksSection(t, rec.Body.String())
			assert.Contains(t, links, `aria-describedby="link-peek-0">`+linkedID(f).String()+"</a>")
			assert.Contains(t, links, "Not found or unavailable.")
		})
	}
}

func TestLinkedIDsDedupesAndBatches(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	others := make([]uuid.UUID, memory.MaxBatch+1)
	links := make([]memory.Link, 0, len(others)+1)
	for i := range others {
		others[i] = uuid.New()
		links = append(links, memory.Link{From: id, To: others[i], Kind: memory.LinkRelated})
	}
	links = append(links, memory.Link{From: others[0], To: id, Kind: memory.LinkSupersedes})

	assert.Equal(t, others, linkedIDs(id, links))

	mem := &detailMemories{byID: map[uuid.UUID]memory.Fetched{}}
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	s.linkedMemories(r, requestContext{Backend: Backend{Memories: mem}}, others)

	gets := mem.lastGets()
	require.Len(t, gets, 2)
	assert.Len(t, gets[0].IDs, memory.MaxBatch)
	assert.Len(t, gets[1].IDs, 1)
}

func TestSnippetOf(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "a b c", snippetOf("  a\n\tb   c \n"))
	long := strings.Repeat("é", linkSnippetRunes+5)
	assert.Equal(t, strings.Repeat("é", linkSnippetRunes)+previewEllipsis, snippetOf(long))
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

func TestEditURLRedirectKeepsListFilter(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	for _, tc := range []struct {
		name, query, want string
	}{
		{name: "filter kept", query: "?q=port+map&type=project&tag=go&cursor=" + uuid.NewString(), want: f.path("?q=port+map&tag=go&type=project")},
		{name: "malformed filter dropped", query: "?type=bogus&q=x", want: f.path("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := send(f.h, http.MethodGet, f.path("/edit"+tc.query), "", withCookie(f.c))

			assert.Equal(t, http.StatusSeeOther, rec.Code)
			assert.Equal(t, tc.want, rec.Header().Get("Location"))
		})
	}
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

// editorFields is the editor form plus the rail, whose fields submit with it through form="memory-editor".
func editorFields(t *testing.T, body string) string {
	t.Helper()
	header := regexp.MustCompile(`(?s)<header class="detail-header.*?</header>`).FindString(body)
	require.NotEmpty(t, header, "detail header")
	return header + editorForm(t, body) + detailRail(t, body)
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
	assert.Regexp(t, `name="title" value="Port map"[^>]*form="memory-editor"`, body)
	assert.Contains(t, editor, "line one\n&lt;img src=x onerror=alert(1)&gt;</textarea>")
	fields := editorFields(t, body)
	assert.Contains(t, fields, `name="tags" value="go, ops"`)
	assert.Contains(t, fields, `<option value="project" selected>`)
	assert.Regexp(t, `<input type="date" name="expires_at" value="2026-12-31"`, fields)
	assert.Contains(t, fields, `name="expires_at_original" value="2026-12-31T00:00:00Z"`)
	assert.Regexp(t, `<button type="button"[^>]*data-clear-expiry`, fields)
	assert.Regexp(t, `name="pinned"[^>]*checked`, fields)
	assert.Contains(t, editor, `name="version" value="2"`)
	assert.Contains(t, editor, `name="csrf" value="`+f.s.session.csrf+`"`)
	assert.Regexp(t, `<span[^>]*data-dirty-marker[^>]*hidden`, body)
	assert.NotContains(t, editor[1:], "<form", "no form is nested in the editor")
	assert.Regexp(t, `<button[^>]*form="memory-editor"[^>]*data-save`, body)
	assert.Regexp(t, `<a href="`+regexp.QuoteMeta(f.path(""))+`"[^>]*data-discard>Discard</a>`, body)
	assert.Contains(t, fields, "(UTC, empty means never)")
}

func TestDetailTabsRenderAllPanelsWithoutJS(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `<div role="tablist" aria-label="Memory sections" data-tabs`)
	for _, tab := range []string{"content", "history", "links", "activity"} {
		assert.Regexp(t, `<a role="tab" href="#`+tab+`" id="tab-`+tab+`" aria-controls="`+tab+`"`, body, tab)
		assert.Regexp(t, `<section id="`+tab+`" role="tabpanel" aria-labelledby="tab-`+tab+`"[^>]*data-tab-panel>`, body, tab)
	}
	assert.Regexp(t, `id="tab-history"[^>]*>History <span class="tab-count">2</span>`, body)
	assert.Regexp(t, `id="tab-links"[^>]*>Links <span class="tab-count">1</span>`, body)
	assert.NotContains(t, body, "data-section-fold")
	assert.NotContains(t, body, "data-section-nav")
	assert.NotRegexp(t, `data-tab-panel[^>]*hidden`, body)
	content := regexp.MustCompile(`(?s)<section id="content" role="tabpanel".*?</section>`).FindString(body)
	assert.Contains(t, content, "data-primary-actions", "Save and the menu belong to the Content tab")
	script, err := os.ReadFile("static/app.js")
	require.NoError(t, err)
	assert.Contains(t, string(script), `var TABS = ['content', 'history', 'links', 'activity'];`)
	assert.Contains(t, string(script), `versions: 'history'`)
	assert.Contains(t, string(script), "'beforematch'")
	assert.NotContains(t, string(script), "COLLAPSED_KEY")
	assert.NotContains(t, string(script), "data-section-fold")
}

func TestPristineEditorMatchesItsOriginals(t *testing.T) {
	t.Parallel()
	expires := time.Date(2026, 12, 31, 15, 30, 0, 0, time.UTC)
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.fetched[0].Memory.ExpiresAt = &expires
		m.fetched[0].Memory.Pinned = true
	})

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	editor := editorFields(t, rec.Body.String())
	assert.NotContains(t, editor, "data-unsaved")
	for _, name := range []string{"title", "tags", "expires_at"} {
		input := regexp.MustCompile(`<input[^>]*name="` + name + `"[^>]*>`).FindString(editor)
		value := regexp.MustCompile(`\svalue="([^"]*)"`).FindStringSubmatch(input)
		original := regexp.MustCompile(`data-original="([^"]*)"`).FindStringSubmatch(input)
		require.Len(t, value, 2, name)
		require.Len(t, original, 2, name)
		assert.Equal(t, original[1], value[1], name)
	}
	assert.Regexp(t, `name="expires_at" value="2026-12-31" data-original="2026-12-31"`, editor)
	content := regexp.MustCompile(`(?s)<textarea[^>]*name="content"[^>]*data-original="([^"]*)"[^>]*>\n(.*?)</textarea>`).FindStringSubmatch(editor)
	require.Len(t, content, 3)
	assert.Equal(t, content[1], content[2])
	assert.Regexp(t, `<select name="type" data-original="project"`, editor)
	assert.Contains(t, editor, `<option value="project" selected>`)
	assert.Regexp(t, `name="pinned"[^>]*data-original="on"[^>]*checked`, editor)
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
			editor := editorFields(t, body)
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
	assert.Equal(t, newTimeView(t3), entries[0].When)
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

	more := regexp.MustCompile(`(?s)<details class="more-actions[^>]*>.*?</details>`).FindString(body)
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
	adminMore := regexp.MustCompile(`(?s)<details class="more-actions[^>]*>.*?</details>`).FindString(page)
	adminDeleteAt := strings.Index(adminMore, `action="/c/personal/memories/`+m.ID.String()+`/delete"`)
	purgeAt := strings.Index(adminMore, `action="/c/personal/memories/`+m.ID.String()+`/purge"`)
	assert.Positive(t, adminDeleteAt)
	require.Greater(t, purgeAt, adminDeleteAt, "Purge comes after Delete in the menu")
	assert.Contains(t, adminMore[purgeAt:], `name="confirm"`)
	assert.Equal(t, 1, strings.Count(page, "/purge\""), "Purge only in the menu")
}

func detailRail(t *testing.T, body string) string {
	t.Helper()
	rail := regexp.MustCompile(`(?s)<aside class="detail-rail[^"]*" aria-label="Properties">.*?</aside>`).FindString(body)
	require.NotEmpty(t, rail, "properties rail")
	return rail
}

func TestRailFieldsBelongToEditor(t *testing.T) {
	t.Parallel()
	slug := "port-map"
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { m.fetched[0].Memory.Slug = &slug })

	body := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c), withHeader("HX-Request", "true")).Body.String()

	rail := detailRail(t, body)
	for _, name := range []string{"tags", "type", "expires_at", "expires_at_original", "pinned"} {
		assert.Regexp(t, `<(input|select)[^>]*name="`+name+`"[^>]*form="memory-editor"`, rail, name)
	}
	assert.Regexp(t, `data-tag-entry[^>]*form="memory-editor"|form="memory-editor"[^>]*data-tag-entry`, rail)
	assert.Contains(t, rail, "(global)")
	assert.Contains(t, rail, "port-map")
	assert.Contains(t, rail, `<time datetime="2026-11-02T09:00:00Z"`)
	assert.NotContains(t, rail, "data-section-fold")
	script, err := os.ReadFile("static/app.js")
	require.NoError(t, err)
	assert.NotContains(t, string(script), "form.querySelectorAll('[data-original]')", "dirty tracking must see fields outside the form element")
	assert.Contains(t, string(script), "form.elements")
}

func TestHeaderShowsOnlyTypeAndStatus(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	body := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c), withHeader("HX-Request", "true")).Body.String()

	assert.NotContains(t, body, "Editing version")
	more := regexp.MustCompile(`(?s)<details class="more-actions[^>]*>.*?</details>`).FindString(body)
	info := regexp.MustCompile(`(?s)<p data-memory-info[^>]*>.*?</p>`).FindString(more)
	assert.Contains(t, info, "Version 2")
	assert.Contains(t, info, "Embedded with nomic-embed-text")
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

func TestDetailDatesShowDateWithTimeOnHover(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `<time datetime="2026-11-02T09:00:00Z" title="2026-11-02 09:00 UTC">2026-11-02</time>`)
	assert.Contains(t, body, `<time datetime="2026-11-01T09:00:00Z" title="2026-11-01 09:00 UTC">2026-11-01</time>`)
	assert.Contains(t, body, `<time datetime="2026-11-03T09:00:00Z" title="2026-11-03 09:00 UTC">2026-11-03</time>`)
	assert.NotContains(t, body, "2026-11-02 09:00 UTC<")
}
