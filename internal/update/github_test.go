package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRepo = "owner/memorable"

func newTestGitHub(t *testing.T, h http.Handler, token string) (*GitHub, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	allowLocal := func(u *url.URL) bool { return u.Hostname() == "127.0.0.1" }
	return &GitHub{
		BaseURL:   srv.URL,
		Repo:      testRepo,
		Token:     token,
		UserAgent: "memorable/test",
		HTTP:      NewHTTPClient(srv.Client().Transport, allowLocal),
	}, srv
}

func TestRedirectAllowed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url  string
		want bool
	}{
		{url: "https://release-assets.githubusercontent.com/x", want: true},
		{url: "https://objects.githubusercontent.com/x", want: true},
		{url: "https://api.github.com/x", want: true},
		{url: "http://release-assets.githubusercontent.com/x", want: false},
		{url: "https://githubusercontent.com.evil.example/x", want: false},
		{url: "https://evilgithubusercontent.com/x", want: false},
		{url: "https://example.com/x", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(tt.url)
			require.NoError(t, err)
			assert.Equal(t, tt.want, RedirectAllowed(u))
		})
	}
}

func TestGitHub_Releases(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	var gotHeader http.Header
	gh, _ := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		if r.URL.Path != "/repos/"+testRepo+"/releases" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]Release{{Tag: "v0.2.0"}, {Tag: "v0.1.0-alpha.1", Prerelease: true}})
	}), "tok")

	rels, err := gh.Releases(t.Context())
	require.NoError(t, err)
	is.Equal([]string{"v0.2.0", "v0.1.0-alpha.1"}, []string{rels[0].Tag, rels[1].Tag})
	is.True(rels[1].Prerelease)
	is.Equal("Bearer tok", gotHeader.Get("Authorization"))
	is.Equal("application/vnd.github+json", gotHeader.Get("Accept"))
	is.Equal("memorable/test", gotHeader.Get("User-Agent"))
}

func TestGitHub_NoTokenSendsNoAuthorization(t *testing.T) {
	t.Parallel()
	var auth []string
	gh, _ := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Values("Authorization")
		_, _ = w.Write([]byte("[]"))
	}), "")
	_, err := gh.Releases(t.Context())
	require.NoError(t, err)
	assert.Empty(t, auth)
}

func TestGitHub_NotFoundWithoutTokenHintsAtToken(t *testing.T) {
	t.Parallel()
	gh, _ := newTestGitHub(t, http.NotFoundHandler(), "")
	_, err := gh.Releases(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GH_TOKEN")
}

func TestGitHub_ReleaseByTag(t *testing.T) {
	t.Parallel()
	gh, _ := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+testRepo+"/releases/tags/v0.1.0" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(Release{Tag: "v0.1.0"})
	}), "tok")
	rel, err := gh.ReleaseByTag(t.Context(), "v0.1.0")
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0", rel.Tag)

	_, err = gh.ReleaseByTag(t.Context(), "v9.9.9")
	require.Error(t, err)
}

func TestGitHub_RejectsPlainHTTP(t *testing.T) {
	t.Parallel()
	gh := &GitHub{BaseURL: "http://127.0.0.1:1", Repo: testRepo, HTTP: NewHTTPClient(nil, RedirectAllowed)}
	_, err := gh.Releases(t.Context())
	require.ErrorContains(t, err, "https")
}

func TestGitHub_Download(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+testRepo+"/releases/assets/1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "want octet-stream", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/signed/1", http.StatusFound)
	})
	mux.HandleFunc("/signed/1", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("payload")) })
	mux.HandleFunc("/repos/"+testRepo+"/releases/assets/2", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.invalid/x", http.StatusFound)
	})
	mux.HandleFunc("/repos/"+testRepo+"/releases/assets/3", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/repos/"+testRepo+"/releases/assets/3", http.StatusFound)
	})
	gh, srv := newTestGitHub(t, mux, "tok")
	asset := func(id string) Asset {
		return Asset{Name: id, URL: srv.URL + "/repos/" + testRepo + "/releases/assets/" + id}
	}

	t.Run("follows an allowed redirect", func(t *testing.T) {
		t.Parallel()
		got, err := gh.Download(t.Context(), asset("1"), 1024)
		require.NoError(t, err)
		assert.Equal(t, "payload", string(got))
	})
	t.Run("refuses a redirect to another host", func(t *testing.T) {
		t.Parallel()
		_, err := gh.Download(t.Context(), asset("2"), 1024)
		require.ErrorContains(t, err, "redirect")
	})
	t.Run("stops a redirect loop", func(t *testing.T) {
		t.Parallel()
		_, err := gh.Download(t.Context(), asset("3"), 1024)
		require.ErrorContains(t, err, "redirect")
	})
	t.Run("enforces the size cap", func(t *testing.T) {
		t.Parallel()
		_, err := gh.Download(t.Context(), asset("1"), 3)
		require.ErrorContains(t, err, "limit")
	})
	t.Run("refuses an asset outside the repository", func(t *testing.T) {
		t.Parallel()
		_, err := gh.Download(t.Context(), Asset{Name: "x", URL: "https://example.com/file"}, 1024)
		require.ErrorContains(t, err, "not a release asset")
	})
}
