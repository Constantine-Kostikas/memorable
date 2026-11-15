package webui

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/store"
)

const (
	testPort = 8123
	testHost = "127.0.0.1:8123"
	wantCSP  = "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	restart  = "restart memorable ui to get a new link"
)

type fakeMemories struct{ Memories }

func (fakeMemories) Review(context.Context) (memory.ReviewQueue, error) {
	return memory.ReviewQueue{}, nil
}

type fakeStore struct{}

func (fakeStore) Stats(context.Context) (store.Stats, error) { return store.Stats{}, nil }

func (fakeStore) ProfileList(context.Context) ([]memory.Memory, error) { return nil, nil }

type backendRecorder struct {
	mu     sync.Mutex
	names  []string
	broken string
}

func (b *backendRecorder) open(_ context.Context, name string) (Backend, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.names = append(b.names, name)
	if name == b.broken {
		return Backend{}, errors.New("connection refused")
	}
	return Backend{Memories: fakeMemories{}, Store: fakeStore{}}, nil
}

func (b *backendRecorder) opened() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.names...)
}

func testConfig(b *backendRecorder) Config {
	return Config{
		Contexts: []string{"personal", "work"},
		Initial:  "personal",
		Backend:  b.open,
		Logger:   slog.New(slog.DiscardHandler),
	}
}

func newTestServer(t *testing.T) (*Server, *backendRecorder) {
	t.Helper()
	b := &backendRecorder{}
	s, err := New(testConfig(b))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	return s, b
}

// testHandler adds test-only routes: a POST write route and a GET context probe.
func testHandler(s *Server) http.Handler {
	return s.handler(testPort, func(mux *http.ServeMux) {
		mux.HandleFunc("POST /c/{context}/test-write", s.inContext(
			func(w http.ResponseWriter, _ *http.Request, rc requestContext) {
				_, _ = io.WriteString(w, "wrote "+rc.Name)
			}))
		mux.HandleFunc("GET /c/{context}/probe", s.inContext(
			func(w http.ResponseWriter, _ *http.Request, rc requestContext) {
				_, _ = io.WriteString(w, "probe "+rc.Name)
			}))
	})
}

type requestOption func(*http.Request)

func withCookie(c *http.Cookie) requestOption {
	return func(r *http.Request) { r.AddCookie(c) }
}

func withHeader(name, value string) requestOption {
	return func(r *http.Request) { r.Header.Set(name, value) }
}

func withHost(host string) requestOption {
	return func(r *http.Request) { r.Host = host }
}

func send(h http.Handler, method, target, body string, opts ...requestOption) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = testHost
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func launchToken(t *testing.T, s *Server) string {
	t.Helper()
	u, err := url.Parse(s.LaunchURL(testPort))
	require.NoError(t, err)
	return u.Query().Get("token")
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	raw := rec.Header().Get("Set-Cookie")
	require.NotEmpty(t, raw, "Set-Cookie")
	c, err := http.ParseSetCookie(raw)
	require.NoError(t, err)
	return c
}

func login(t *testing.T, s *Server, h http.Handler) *http.Cookie {
	t.Helper()
	rec := send(h, http.MethodGet, "/?token="+launchToken(t, s), "")
	require.Equal(t, http.StatusSeeOther, rec.Code)
	return sessionCookie(t, rec)
}

func TestTokenLaunchURL(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)

	assert.True(t, strings.HasPrefix(s.LaunchURL(testPort), "http://127.0.0.1:8123/?token="))
	raw, err := base64.RawURLEncoding.DecodeString(launchToken(t, s))
	require.NoError(t, err)
	assert.Len(t, raw, 32)

	other, _ := newTestServer(t)
	assert.NotEqual(t, launchToken(t, s), launchToken(t, other))
}

func TestTokenExchange(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	token := launchToken(t, s)

	first := send(h, http.MethodGet, "/?token="+token, "")
	require.Equal(t, http.StatusSeeOther, first.Code)
	assert.Equal(t, "/c/personal/memories", first.Header().Get("Location"))
	cookie := sessionCookie(t, first)
	assert.Equal(t, "memorable_session", cookie.Name)
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	assert.Equal(t, "/", cookie.Path)
	value, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	require.NoError(t, err)
	assert.Len(t, value, 32)
	assert.NotEqual(t, token, cookie.Value)

	replay := send(h, http.MethodGet, "/?token="+token, "")
	assert.Equal(t, http.StatusForbidden, replay.Code)
	assert.Contains(t, replay.Body.String(), restart)
	assert.Empty(t, replay.Header().Get("Set-Cookie"))

	reopen := send(h, http.MethodGet, "/?token="+token, "", withCookie(cookie))
	assert.Equal(t, http.StatusSeeOther, reopen.Code)
	assert.Equal(t, "/c/personal/memories", reopen.Header().Get("Location"))
	assert.Empty(t, reopen.Header().Get("Set-Cookie"))

	bare := send(h, http.MethodGet, "/", "", withCookie(cookie))
	assert.Equal(t, http.StatusSeeOther, bare.Code)
	assert.Equal(t, "/c/personal/memories", bare.Header().Get("Location"))
}

