package webui

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
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

func oobRow(t *testing.T, body string, id uuid.UUID) string {
	t.Helper()
	return regexp.MustCompile(`(?s)<li id="row-` + id.String() + `" hx-swap-oob="outerHTML"[^>]*>.*?</li>`).FindString(body)
}

func TestPaneSaveRefreshesListRowOutOfBand(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/update?q=port&type=project", updateForm(nil), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	row := oobRow(t, rec.Body.String(), f.id)
	require.NotEmpty(t, row, rec.Body.String())
	assert.Contains(t, row, "New title")
	assert.Contains(t, row, "type-tab-feedback")
	assert.Contains(t, row, `href="`+f.path("?q=port&amp;type=project")+`"`, "the row keeps the list filter")
	assert.Contains(t, row, `aria-current="true"`)
	assert.Contains(t, row, "data-row-link")
}

func TestPaneActionsRefreshListRowOutOfBand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		action string
		form   url.Values
		want   string
	}{
		{action: "/restore", form: url.Values{}, want: "Port map"},
		{action: "/activate", form: url.Values{}, want: "Port map"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			t.Parallel()
			f := newDetailFixture(t, nil)

			rec := f.post(tc.action, tc.form, withHeader("HX-Request", "true"))

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			row := oobRow(t, rec.Body.String(), f.id)
			require.NotEmpty(t, row, rec.Body.String())
			assert.Contains(t, row, tc.want)
		})
	}
}

func TestPaneSupersedeRefreshesListRowOutOfBand(t *testing.T) {
	t.Parallel()
	replacement := uuid.New()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.byID[replacement] = memory.Fetched{Ref: replacement.String(), Memory: memory.Memory{ID: replacement, Scope: memory.ScopeGlobal}}
	})

	rec := f.post("/supersede", url.Values{"replacement": {replacement.String()}}, withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotEmpty(t, oobRow(t, rec.Body.String(), f.id))
}

func TestPaneActionWithoutRowRefresh(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mut  func(*detailMemories, uuid.UUID)
		opts []requestOption
	}{
		{name: "full page rebuilds the list", opts: nil},
		{name: "failed save", mut: func(m *detailMemories, _ uuid.UUID) { m.mutErr = fmt.Errorf("update: %w", memory.ErrConflict) },
			opts: []requestOption{withHeader("HX-Request", "true")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDetailFixture(t, tc.mut)

			rec := f.post("/update", updateForm(nil), tc.opts...)

			assert.NotContains(t, rec.Body.String(), "hx-swap-oob=\"outerHTML\"")
		})
	}
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

func withNextMemory(next uuid.UUID) func(*detailMemories, uuid.UUID) {
	return func(m *detailMemories, _ uuid.UUID) {
		m.byID[next] = memory.Fetched{Ref: next.String(), Memory: memory.Memory{
			ID: next, Title: "Next card", Content: "next body", Type: memory.TypeReference, Status: memory.StatusActive,
			Scope: memory.ScopeGlobal, Version: 1,
		}}
	}
}

func deletedRowRemoval(id uuid.UUID) string {
	return `<li id="row-` + id.String() + `" hx-swap-oob="delete"></li>`
}

func TestDeleteAction(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/delete", url.Values{})

	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, []any{f.id}, f.mem.called("Delete")[0].Args)
}

func TestPaneDeleteOpensNextMemory(t *testing.T) {
	t.Parallel()
	next := uuid.New()
	f := newDetailFixture(t, withNextMemory(next))

	rec := f.post("/delete?type=project", url.Values{"next": {next.String()}}, withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.True(t, strings.HasPrefix(body, `<section id="detail-pane" data-memory-href="/c/personal/memories/`+next.String()+`"`), body)
	assert.Contains(t, body, `value="Next card"`)
	assert.Contains(t, body, deletedRowRemoval(f.id))
	assert.Regexp(t, `(?s)hx-swap-oob="beforeend:#toasts".*Memory deleted.`, body)
	assert.Equal(t, "/c/personal/memories/"+next.String()+"?type=project", rec.Header().Get("HX-Push-Url"))
	assert.Contains(t, body, `action="/c/personal/memories/`+next.String()+`/update?type=project"`, "the next pane keeps the list filter")
}

func TestPaneDeleteKeepsTheFilterShownInThePage(t *testing.T) {
	t.Parallel()
	next := uuid.New()
	for name, tc := range map[string]struct {
		form url.Values
		want string
	}{
		"next memory": {form: url.Values{"next": {next.String()}}, want: "/c/personal/memories/" + next.String() + "?q=port&status=flagged&type=project"},
		"empty pane":  {form: url.Values{}, want: "/c/personal/memories?q=port&status=flagged&type=project"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newDetailFixture(t, withNextMemory(next))
			page := "http://" + testHost + "/c/personal/memories?q=port&type=project&status=flagged"

			rec := f.post("/delete", tc.form, withHeader("HX-Request", "true"), withHeader("HX-Current-URL", page))

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tc.want, rec.Header().Get("HX-Push-Url"), "the filter set after the pane loaded wins over the form's")
		})
	}
}

