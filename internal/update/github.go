package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the GitHub REST API. Release assets are fetched through it, never from browser download URLs.
const DefaultBaseURL = "https://api.github.com"

// DefaultRepo is the repository that publishes memorable releases.
const DefaultRepo = "Constantine-Kostikas/memorable"

const (
	maxRedirects = 3
	maxJSONBytes = 4 << 20
)

// Release is the subset of a GitHub release that update needs.
type Release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Asset is a file attached to a release. URL is its API URL, not the browser download URL.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// GitHub reads releases from the GitHub REST API.
type GitHub struct {
	BaseURL   string
	Repo      string
	Token     string
	UserAgent string
	HTTP      *http.Client
}

// RedirectAllowed reports whether an asset download may follow a redirect to u: HTTPS to api.github.com or
// a *.githubusercontent.com host, where GitHub serves release files from short-lived signed URLs.
func RedirectAllowed(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	return host == "api.github.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

// NewHTTPClient returns a client with connection timeouts that follows at most three redirects, each of which
// allow must accept. A nil transport uses a hardened clone of the default transport.
//
// net/http drops the Authorization header when a redirect leaves the original host, so the token never reaches
// the signed download host.
func NewHTTPClient(transport http.RoundTripper, allow func(*url.URL) bool) *http.Client {
	if transport == nil {
		t := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
		if def, ok := http.DefaultTransport.(*http.Transport); ok {
			t = def.Clone()
		}
		t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		t.TLSHandshakeTimeout = 10 * time.Second
		t.ResponseHeaderTimeout = 30 * time.Second
		transport = t
	}
	return &http.Client{
		Transport: transport,
		Timeout:   5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("too many redirects") //nolint:err113 // shown to the user
			}
			if !allow(req.URL) {
				return fmt.Errorf("refusing redirect to %s://%s", req.URL.Scheme, req.URL.Host)
			}
			return nil
		},
	}
}

// Releases lists the newest 100 releases, newest first, including pre-releases and drafts visible to the token.
func (g *GitHub) Releases(ctx context.Context) ([]Release, error) {
	var rels []Release
	if err := g.getJSON(ctx, "/repos/"+g.Repo+"/releases?per_page=100", &rels); err != nil {
		return nil, err
	}
	return rels, nil
}

// ReleaseByTag fetches one release by its tag.
func (g *GitHub) ReleaseByTag(ctx context.Context, tag string) (Release, error) {
	var rel Release
	if err := g.getJSON(ctx, "/repos/"+g.Repo+"/releases/tags/"+url.PathEscape(tag), &rel); err != nil {
		return Release{}, err
	}
	return rel, nil
}

// Download fetches an asset through the API, refusing anything larger than maxBytes.
func (g *GitHub) Download(ctx context.Context, a Asset, maxBytes int64) ([]byte, error) {
	return g.DownloadTo(ctx, a, maxBytes, io.Discard)
}

// DownloadTo is Download that also writes each received chunk to progress as it arrives.
func (g *GitHub) DownloadTo(ctx context.Context, a Asset, maxBytes int64, progress io.Writer) ([]byte, error) {
	prefix := g.BaseURL + "/repos/" + g.Repo + "/releases/assets/"
	if !strings.HasPrefix(a.URL, prefix) {
		return nil, fmt.Errorf("%s: %q is not a release asset of %s", a.Name, a.URL, g.Repo)
	}
	body, err := g.get(ctx, a.URL, "application/octet-stream", maxBytes, progress)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", a.Name, err)
	}
	return body, nil
}

func (g *GitHub) getJSON(ctx context.Context, path string, v any) error {
	body, err := g.get(ctx, g.BaseURL+path, "application/vnd.github+json", maxJSONBytes, io.Discard)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decoding GitHub response for %s: %w", path, err)
	}
	return nil
}

func (g *GitHub) get(ctx context.Context, rawURL, accept string, maxBytes int64, progress io.Writer) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return nil, fmt.Errorf("refusing %q: only https is allowed", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.UserAgent != "" {
		req.Header.Set("User-Agent", g.UserAgent)
	}
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, g.statusError(resp)
	}
	body, err := io.ReadAll(io.TeeReader(io.LimitReader(resp.Body, maxBytes+1), progress))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", u.Redacted(), err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response from %s is over the %d byte limit", u.Redacted(), maxBytes)
	}
	return body, nil
}

func (g *GitHub) statusError(resp *http.Response) error {
	msg := fmt.Sprintf("GitHub returned %s for %s", resp.Status, resp.Request.URL.Path)
	switch {
	case resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0":
		msg += ": API rate limit reached; set GH_TOKEN or try again later"
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		if g.Token == "" {
			msg += ": check the repository name, or set GH_TOKEN if GitHub requires a token"
		} else {
			msg += ": check that the token can read " + g.Repo
		}
	}
	return errors.New(msg) //nolint:err113 // shown to the user
}
