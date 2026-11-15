package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const leakedDSN = "postgres://memorable_app:hunter2-SENTINEL@127.0.0.1:5432/memorable"

var launchURLPattern = regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/\?token=([A-Za-z0-9_-]+)`)

// uiConfigFile writes a config with the personal and work contexts and returns its path.
func uiConfigFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := fmt.Sprintf("[database]\nport = 1\n\n[contexts.personal]\npaths = [%q]\n\n[contexts.work]\npaths = [%q]\n",
		filepath.Join(dir, "personal"), filepath.Join(dir, "work"))
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o600))
	return path
}

type buildForCalls struct {
	mu    sync.Mutex
	names []string
}

func (c *buildForCalls) record(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.names = append(c.names, name)
}

func (c *buildForCalls) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, got := range c.names {
		if got == name {
			n++
		}
	}
	return n
}

// uiDeps returns deps whose personal context opens a fake app and whose work context fails to open.
func uiDeps(t *testing.T) (deps, *buildForCalls) {
	t.Helper()
	d := fakeDeps(t)
	d.getenv = func(string) string { return "" }
	d.adminPassword = func(*cobra.Command) (string, error) {
		t.Error("unexpected admin password prompt")
		return "", errors.New("unexpected prompt")
	}
	d.openBrowser = func(string) error { return nil }
	calls := &buildForCalls{}
	personal := fakeApp(t)
	d.buildFor = func(_ context.Context, _, name string, _ *slog.Logger, details map[string]any) (*app, error) {
		calls.record(name)
		assert.Equal(t, map[string]any{"via": "ui"}, details)
		if name == "work" {
			return nil, fmt.Errorf("opening %s: %w", leakedDSN, memory.ErrUnavailable)
		}
		return personal, nil
	}
	return d, calls
}

type uiRun struct {
	stderr *lockedBuffer
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func startUI(t *testing.T, d deps, args ...string) *uiRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := newRootCmdWith(buildInfo{Version: "test"}, d)
	r := &uiRun{stderr: &lockedBuffer{}, cancel: cancel, done: make(chan struct{})}
	cmd.SetOut(r.stderr)
	cmd.SetErr(r.stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	go func() {
		defer close(r.done)
		r.err = cmd.ExecuteContext(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("memorable ui did not stop")
		}
	})
	return r
}

func (r *uiRun) launchURL(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := launchURLPattern.FindString(r.stderr.String()); m != "" {
			return m
		}
		select {
		case <-r.done:
			t.Fatalf("memorable ui exited early: %v; output: %s", r.err, r.stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("no launch URL on stderr: %s", r.stderr.String())
	return ""
}

func (r *uiRun) stop(t *testing.T) error {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
		return r.err
	case <-time.After(5 * time.Second):
		t.Fatal("memorable ui did not stop within 5 seconds")
		return nil
	}
}

// uiClient keeps the session cookie and does not follow redirects.
func uiClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &http.Client{
		Jar:           jar,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func baseURL(launch string) string {
	return strings.SplitN(launch, "/?", 2)[0]
}

func TestUIRefusesAdminWithoutTTY(t *testing.T) {
	t.Parallel()
	d, calls := uiDeps(t)
	d.adminPassword = func(cmd *cobra.Command) (string, error) {
		return promptAdminPassword(cmd.InOrStdin(), cmd.ErrOrStderr(), false)
	}
	opened := false
	d.openBrowser = func(string) error {
		opened = true
		return nil
	}
	out, err := runRoot(t, d, "secret\n", "--config", uiConfigFile(t), "ui", "--admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal")
	assert.NotContains(t, out, "token=")
	assert.False(t, opened)
	assert.Zero(t, calls.count("personal"))
}

func TestUIUnknownContextIsUsageError(t *testing.T) {
	t.Parallel()
	d, _ := uiDeps(t)
	_, err := runRoot(t, d, "", "--config", uiConfigFile(t), "ui", "--admin", "--context", "nope")
	require.Error(t, err)
	assert.Equal(t, 2, exitCode(err))
	assert.Contains(t, err.Error(), "nope")
}

func TestUIBindsLoopbackOnly(t *testing.T) {
	t.Parallel()
	ln, err := listenLoopback(0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	addr, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1", addr.IP.String())
	assert.NotZero(t, addr.Port)

	port := addr.Port
	require.NoError(t, ln.Close())
	fixed, err := listenLoopback(port)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fixed.Close() })
	assert.Equal(t, fmt.Sprintf("127.0.0.1:%d", port), fixed.Addr().String())
}

func TestUIServesOnPrintedLoopbackURL(t *testing.T) {
	t.Parallel()
	d, _ := uiDeps(t)
	run := startUI(t, d, "--config", uiConfigFile(t), "ui", "--no-browser")
	launch := run.launchURL(t)
	status, _ := get(t, uiClient(t), launch)
	assert.Equal(t, http.StatusSeeOther, status)
	require.NoError(t, run.stop(t))
}

func TestOpenBrowserWith(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		goos    string
		want    string
		wantErr bool
	}{
		{name: "linux uses xdg-open", goos: "linux", want: "xdg-open"},
		{name: "macOS uses open", goos: "darwin", want: "open"},
		{name: "other systems are unsupported", goos: "windows", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotName string
			var gotArgs []string
			run := func(_ context.Context, name string, args ...string) ([]byte, error) {
				gotName, gotArgs = name, args
				return nil, nil
			}
			err := openBrowserWith(run, tt.goos)("/tmp/memorable-ui-1/launch.html")
			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, gotName)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, gotName)
			assert.Equal(t, []string{"/tmp/memorable-ui-1/launch.html"}, gotArgs)
		})
	}
}

func TestUILaunchFileNotInArgv(t *testing.T) {
	t.Parallel()
	d, _ := uiDeps(t)
	type opened struct {
		path    string
		mode    os.FileMode
		dirMode os.FileMode
		content string
	}
	got := make(chan opened, 1)
	d.openBrowser = func(path string) error {
		info, err := os.Stat(path)
		require.NoError(t, err)
		dirInfo, err := os.Stat(filepath.Dir(path))
		require.NoError(t, err)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		got <- opened{path: path, mode: info.Mode().Perm(), dirMode: dirInfo.Mode().Perm(), content: string(content)}
		return nil
	}
	run := startUI(t, d, "--config", uiConfigFile(t), "ui")
	launch := run.launchURL(t)
	token := launchURLPattern.FindStringSubmatch(launch)[2]

	var o opened
	select {
	case o = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("browser was not opened")
	}
	assert.NotContains(t, o.path, "token=")
	assert.NotContains(t, o.path, token)
	assert.Equal(t, "launch.html", filepath.Base(o.path))
	assert.Equal(t, os.FileMode(0o600), o.mode)
	assert.Equal(t, os.FileMode(0o700), o.dirMode)
	assert.Contains(t, o.content, `<meta http-equiv="refresh" content="0;url=`+html.EscapeString(launch)+`">`)

	require.NoError(t, run.stop(t))
	_, err := os.Stat(filepath.Dir(o.path))
	assert.ErrorIs(t, err, os.ErrNotExist, "launch directory removed on shutdown")
}

func TestUINoBrowserStillPrintsURL(t *testing.T) {
	t.Parallel()
	d, _ := uiDeps(t)
	d.openBrowser = func(string) error {
		t.Error("browser opened despite --no-browser")
		return nil
	}
	run := startUI(t, d, "--config", uiConfigFile(t), "ui", "--no-browser")
	assert.NotEmpty(t, run.launchURL(t))
	require.NoError(t, run.stop(t))
}

func TestUIContextFailureIsolated(t *testing.T) {
	t.Parallel()
	d, calls := uiDeps(t)
	run := startUI(t, d, "--config", uiConfigFile(t), "ui", "--no-browser", "--context", "work")
	launch := run.launchURL(t)
	client := uiClient(t)

	status, _ := get(t, client, launch)
	require.Equal(t, http.StatusSeeOther, status)

	status, body := get(t, client, baseURL(launch)+"/c/work/review")
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Contains(t, body, "other contexts still work")
	status, _ = get(t, client, baseURL(launch)+"/c/work/review")
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, 2, calls.count("work"), "a failed open is retried, not cached")

	status, body = get(t, client, baseURL(launch)+"/c/personal/review")
	assert.Equal(t, http.StatusOK, status, body)
	status, _ = get(t, client, baseURL(launch)+"/c/personal/review")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, 1, calls.count("personal"), "an opened backend is cached")

	require.NoError(t, run.stop(t))
	logs := run.stderr.String()
	assert.NotContains(t, logs, "hunter2-SENTINEL")
	assert.NotContains(t, logs, "postgres://")
}

func TestUIBackends(t *testing.T) {
	t.Parallel()
	personal, work := fakeApp(t), fakeApp(t)
	failWork := true
	opens := map[string]int{}
	b := newUIBackends([]string{"personal", "work"}, func(_ context.Context, name string) (*app, error) {
		opens[name]++
		switch {
		case name == "personal":
			return personal, nil
		case failWork:
			return nil, fmt.Errorf("opening %s: %w", leakedDSN, memory.ErrUnavailable)
		default:
			return work, nil
		}
	}, slog.New(slog.DiscardHandler))
	t.Cleanup(b.close)
	ctx := context.Background()

	_, err := b.get(ctx, "work")
	require.ErrorIs(t, err, memory.ErrUnavailable)
	assert.NotContains(t, err.Error(), "hunter2-SENTINEL")
	assert.NotContains(t, err.Error(), "postgres://")

	got, err := b.get(ctx, "personal")
	require.NoError(t, err)
	assert.Same(t, personal.svc, got.Memories)

	failWork = false
	got, err = b.get(ctx, "work")
	require.NoError(t, err)
	assert.Same(t, work.svc, got.Memories)

	got, err = b.get(ctx, "personal")
	require.NoError(t, err)
	assert.Same(t, personal.svc, got.Memories)
	assert.Equal(t, map[string]int{"personal": 1, "work": 2}, opens)

	_, err = b.get(ctx, "elsewhere")
	require.Error(t, err)
	assert.NotContains(t, opens, "elsewhere")
}

func TestRedactOpenError(t *testing.T) {
	t.Parallel()
	err := redactOpenError(fmt.Errorf("dial %s: %w", leakedDSN, memory.ErrUnavailable))
	require.ErrorIs(t, err, memory.ErrUnavailable)
	assert.NotContains(t, err.Error(), "hunter2-SENTINEL")

	err = redactOpenError(errors.New("cannot parse " + leakedDSN))
	assert.NotContains(t, err.Error(), "hunter2-SENTINEL")
	assert.NotContains(t, err.Error(), "postgres://")
}

//nolint:paralleltest // goleak needs no other test goroutines running
func TestUIShutdown(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	d, _ := uiDeps(t)
	opened := make(chan string, 1)
	d.openBrowser = func(path string) error {
		opened <- path
		return nil
	}
	run := startUI(t, d, "--config", uiConfigFile(t), "ui")
	launch := run.launchURL(t)
	var path string
	select {
	case path = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("browser was not opened")
	}

	client := uiClient(t)
	status, _ := get(t, client, launch)
	require.Equal(t, http.StatusSeeOther, status)
	status, _ = get(t, client, baseURL(launch)+"/c/personal/review")
	require.Equal(t, http.StatusOK, status)
	client.CloseIdleConnections()

	start := time.Now()
	require.NoError(t, run.stop(t))
	assert.Less(t, time.Since(start), 5*time.Second)
	_, err := os.Stat(filepath.Dir(path))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = http.Get(baseURL(launch) + "/") //nolint:noctx,bodyclose // the listener must be closed, so there is no body
	require.Error(t, err, "listener closed")
	goleak.VerifyNone(t, ignore)
}