func TestTokenExchangeRejectsWrongToken(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	token := launchToken(t, s)

	for _, target := range []string{"/", "/?token=", "/?token=wrong", "/?token=" + token + "x", "/?token=" + token[1:]} {
		rec := send(h, http.MethodGet, target, "")
		assert.Equal(t, http.StatusForbidden, rec.Code, target)
		assert.Contains(t, rec.Body.String(), restart, target)
		assert.Empty(t, rec.Header().Get("Set-Cookie"), target)
	}

	assert.Equal(t, http.StatusSeeOther, send(h, http.MethodGet, "/?token="+token, "").Code,
		"failed attempts must not consume the token")
}

func TestTokenExchangeIsSingleUseUnderConcurrency(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	target := "/?token=" + launchToken(t, s)

	const attempts = 50
	codes := make(chan int, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Go(func() { codes <- send(h, http.MethodGet, target, "").Code })
	}
	wg.Wait()
	close(codes)

	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	assert.Equal(t, map[int]int{http.StatusSeeOther: 1, http.StatusForbidden: attempts - 1}, counts)
}

func TestRejectsBadHost(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	cookie := login(t, s, h)

	for _, host := range []string{"evil.example:8123", "127.0.0.1:9999", "127.0.0.1", "localhost", "0.0.0.0:8123", "[::1]:8123"} {
		rec := send(h, http.MethodGet, "/c/personal/probe", "", withHost(host), withCookie(cookie))
		assert.Equal(t, http.StatusMisdirectedRequest, rec.Code, host)
		assert.NotContains(t, rec.Body.String(), "probe", host)
	}
	for _, host := range []string{"127.0.0.1:8123", "localhost:8123"} {
		rec := send(h, http.MethodGet, "/c/personal/probe", "", withHost(host), withCookie(cookie))
		assert.Equal(t, http.StatusOK, rec.Code, host)
	}
}

func TestRejectsBadHostBeforeTokenExchange(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	token := launchToken(t, s)

	rec := send(h, http.MethodGet, "/?token="+token, "", withHost("evil.example:8123"))
	assert.Equal(t, http.StatusMisdirectedRequest, rec.Code)
	assert.Empty(t, rec.Header().Get("Set-Cookie"))
	assert.Equal(t, http.StatusSeeOther, send(h, http.MethodGet, "/?token="+token, "").Code)
}

func TestRejectsMissingSession(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	cookie := login(t, s, h)

	missing := send(h, http.MethodGet, "/c/personal/probe", "")
	assert.Equal(t, http.StatusForbidden, missing.Code)
	assert.Contains(t, missing.Body.String(), restart)
	assert.NotContains(t, missing.Body.String(), s.session.csrf)
	assert.NotContains(t, missing.Body.String(), "probe personal")

	forged := &http.Cookie{Name: cookie.Name, Value: cookie.Value[:len(cookie.Value)-1] + "A"}
	if forged.Value == cookie.Value {
		forged.Value = cookie.Value[:len(cookie.Value)-1] + "B"
	}
	assert.Equal(t, http.StatusForbidden, send(h, http.MethodGet, "/c/personal/probe", "", withCookie(forged)).Code)

	unknownPath := send(h, http.MethodGet, "/c/personal/nothing-here", "")
	assert.Equal(t, http.StatusForbidden, unknownPath.Code)

	ok := send(h, http.MethodGet, "/c/personal/probe", "", withCookie(cookie))
	assert.Equal(t, http.StatusOK, ok.Code)
	assert.Equal(t, "probe personal", ok.Body.String())
}

func TestRejectsPlantedCookieShadowingSession(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	cookie := login(t, s, h)

	planted := &http.Cookie{Name: cookie.Name, Value: "planted"}
	rec := send(h, http.MethodGet, "/c/personal/probe", "", withCookie(planted), withCookie(cookie))
	assert.Equal(t, http.StatusOK, rec.Code)

	onlyPlanted := send(h, http.MethodGet, "/c/personal/probe", "", withCookie(planted))
	assert.Equal(t, http.StatusForbidden, onlyPlanted.Code)
}

func TestRejectsSessionAfterClose(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	cookie := login(t, s, h)

	s.Close()
	s.Close()

	assert.Equal(t, http.StatusForbidden, send(h, http.MethodGet, "/c/personal/probe", "", withCookie(cookie)).Code)
}