func TestPaneActionLinksFollowThePageFilter(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)
	page := "http://" + testHost + f.path("?type=project&q=port")

	rec := f.post("/feedback?type=user", url.Values{"kind": {"useful"}}, withHeader("HX-Request", "true"), withHeader("HX-Current-URL", page))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `action="`+f.path("/delete?q=port&amp;type=project")+`"`)
}

func TestPaneIgnoresCurrentURLOutsideTheContextList(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/delete?type=project", url.Values{}, withHeader("HX-Request", "true"),
		withHeader("HX-Current-URL", "http://"+testHost+"/c/personal/review?type=user"))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/c/personal/memories?type=project", rec.Header().Get("HX-Push-Url"))
}

func TestPaneDeleteWithoutNextShowsEmptyPane(t *testing.T) {
	t.Parallel()
	for name, form := range map[string]url.Values{
		"no next":        {},
		"malformed next": {"next": {"not-a-uuid"}},
		"missing next":   {"next": {uuid.NewString()}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newDetailFixture(t, nil)
			form := maps.Clone(form)

			rec := f.post("/delete?type=project", form, withHeader("HX-Request", "true"))

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			body := rec.Body.String()
			assert.True(t, strings.HasPrefix(body, `<section id="detail-pane" aria-label="Memory"`), body)
			assert.Contains(t, body, "Select a memory")
			assert.Contains(t, body, deletedRowRemoval(f.id))
			assert.Contains(t, body, "Memory deleted.")
			assert.Equal(t, "/c/personal/memories?type=project", rec.Header().Get("HX-Push-Url"))
		})
	}
}

func TestPaneDeleteFormAsksForTheNeighbor(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Regexp(t, `<form[^>]*action="`+f.path("/delete")+`"[^>]*data-delete-memory>`, rec.Body.String())
	script, err := os.ReadFile("static/app.js")
	require.NoError(t, err)
	assert.Contains(t, string(script), "hasAttribute('data-delete-memory')", "app.js adds next to this form's request")
}

func TestDeleteWithoutHtmxRedirects(t *testing.T) {
	t.Parallel()
	next := uuid.New()
	for name, tc := range map[string]struct {
		form url.Values
		want string
	}{
		"to the next memory": {form: url.Values{"next": {next.String()}}, want: "/c/personal/memories/" + next.String() + "?type=project"},
		"to the list":        {form: url.Values{}, want: "/c/personal/memories?type=project"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newDetailFixture(t, withNextMemory(next))

			rec := f.post("/delete?type=project", tc.form)

			require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
			assert.Equal(t, tc.want, rec.Header().Get("Location"))
		})
	}
}

func TestFailedPaneDeleteKeepsTheMemory(t *testing.T) {
	t.Parallel()
	next := uuid.New()
	f := newDetailFixture(t, func(m *detailMemories, id uuid.UUID) {
		withNextMemory(next)(m, id)
		m.mutErr = fmt.Errorf("%w: profile", memory.ErrReadOnly)
	})

	rec := f.post("/delete", url.Values{"next": {next.String()}}, withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	body := rec.Body.String()
	assert.True(t, strings.HasPrefix(body, `<section id="detail-pane" data-memory-href="`+f.path("")+`"`), body)
	assert.NotContains(t, body, `hx-swap-oob="delete"`)
	assert.Empty(t, rec.Header().Get("HX-Push-Url"))
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
	body := send(f.h, http.MethodGet, f.path(""), "", withCookie(f.c)).Body.String()
	rendered := regexp.MustCompile(`name="expires_at" value="([^"]*)"`).FindStringSubmatch(body)
	original := regexp.MustCompile(`name="expires_at_original" value="([^"]*)"`).FindStringSubmatch(body)
	require.Len(t, rendered, 2)
	require.Len(t, original, 2)

	rec := f.post("/update", updateForm(url.Values{"expires_at": {rendered[1]}, "expires_at_original": {original[1]}}))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	patch := f.mem.called("Update")[0].Args[2].(memory.Patch)
	assert.Nil(t, patch.ExpiresAt, "an untouched date keeps the stored expiry, time included")
}

func TestExpiryUnchangedKeepsTime(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/update", updateForm(url.Values{"expires_at": {"2026-12-01"}, "expires_at_original": {"2026-12-01T15:30:00Z"}}))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	patch := f.mem.called("Update")[0].Args[2].(memory.Patch)
	assert.Nil(t, patch.ExpiresAt)
}

func TestExpiryChangedUsesDate(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/update", updateForm(url.Values{"expires_at": {"2026-12-05"}, "expires_at_original": {"2026-12-01T15:30:00Z"}}))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	patch := f.mem.called("Update")[0].Args[2].(memory.Patch)
	require.NotNil(t, patch.ExpiresAt)
	require.NotNil(t, *patch.ExpiresAt)
	assert.True(t, time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC).Equal(**patch.ExpiresAt))
}

