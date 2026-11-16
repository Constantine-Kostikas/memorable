package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func updateForm(extra url.Values) url.Values {
	form := url.Values{
		"title": {"New title"}, "content": {"New content"}, "tags": {"go, ops ,"}, "type": {"feedback"},
		"pinned": {"on"}, "expires_at": {""}, "version": {"2"},
	}
	for k, v := range extra {
		form[k] = v
	}
	return form
}

func TestUpdateCallsService(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/update", updateForm(nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	calls := f.mem.called("Update")
	require.Len(t, calls, 1)
	assert.Equal(t, f.id, calls[0].Args[0])
	assert.Equal(t, 2, calls[0].Args[1])
	patch := calls[0].Args[2].(memory.Patch)
	assert.Equal(t, "New title", *patch.Title)
	assert.Equal(t, "New content", *patch.Content)
	assert.Equal(t, []string{"go", "ops"}, *patch.Tags)
	assert.Equal(t, memory.TypeFeedback, *patch.Type)
	assert.True(t, *patch.Pinned)
	require.NotNil(t, patch.ExpiresAt)
	assert.Nil(t, *patch.ExpiresAt, "empty expires_at clears")
	assert.Contains(t, rec.Body.String(), "Saved.")
}

func TestUpdateConflict(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { m.mutErr = fmt.Errorf("update: %w", memory.ErrConflict) })

	rec := f.post("/update", updateForm(url.Values{"content": {"my edit"}}))

	require.Equal(t, http.StatusConflict, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "This memory changed since you opened it.")
	assert.Contains(t, body, `href="`+f.path("")+`"`)
	assert.Contains(t, body, "my edit")
	assert.Contains(t, body, `name="csrf" value="`+f.s.session.csrf+`"`)
}

func TestUpdateSecretKeepsForm(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.mutErr = fmt.Errorf("%w: aws-access-key", memory.ErrSecretDetected)
	})

	rec := f.post("/update", updateForm(url.Values{"content": {"key " + hostile}}))

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Rejected: looks like a secret (aws-access-key).")
	assert.Contains(t, body, "key &lt;img src=x onerror=alert(1)&gt;")
	assert.NotContains(t, body, hostile)
	assert.Contains(t, body, `name="version" value="2"`)
}

func TestUpdateQuarantinedMessage(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, id uuid.UUID) {
		m.updated = memory.Memory{ID: id, Status: memory.StatusQuarantined}
		m.fetched = []memory.Fetched{{Ref: id.String(), Memory: memory.Memory{ID: id, Status: memory.StatusQuarantined}, Err: memory.ErrQuarantined}}
	})

	rec := f.post("/update", updateForm(nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Saved, but quarantined: it needs approval (memorable approve or ui --admin).")
	assert.NotContains(t, rec.Body.String(), "New content")
	assert.Empty(t, f.mem.called("Activity"))
}

func TestUpdateRequiresVersion(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/update", updateForm(url.Values{"version": {""}}))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Empty(t, f.mem.called("Update"))
}

func TestUpdateSetsExpiry(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/update", updateForm(url.Values{"expires_at": {"2026-12-31"}}))

	require.Equal(t, http.StatusOK, rec.Code)
	patch := f.mem.called("Update")[0].Args[2].(memory.Patch)
	require.NotNil(t, *patch.ExpiresAt)
	assert.Equal(t, "2026-12-31", (*patch.ExpiresAt).Format("2006-01-02"))
}

func TestFeedbackAction(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/feedback", url.Values{"kind": {"wrong"}, "note": {"stale"}})

	require.Equal(t, http.StatusOK, rec.Code)
	calls := f.mem.called("Feedback")
	require.Len(t, calls, 1)
	assert.Equal(t, []any{f.id, memory.FeedbackWrong, "stale"}, calls[0].Args)
	assert.Contains(t, rec.Body.String(), "Feedback recorded.")
}

func TestDeleteAction(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/delete", url.Values{})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []any{f.id}, f.mem.called("Delete")[0].Args)
}

func TestRestoreAction(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/restore", url.Values{"version": {"1"}})

	require.Equal(t, http.StatusOK, rec.Code)
	args := f.mem.called("Restore")[0].Args
	assert.Equal(t, f.id, args[0])
	require.NotNil(t, args[1])
	assert.Equal(t, 1, *args[1].(*int))

	rec = f.post("/restore", url.Values{})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, f.mem.called("Restore")[1].Args[1].(*int))
}

func TestActivateAction(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/activate", url.Values{})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []any{f.id}, f.mem.called("Activate")[0].Args)
}