func TestRejectsUncleanStaticPath(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)

	for _, target := range []string{"/static/../c/personal/probe", "/static/./../c/personal/probe", "/static//../c/personal/probe"} {
		rec := send(h, http.MethodGet, target, "")
		assert.NotEqual(t, http.StatusOK, rec.Code, target)
		assert.NotContains(t, rec.Body.String(), "probe personal", target)
	}
}

func TestStaticServedWithoutSession(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)

	css := send(h, http.MethodGet, "/static/app.css", "")
	assert.Equal(t, http.StatusOK, css.Code)
	assert.Contains(t, css.Header().Get("Content-Type"), "text/css")

	js := send(h, http.MethodGet, "/static/htmx.min.js", "")
	assert.Equal(t, http.StatusOK, js.Code)

	assert.Equal(t, http.StatusNotFound, send(h, http.MethodGet, "/static/missing.css", "").Code)
	assert.Equal(t, http.StatusForbidden, send(h, http.MethodPost, "/static/app.css", "").Code)
}

func validPost(s *Server, cookie *http.Cookie) []requestOption {
	return []requestOption{
		withCookie(cookie),
		withHeader("X-CSRF-Token", s.session.csrf),
		withHeader("Sec-Fetch-Site", "same-origin"),
	}
}

func TestAcceptsValidPost(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	cookie := login(t, s, h)

	header := send(h, http.MethodPost, "/c/work/test-write", "", validPost(s, cookie)...)
	assert.Equal(t, http.StatusOK, header.Code)
	assert.Equal(t, "wrote work", header.Body.String())

	form := send(h, http.MethodPost, "/c/personal/test-write", "csrf="+url.QueryEscape(s.session.csrf),
		withCookie(cookie),
		withHeader("Content-Type", "application/x-www-form-urlencoded"),
		withHeader("Origin", "http://127.0.0.1:8123"))
	assert.Equal(t, http.StatusOK, form.Code)
	assert.Equal(t, "wrote personal", form.Body.String())
}

func TestRejectsCrossOriginPost(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	cookie := login(t, s, h)

	cases := map[string][]requestOption{
		"cross-site fetch metadata": {withHeader("Origin", "http://evil.example"), withHeader("Sec-Fetch-Site", "cross-site")},
		"foreign origin only":       {withHeader("Origin", "http://evil.example")},
		"same-site other port":      {withHeader("Sec-Fetch-Site", "same-site")},
	}
	for name, extra := range cases {
		opts := append([]requestOption{withCookie(cookie), withHeader("X-CSRF-Token", s.session.csrf)}, extra...)
		rec := send(h, http.MethodPost, "/c/personal/test-write", "", opts...)
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.NotContains(t, rec.Body.String(), "wrote", name)
	}
}

func TestRejectsMissingCSRF(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	cookie := login(t, s, h)
	sameOrigin := withHeader("Sec-Fetch-Site", "same-origin")
	form := withHeader("Content-Type", "application/x-www-form-urlencoded")

	cases := map[string]*httptest.ResponseRecorder{
		"none":         send(h, http.MethodPost, "/c/personal/test-write", "", withCookie(cookie), sameOrigin),
		"empty header": send(h, http.MethodPost, "/c/personal/test-write", "", withCookie(cookie), sameOrigin, withHeader("X-CSRF-Token", "")),
		"wrong header": send(h, http.MethodPost, "/c/personal/test-write", "", withCookie(cookie), sameOrigin, withHeader("X-CSRF-Token", "wrong")),
		"wrong field":  send(h, http.MethodPost, "/c/personal/test-write", "csrf=wrong", withCookie(cookie), sameOrigin, form),
		"query only": send(h, http.MethodPost, "/c/personal/test-write?csrf="+url.QueryEscape(s.session.csrf), "",
			withCookie(cookie), sameOrigin, form),
		"cookie value as token": send(h, http.MethodPost, "/c/personal/test-write", "", withCookie(cookie), sameOrigin,
			withHeader("X-CSRF-Token", cookie.Value)),
	}
	for name, rec := range cases {
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.NotContains(t, rec.Body.String(), "wrote", name)
	}
}

func TestRejectsOversizedPostBody(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	cookie := login(t, s, h)

	body := "csrf=" + url.QueryEscape(s.session.csrf) + "&pad=" + strings.Repeat("a", 2<<20)
	rec := send(h, http.MethodPost, "/c/personal/test-write", body,
		withCookie(cookie),
		withHeader("Sec-Fetch-Site", "same-origin"),
		withHeader("Content-Type", "application/x-www-form-urlencoded"))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.NotContains(t, rec.Body.String(), "wrote")
}

