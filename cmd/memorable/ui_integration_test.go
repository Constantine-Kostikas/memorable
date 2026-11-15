//go:build integration

package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/store/storetest"
)

// uiIntegrationHome bootstraps a database with the personal context, which is also the fallback
// context, and returns the HOME and XDG_CONFIG_HOME to use, plus a directory no context maps.
func uiIntegrationHome(t *testing.T) (home, configHome, unmatched string, cfg config.Config) {
	t.Helper()
	ctx := context.Background()
	db := storetest.NewDatabase(t)
	home = t.TempDir()
	unmatched = filepath.Join(home, "elsewhere")
	require.NoError(t, os.MkdirAll(unmatched, 0o755))
	cfg = config.Default()
	cfg.Database = storetest.DatabaseConfig(db)
	cfg.Contexts = map[string]config.Context{"personal": {Paths: []string{filepath.Join(home, "projects")}}}
	cfg.FallbackContext = "personal"
	cfg.Embedding.URL = "http://127.0.0.1:1"
	require.NoError(t, storetest.Bootstrap(ctx, t, db, &cfg))
	configHome = filepath.Join(home, "config")
	cfgDir := filepath.Join(configHome, "memorable")
	require.NoError(t, config.Save(filepath.Join(cfgDir, "config.toml"), cfg))
	creds := fmt.Sprintf(`{"memorable_app@%s:%d/%s": %q}`, cfg.Database.Host, cfg.Database.Port, cfg.Database.Name, storetest.AppPassword)
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "credentials.json"), []byte(creds), 0o600))
	return home, configHome, unmatched, cfg
}

func TestUI_ServesAndStopsOnSIGTERM(t *testing.T) {
	t.Parallel()
	home, configHome, unmatched, _ := uiIntegrationHome(t)
	cmd := exec.Command(binPath, "ui", "--no-browser")
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+configHome, "MEMORABLE_KEYRING=off", "MEMORABLE_CONFIG=")
	cmd.Dir = unmatched
	stderr, err := cmd.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	launch := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if m := launchURLPattern.FindString(sc.Text()); m != "" {
				launch <- m
			}
		}
	}()
	var url string
	select {
	case url = <-launch:
	case <-time.After(10 * time.Second):
		t.Fatal("no launch URL")
	}

	client := uiClient(t)
	status, _ := get(t, client, url)
	require.Equal(t, http.StatusSeeOther, status)
	status, body := get(t, client, baseURL(url)+"/c/personal/memories")
	assert.Equal(t, http.StatusOK, status, body)
	status, body = get(t, client, baseURL(url)+"/c/personal/stats")
	assert.Equal(t, http.StatusOK, status, body)
	client.CloseIdleConnections()

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.NoError(t, err, "ui exits cleanly on SIGTERM")
	case <-time.After(10 * time.Second):
		t.Fatal("ui kept running after SIGTERM")
	}
}

//nolint:paralleltest // t.Setenv points the credential store at the test home
func TestBuildAppFor_FallbackContextIsReadWrite(t *testing.T) {
	home, configHome, _, _ := uiIntegrationHome(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("MEMORABLE_KEYRING", "off")
	ctx := context.Background()
	d := defaultDeps()
	cfgPath := filepath.Join(configHome, "memorable", "config.toml")

	a, err := buildAppFor(ctx, d, cfgPath, "personal", slog.New(slog.DiscardHandler), map[string]any{"via": "ui"})
	require.NoError(t, err)
	t.Cleanup(a.Close)
	assert.False(t, a.res.ReadOnly)
	assert.Nil(t, a.res.Project)

	written, err := a.svc.Write(ctx, []memory.NewMemory{
		{Title: "UI write", Content: "Written through the UI app.", Type: memory.TypeUser, Source: memory.SourceUserStated},
	})
	require.NoError(t, err)
	require.Len(t, written, 1)
	require.NotNil(t, written[0].Memory, written[0].Reason)
	activity, err := a.svc.Activity(ctx, written[0].Memory.ID)
	require.NoError(t, err)
	require.NotEmpty(t, activity.Audit)
	entry := activity.Audit[len(activity.Audit)-1]
	assert.True(t, strings.HasPrefix(entry.Process, fmt.Sprintf("ui:%d@", os.Getpid())), entry.Process)
	assert.Equal(t, "ui", entry.Details["via"])

	_, err = buildAppFor(ctx, d, cfgPath, "missing", slog.New(slog.DiscardHandler), nil)
	require.ErrorIs(t, err, memory.ErrNoContext)
}

func TestOpenUIAdmin(t *testing.T) {
	t.Parallel()
	_, _, _, cfg := uiIntegrationHome(t)
	ctx := context.Background()
	schema := config.SchemaName("personal")

	admin, err := openUIAdmin(ctx, cfg.Database, storetest.AdminPassword, schema, "dev")
	require.NoError(t, err)
	admin.Close()

	const wrong = "wrong-SENTINEL-password"
	_, err = openUIAdmin(ctx, cfg.Database, wrong, schema, "dev")
	require.ErrorIs(t, err, errAdminPasswordRejected)
	assert.NotContains(t, err.Error(), wrong)

	opener := newUIAdminOpener(&cfg, storetest.AdminPassword, "dev")
	ops, err := opener(ctx, "personal")
	require.NoError(t, err)
	_, err = ops.QuarantinedList(ctx)
	require.NoError(t, err)
	ops.Close()

	_, err = opener(ctx, "missing")
	require.ErrorIs(t, err, memory.ErrNoContext)
}
