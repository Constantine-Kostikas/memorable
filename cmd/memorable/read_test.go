package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func fakeDeps(t *testing.T) deps {
	t.Helper()
	d := defaultDeps()
	a := fakeApp(t)
	d.build = func(context.Context, string, []string, *slog.Logger) (*app, error) { return a, nil }
	d.cwd = func() (string, error) { return "/home/x/projects/demo", nil }
	return d
}

func TestSearchCmd_JSON(t *testing.T) {
	t.Parallel()
	out, err := runRoot(t, fakeDeps(t), "", "search", "spelling", "-o", "json")
	require.NoError(t, err, out)
	var res searchView
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	require.Len(t, res.Results, 1)
	assert.Equal(t, "Spelling", res.Results[0].Title)
	assert.Equal(t, "user", res.Results[0].Type)
	assert.NotEmpty(t, res.SearchID)
}

func TestSearchCmd_Text(t *testing.T) {
	t.Parallel()
	out, err := runRoot(t, fakeDeps(t), "", "search", "spelling")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Spelling")
	assert.NotContains(t, out, "\x1b[")
}

func TestGetCmd(t *testing.T) {
	t.Parallel()
	out, err := runRoot(t, fakeDeps(t), "", "get", "style/spelling", "-o", "json")
	require.NoError(t, err, out)
	var got []memoryView
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.Len(t, got, 1)
	assert.Equal(t, "Use US spelling everywhere.", got[0].Content)

	out, err = runRoot(t, fakeDeps(t), "", "get", "style/spelling")
	require.NoError(t, err)
	assert.Contains(t, out, "Use US spelling everywhere.")
}

func TestListAndTopCmd(t *testing.T) {
	t.Parallel()
	out, err := runRoot(t, fakeDeps(t), "", "list")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Spelling")
	out, err = runRoot(t, fakeDeps(t), "", "top", "-o", "json")
	require.NoError(t, err, out)
	var items []summaryView
	require.NoError(t, json.Unmarshal([]byte(out), &items))
	assert.Len(t, items, 1)
}

func TestExitCodes(t *testing.T) {
	t.Parallel()
	_, err := runRoot(t, defaultDeps(), "", "--nope")
	assert.Equal(t, 2, exitCode(err))
	_, err = runRoot(t, defaultDeps(), "", "bogus-command")
	assert.Equal(t, 2, exitCode(err))
	_, err = runRoot(t, fakeDeps(t), "", "search")
	assert.Equal(t, 2, exitCode(err), "missing argument is a usage error")
	_, err = runRoot(t, defaultDeps(), "", "version", "-o", "yaml")
	assert.Equal(t, 2, exitCode(err))
	assert.Equal(t, 0, exitCode(nil))
}

func TestListCmd_AnyTag(t *testing.T) {
	t.Parallel()
	a := fakeApp(t)
	for _, m := range []struct{ title, tag string }{{"Alpha note", "a"}, {"Beta note", "b"}, {"Gamma note", "c"}} {
		_, err := a.svc.Write(context.Background(), []memory.NewMemory{
			{Title: m.title, Content: "content " + m.tag, Type: memory.TypeProject, Source: memory.SourceUserStated, Tags: []string{m.tag}, OnSimilar: memory.OnSimilarKeepBoth},
		})
		require.NoError(t, err)
	}
	d := defaultDeps()
	d.build = func(context.Context, string, []string, *slog.Logger) (*app, error) { return a, nil }
	d.cwd = func() (string, error) { return "/home/x/projects/demo", nil }

	out, err := runRoot(t, d, "", "list", "--any-tag", "a", "--any-tag", "b")

	require.NoError(t, err, out)
	assert.Contains(t, out, "Alpha note")
	assert.Contains(t, out, "Beta note")
	assert.NotContains(t, out, "Gamma note")
}

func TestTagsCmd(t *testing.T) {
	t.Parallel()
	a := fakeApp(t)
	for _, m := range []struct {
		title string
		tags  []string
	}{{"Alpha note", []string{"go", "ci"}}, {"Beta note", []string{"go"}}} {
		_, err := a.svc.Write(context.Background(), []memory.NewMemory{
			{Title: m.title, Content: "content " + m.title, Type: memory.TypeProject, Source: memory.SourceUserStated, Tags: m.tags, OnSimilar: memory.OnSimilarKeepBoth},
		})
		require.NoError(t, err)
	}
	d := defaultDeps()
	d.build = func(context.Context, string, []string, *slog.Logger) (*app, error) { return a, nil }
	d.cwd = func() (string, error) { return "/home/x/projects/demo", nil }

	out, err := runRoot(t, d, "", "tags", "-o", "json")
	require.NoError(t, err, out)
	assert.JSONEq(t, `[{"tag":"go","count":2},{"tag":"ci","count":1}]`, out)

	out, err = runRoot(t, d, "", "tags")
	require.NoError(t, err, out)
	assert.Contains(t, out, "go\t2")
	assert.Contains(t, out, "ci\t1")
}