func TestRejectsGetOnWriteRoute(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	cookie := login(t, s, h)

	rec := send(h, http.MethodGet, "/c/personal/test-write", "", withCookie(cookie))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.NotContains(t, rec.Body.String(), "wrote")
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	s, _ := newTestServer(t)
	h := testHandler(s)
	token := launchToken(t, s)

	responses := map[string]*httptest.ResponseRecorder{}
	responses["token exchange 303"] = send(h, http.MethodGet, "/?token="+token, "")
	cookie := sessionCookie(t, responses["token exchange 303"])
	responses["missing session 403"] = send(h, http.MethodGet, "/c/personal/probe", "")
	responses["bad host 421"] = send(h, http.MethodGet, "/", "", withHost("evil.example:8123"))
	responses["unknown path 404"] = send(h, http.MethodGet, "/nothing", "", withCookie(cookie))
	responses["unknown context 404"] = send(h, http.MethodGet, "/c/nope/probe", "", withCookie(cookie))
	responses["wrong method 405"] = send(h, http.MethodGet, "/c/personal/test-write", "", withCookie(cookie))
	responses["missing csrf 403"] = send(h, http.MethodPost, "/c/personal/test-write", "", withCookie(cookie))
	responses["cross origin 403"] = send(h, http.MethodPost, "/c/personal/test-write", "",
		withCookie(cookie), withHeader("X-CSRF-Token", s.session.csrf), withHeader("Sec-Fetch-Site", "cross-site"))
	responses["static 200"] = send(h, http.MethodGet, "/static/app.css", "")
	responses["probe 200"] = send(h, http.MethodGet, "/c/personal/probe", "", withCookie(cookie))

	wantCodes := map[string]int{
		"token exchange 303":  http.StatusSeeOther,
		"missing session 403": http.StatusForbidden,
		"bad host 421":        http.StatusMisdirectedRequest,
		"unknown path 404":    http.StatusNotFound,
		"unknown context 404": http.StatusNotFound,
		"wrong method 405":    http.StatusMethodNotAllowed,
		"missing csrf 403":    http.StatusForbidden,
		"cross origin 403":    http.StatusForbidden,
		"static 200":          http.StatusOK,
		"probe 200":           http.StatusOK,
	}
	for name, rec := range responses {
		assert.Equal(t, wantCodes[name], rec.Code, name)
		assert.Equal(t, wantCSP, rec.Header().Get("Content-Security-Policy"), name)
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), name)
		assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"), name)
	}
}

func TestUnknownContext404(t *testing.T) {
	t.Parallel()
	s, backends := newTestServer(t)
	h := testHandler(s)
	cookie := login(t, s, h)

	unknown := send(h, http.MethodGet, "/c/nope/probe", "", withCookie(cookie))
	assert.Equal(t, http.StatusNotFound, unknown.Code)
	assert.NotContains(t, unknown.Body.String(), "probe nope")

	assert.Equal(t, http.StatusNotFound, send(h, http.MethodPost, "/c/nope/test-write", "", validPost(s, cookie)...).Code)

	for _, name := range []string{"personal", "work", "personal"} {
		rec := send(h, http.MethodGet, "/c/"+name+"/probe", "", withCookie(cookie))
		assert.Equal(t, http.StatusOK, rec.Code, name)
		assert.Equal(t, "probe "+name, rec.Body.String())
	}
	assert.Equal(t, []string{"personal", "work", "personal"}, backends.opened(),
		"the backend is resolved from the path on every request, never for an unknown context")
}

func TestUnknownContextBrokenBackendIsIsolated(t *testing.T) {
	t.Parallel()
	b := &backendRecorder{broken: "work"}
	s, err := New(testConfig(b))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	h := testHandler(s)
	cookie := login(t, s, h)

	broken := send(h, http.MethodGet, "/c/work/probe", "", withCookie(cookie))
	assert.Equal(t, http.StatusServiceUnavailable, broken.Code)
	assert.NotContains(t, broken.Body.String(), "connection refused")

	assert.Equal(t, http.StatusOK, send(h, http.MethodGet, "/c/personal/probe", "", withCookie(cookie)).Code)
}

func TestNewValidatesConfig(t *testing.T) {
	t.Parallel()
	b := &backendRecorder{}

	cases := map[string]func(*Config){
		"no contexts":     func(c *Config) { c.Contexts = nil },
		"unknown initial": func(c *Config) { c.Initial = "elsewhere" },
		"nil backend":     func(c *Config) { c.Backend = nil },
	}
	for name, mutate := range cases {
		cfg := testConfig(b)
		mutate(&cfg)
		_, err := New(cfg)
		assert.Error(t, err, name)
	}
}
