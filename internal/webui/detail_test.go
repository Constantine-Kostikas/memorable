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
	assert.Contains(t, body, `<pre class="whitespace-pre-wrap break-words">line one`)
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
	assert.Contains(t, rec.Body.String(), `id="memory-detail"`)
}

func TestEditFormCarriesVersion(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path("/edit"), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `name="version" value="2"`)
	assert.Contains(t, body, `name="csrf" value="`+f.s.session.csrf+`"`)
	assert.Contains(t, body, "go, ops")
	assert.NotContains(t, body, hostile)
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
	assert.NotContains(t, edit.Body.String(), `name="content"`)
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
		ResponseHandling       []struct {
			Code  string `json:"code"`
			Swap  bool   `json:"swap"`
			Error bool   `json:"error"`
		} `json:"responseHandling"`
	}
	require.NoError(t, json.Unmarshal([]byte(m[1]), &cfg))
	assert.False(t, cfg.IncludeIndicatorStyles)
	assert.False(t, cfg.AllowEval)
	swapped := func(status string) (swap, isErr bool) {
		for _, rule := range cfg.ResponseHandling {
			if regexp.MustCompile("^(?:" + rule.Code + ")$").MatchString(status) {
				return rule.Swap, rule.Error
			}
		}
		return false, false
	}
	for _, status := range []string{"404", "409", "422", "429", "503"} {
		swap, isErr := swapped(status)
		assert.True(t, swap && isErr, status)
	}
	for _, status := range []string{"403", "500"} {
		swap, isErr := swapped(status)
		assert.False(t, swap, status)
		assert.True(t, isErr, status)
	}
	swap, _ := swapped("200")
	assert.True(t, swap)
}