func TestLinkAndUnlinkActions(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)
	to := uuid.New()

	f.mem.byID[to] = memory.Fetched{Ref: to.String(), Memory: memory.Memory{ID: to}}

	rec := f.post("/link", url.Values{"to": {to.String()}, "kind": {"contradicts"}})
	require.Equal(t, http.StatusOK, rec.Code)
	rec = f.post("/unlink", url.Values{"to": {to.String()}, "kind": {"contradicts"}})
	require.Equal(t, http.StatusOK, rec.Code)

	calls := f.mem.called("Link")
	require.Len(t, calls, 2)
	want := memory.Link{From: f.id, To: to, Kind: "contradicts"}
	assert.Equal(t, []any{want, false}, calls[0].Args)
	assert.Equal(t, []any{want, true}, calls[1].Args)
	assert.Empty(t, f.mem.lastGetsWithSlugs())
}

func (d *detailMemories) lastGetsWithSlugs() []memory.GetRequest {
	var out []memory.GetRequest
	for _, g := range d.lastGets() {
		if len(g.Slugs) > 0 {
			out = append(out, g)
		}
	}
	return out
}

func TestLinkBySlugResolvesPassively(t *testing.T) {
	t.Parallel()
	to := uuid.New()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.bySlug["port-map"] = memory.Fetched{Ref: "port-map", Memory: memory.Memory{ID: to}}
	})

	rec := f.post("/link", url.Values{"to": {"port-map"}, "kind": {"related"}})

	require.Equal(t, http.StatusOK, rec.Code)
	slugGets := f.mem.lastGetsWithSlugs()
	require.Len(t, slugGets, 1)
	assert.True(t, slugGets[0].Passive)
	assert.Equal(t, []string{"port-map"}, slugGets[0].Slugs)
	assert.Equal(t, memory.Link{From: f.id, To: to, Kind: "related"}, f.mem.called("Link")[0].Args[0])
}

func TestSupersedeBySlug(t *testing.T) {
	t.Parallel()
	replacement := uuid.New()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.bySlug["new-map"] = memory.Fetched{Ref: "new-map", Memory: memory.Memory{ID: replacement}}
	})

	rec := f.post("/supersede", url.Values{"replacement": {"new-map"}})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []any{f.id, replacement}, f.mem.called("Supersede")[0].Args)
	slugGets := f.mem.lastGetsWithSlugs()
	require.Len(t, slugGets, 1)
	assert.True(t, slugGets[0].Passive)
}

func TestSupersedeValidationComesFromService(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.mutErr = fmt.Errorf("%w: replacement is deleted", memory.ErrValidation)
	})

	replacement := uuid.New()
	f.mem.byID[replacement] = memory.Fetched{Memory: memory.Memory{ID: replacement}}

	rec := f.post("/supersede", url.Values{"replacement": {replacement.String()}})

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "replacement is deleted")
}

func TestUnknownSlugIsNotFound(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.bySlug["ghost"] = memory.Fetched{Ref: "ghost", Err: memory.ErrNotFound}
	})

	rec := f.post("/link", url.Values{"to": {"ghost"}, "kind": {"related"}})

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, f.mem.called("Link"))
}

func TestActionErrorsNeverLeakInternals(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { m.mutErr = fmt.Errorf("pq: password=hunter2 failed") })

	rec := f.post("/delete", url.Values{})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "hunter2")
}

func TestActionRequiresCSRF(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodPost, f.path("/delete"), "csrf=wrong", withCookie(f.c),
		withHeader("Content-Type", "application/x-www-form-urlencoded"))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, f.mem.called("Delete"))
}

func TestActionViaHtmxHeaderRendersFragment(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)
	form := url.Values{"kind": {"useful"}}

	rec := send(f.h, http.MethodPost, f.path("/feedback"), form.Encode(), withCookie(f.c),
		withHeader("Content-Type", "application/x-www-form-urlencoded"),
		withHeader("HX-Request", "true"), withHeader("X-CSRF-Token", f.s.session.csrf))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "<html")
	assert.True(t, strings.HasPrefix(rec.Body.String(), `<section id="detail-pane"`))
	assert.NotContains(t, rec.Body.String(), `id="memory-rows"`)
}

func TestActionWithoutHtmxRendersShell(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/feedback", url.Values{"kind": {"useful"}})

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "<html")
	assert.Contains(t, body, `id="memory-rows"`)
	assert.Contains(t, body, `id="detail-pane"`)
	assert.Contains(t, body, "Feedback recorded.")
}