func TestExpiryClearedWithEmpty(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	rec := f.post("/update", updateForm(url.Values{"expires_at": {""}, "expires_at_original": {"2026-12-01T15:30:00Z"}}))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	patch := f.mem.called("Update")[0].Args[2].(memory.Patch)
	require.NotNil(t, patch.ExpiresAt)
	assert.Nil(t, *patch.ExpiresAt)
}

func TestFailedSaveKeepsExpiryOriginal(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) {
		m.mutErr = fmt.Errorf("update: %w", memory.ErrUnavailable)
		m.fetched, m.getErr = nil, fmt.Errorf("get: %w", memory.ErrUnavailable)
	})

	rec := f.post("/update", updateForm(url.Values{"expires_at": {"2026-12-01"}, "expires_at_original": {"2026-12-01T15:30:00Z"}}),
		withHeader("HX-Request", "true"))

	editor := editorForm(t, rec.Body.String())
	assert.Contains(t, editor, `name="expires_at" value="2026-12-01"`)
	assert.Contains(t, editor, `name="expires_at_original" value="2026-12-01T15:30:00Z"`)
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

func TestFailedSaveRendersDirtyWithStoredBaseline(t *testing.T) {
	t.Parallel()
	for name, mutErr := range map[string]error{
		"conflict": fmt.Errorf("update: %w", memory.ErrConflict),
		"secret":   fmt.Errorf("%w: github-pat", memory.ErrSecretDetected),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { m.mutErr = mutErr })

			rec := f.post("/update", updateForm(url.Values{"title": {"Mine"}, "content": {"typed text"}, "tags": {"mine"}}),
				withHeader("HX-Request", "true"))

			editor := editorForm(t, rec.Body.String())
			assert.Regexp(t, `<form id="memory-editor"[^>]*data-unsaved`, editor)
			assert.Regexp(t, `name="title" value="Mine"[^>]*data-original="Port map"`, editor)
			assert.Regexp(t, `name="tags" value="mine"[^>]*data-original="go, ops"`, editor)
			assert.Regexp(t, `<textarea[^>]*data-original="line one\n&lt;img src=x onerror=alert\(1\)&gt;"`, editor)
			assert.Regexp(t, `<select name="type"[^>]*data-original="project"`, editor)
			assert.NotContains(t, rec.Body.String(), hostile)
		})
	}
}

func TestDiscardIsAReloadLink(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, nil)

	body := send(f.h, http.MethodGet, f.path("?q=go"), "", withCookie(f.c)).Body.String()

	assert.Regexp(t, `<a href="`+regexp.QuoteMeta(f.path("?q=go"))+`"[^>]*data-discard`, body)
	assert.NotContains(t, body, `type="reset"`)
	assert.NotContains(t, editorForm(t, body), "data-unsaved")
}

func TestConflictHeaderNamesTheEditedVersion(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, _ uuid.UUID) { m.mutErr = fmt.Errorf("update: %w", memory.ErrConflict) })

	rec := f.post("/update", updateForm(url.Values{"version": {"1"}}), withHeader("HX-Request", "true"))

	assert.Contains(t, rec.Body.String(), "Your edit is based on version 1")
}

func TestFailedSaveOnQuarantinedReloadKeepsErrorAndText(t *testing.T) {
	t.Parallel()
	f := newDetailFixture(t, func(m *detailMemories, id uuid.UUID) {
		m.mutErr = fmt.Errorf("update: %w", memory.ErrQuarantined)
		m.fetched = []memory.Fetched{{Ref: id.String(), Memory: memory.Memory{ID: id, Status: memory.StatusQuarantined}, Err: memory.ErrQuarantined}}
	})

	rec := f.post("/update", updateForm(url.Values{"content": {"typed " + hostile}}), withHeader("HX-Request", "true"))

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "This memory is quarantined")
	assert.Regexp(t, `role="alert"`, body)
	assert.Regexp(t, `<textarea[^>]*readonly[^>]*>\ntyped &lt;img src=x onerror=alert\(1\)&gt;</textarea>`, body)
	assert.NotContains(t, body, hostile)
}
