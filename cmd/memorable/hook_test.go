package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/memory/memorytest"
	"github.com/constantine-kostikas/memorable/internal/scan"
	"github.com/constantine-kostikas/memorable/internal/scope"
)

func fakeApp(t *testing.T) *app {
	t.Helper()
	repo := memorytest.NewRepo()
	sec, err := scan.NewSecretScanner()
	require.NoError(t, err)
	cfg := config.Default()
	svc := memory.NewService(repo, repo, memorytest.NewEmbedder(), sec, scan.Injection, memory.Settings{
		Owner: "dev", Scope: scope.Resolution{Context: "personal"}, Search: cfg.Search, Preload: cfg.Preload,
		WritesPerMinute: 60,
	})
	slug := "style/spelling"
	_, err = svc.Write(context.Background(), []memory.NewMemory{
		{Title: "Spelling", Content: "Use US spelling everywhere.", Type: memory.TypeUser, Source: memory.SourceUserStated, Slug: &slug, Pinned: true},
	})
	require.NoError(t, err)
	return &app{cfg: &cfg, svc: svc}
}

func runRoot(t *testing.T, d deps, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmdWith(buildInfo{Version: "test"}, d)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestHook_PrintsPreload(t *testing.T) {
	t.Parallel()
	d := defaultDeps()
	var gotDirs []string
	d.build = func(_ context.Context, _ string, dirs []string, _ *slog.Logger) (*app, error) {
		gotDirs = dirs
		return fakeApp(t), nil
	}
	out, err := runRoot(t, d, `{"session_id":"s1","cwd":"/home/x/projects/demo","hook_event_name":"SessionStart"}`, "hook", "session-start")
	require.NoError(t, err)
	assert.Equal(t, []string{"/home/x/projects/demo"}, gotDirs)
	assert.Contains(t, out, "Relevant memories (memorable). Fetch full content with memory_get:")
	assert.Contains(t, out, "- [style/spelling] Spelling: Use US spelling everywhere.")
}

func TestHook_SilentOnError(t *testing.T) {
	t.Parallel()
	d := defaultDeps()
	d.build = func(context.Context, string, []string, *slog.Logger) (*app, error) {
		return nil, errors.New("database down")
	}
	out, err := runRoot(t, d, `{"cwd":"/x"}`, "hook", "session-start")
	require.NoError(t, err)
	assert.Empty(t, out)

	out, err = runRoot(t, d, `not json`, "hook", "session-start")
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestHook_BinaryExitsZero(t *testing.T) {
	t.Parallel()
	env, project := isolatedEnv(t)
	cmd := exec.Command(binPath, "hook", "session-start")
	cmd.Env = env
	cmd.Stdin = strings.NewReader(`{"cwd":"` + project + `"}`)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	require.NoError(t, cmd.Run())
	assert.Empty(t, stdout.String())
}

func TestContextAdd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := dir + "/config.toml"
	target := t.TempDir()
	out, err := runRoot(t, defaultDeps(), "", "--config", cfgPath, "context", "add", target, "work")
	require.NoError(t, err, out)
	cfg, err := config.Load(cfgPath, os.Getenv)
	require.NoError(t, err)
	assert.Equal(t, []string{target}, cfg.Contexts["work"].Paths)

	_, err = runRoot(t, defaultDeps(), "", "--config", cfgPath, "context", "add", target, "Work")
	require.Error(t, err)
	_, err = runRoot(t, defaultDeps(), "", "--config", cfgPath, "context", "add", dir+"/missing", "work")
	require.Error(t, err)
}

func TestContextShow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := dir + "/config.toml"
	target := t.TempDir()
	_, err := runRoot(t, defaultDeps(), "", "--config", cfgPath, "context", "add", target, "personal")
	require.NoError(t, err)
	d := defaultDeps()
	d.cwd = func() (string, error) { return target, nil }
	out, err := runRoot(t, d, "", "--config", cfgPath, "context", "--output", "json")
	require.NoError(t, err)
	var v contextView
	require.NoError(t, json.Unmarshal([]byte(out), &v))
	assert.Equal(t, "personal", v.Context)
	assert.Equal(t, "mem_personal", v.Schema)
	assert.False(t, v.ReadOnly)
}