func TestLinkToMissingIDIsNotFound(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/link", url.Values{"to": {uuid.NewString()}, "kind": {"related"}})

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, f.mem.called("Link"))
	for _, g := range f.mem.lastGets() {
		assert.True(t, g.Passive)
	}
}

func TestLinkToQuarantinedOrProfileIsRejected(t *testing.T) {
	t.Parallel()
	quarantined, profile := uuid.New(), uuid.New()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.byID[quarantined] = memory.Fetched{Memory: memory.Memory{ID: quarantined}, Err: memory.ErrQuarantined}
		m.byID[profile] = memory.Fetched{Memory: memory.Memory{ID: profile, Scope: memory.ScopeProfile}}
	})

	for _, target := range []uuid.UUID{quarantined, profile} {
		rec := f.post("/link", url.Values{"to": {target.String()}, "kind": {"related"}})
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, target)
	}
	assert.Empty(t, f.mem.called("Link"))
}

func TestSupersedeMissingReplacementIsNotFound(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/supersede", url.Values{"replacement": {uuid.NewString()}})

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, f.mem.called("Supersede"))
}

func TestReadOnlyIs422WithMessage(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { m.mutErr = fmt.Errorf("%w: profile", memory.ErrReadOnly) })

	rec := f.post("/delete", url.Values{})

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "This memory cannot be changed here.")
}

func TestUpdateConflictKeepsSubmittedValues(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { m.mutErr = fmt.Errorf("update: %w", memory.ErrConflict) })

	rec := f.post("/update", updateForm(url.Values{"title": {"Mine"}, "content": {"typed text"}, "version": {"1"}}),
		withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusConflict, rec.Code)
	body := rec.Body.String()
	assert.True(t, strings.HasPrefix(body, `<section id="detail-pane"`))
	editor := editorForm(t, body)
	assert.Contains(t, editor, `value="Mine"`)
	assert.Contains(t, editor, "typed text</textarea>")
	assert.Contains(t, editor, `name="version" value="1"`, "the stale version stays so a re-save conflicts again")
	assert.Contains(t, body, conflictMessage)
	assert.Contains(t, body, `<a href="#versions" class="underline">Load latest version</a>`)
}

func TestUpdateSecretKeepsSubmittedValues(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.mutErr = fmt.Errorf("%w: github-pat", memory.ErrSecretDetected)
	})

	rec := f.post("/update", updateForm(url.Values{"content": {"token here"}}), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "github-pat")
	assert.Contains(t, editorForm(t, body), "token here</textarea>")
}

func TestUpdateRoundTripsRenderedExpiry(t *testing.T) {
	t.Parallel()
	expires := time.Date(2026, 12, 31, 8, 30, 0, 0, time.UTC)
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { m.fetched[0].Memory.ExpiresAt = &expires })
	rendered := regexp.MustCompile(`name="expires_at" value="([^"]*)"`).
		FindStringSubmatch(send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c)).Body.String())
	require.Len(t, rendered, 2)

	rec := f.post("/update", updateForm(url.Values{"expires_at": {rendered[1]}}))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	patch := f.mem.called("Update")[0].Args[2].(memory.Patch)
	require.NotNil(t, *patch.ExpiresAt)
	assert.True(t, expires.Equal(**patch.ExpiresAt))
}

func TestNoticeIsOutOfBandToast(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/feedback", url.Values{"kind": {"useful"}}, withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.True(t, strings.HasPrefix(body, `<section id="detail-pane"`))
	oob := body[strings.Index(body, "</section>\n"):]
	assert.Contains(t, oob, `hx-swap-oob="beforeend:#toasts"`)
	assert.Contains(t, oob, "Feedback recorded.")
}

func TestActionErrorIsOutOfBandToast(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { m.mutErr = fmt.Errorf("%w: profile", memory.ErrReadOnly) })

	rec := f.post("/delete", url.Values{}, withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Regexp(t, `(?s)hx-swap-oob="beforeend:#toasts".*role="alert".*This memory cannot be changed here.`, rec.Body.String())
}

func TestUpdateKeepsSubmittedValuesWhenReloadFails(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.mutErr = fmt.Errorf("update: %w", memory.ErrUnavailable)
		m.fetched, m.getErr = nil, fmt.Errorf("get: %w", memory.ErrUnavailable)
	})

	rec := f.post("/update", updateForm(url.Values{"content": {"typed text"}}), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	body := rec.Body.String()
	assert.True(t, strings.HasPrefix(body, `<section id="detail-pane"`))
	assert.Contains(t, editorForm(t, body), "typed text</textarea>")
	assert.Contains(t, body, "Memory store unavailable")
}
