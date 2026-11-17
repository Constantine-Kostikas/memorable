package claudecfg

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUpsertClaudeMD(t *testing.T) {
	t.Parallel()
	appended := UpsertClaudeMD("# Mine\n\nRules.\n", "use memorable")
	assert.Equal(t, "# Mine\n\nRules.\n\n"+startMarker+"\nuse memorable\n"+endMarker+"\n", appended)
	assert.Equal(t, appended, UpsertClaudeMD(appended, "use memorable"), "idempotent")

	replaced := UpsertClaudeMD(appended, "use memorable v2")
	assert.Equal(t, 1, strings.Count(replaced, startMarker))
	assert.Contains(t, replaced, "use memorable v2")
	assert.NotContains(t, replaced, "use memorable\n"+endMarker)
	assert.True(t, strings.HasPrefix(replaced, "# Mine"))

	assert.Equal(t, startMarker+"\nx\n"+endMarker+"\n", UpsertClaudeMD("", "x"))
}

func TestMergePermissions(t *testing.T) {
	t.Parallel()
	in := []byte(`{
  "model": "opus",
  "permissions": {
    "allow": ["Bash(ls:*)", "mcp__memorable__memory_search"],
    "deny": []
  },
  "env": {"A": "1"}
}`)
	out, err := MergePermissions(in, []string{"mcp__memorable__memory_search", "mcp__memorable__memory_get"},
		[]string{"mcp__memorable__memory_delete"}, []string{"Bash(psql:*)"})
	require.NoError(t, err)
	s := string(out)
	assert.Equal(t, []any{"Bash(ls:*)", "mcp__memorable__memory_search", "mcp__memorable__memory_get"},
		gjson.Get(s, "permissions.allow").Value())
	assert.Equal(t, []any{"mcp__memorable__memory_delete"}, gjson.Get(s, "permissions.ask").Value())
	assert.Equal(t, []any{"Bash(psql:*)"}, gjson.Get(s, "permissions.deny").Value())
	assert.Equal(t, "opus", gjson.Get(s, "model").String())
	assert.Less(t, strings.Index(s, `"model"`), strings.Index(s, `"permissions"`), "key order preserved")
	assert.Less(t, strings.Index(s, `"permissions"`), strings.Index(s, `"env"`))

	again, err := MergePermissions(out, []string{"mcp__memorable__memory_get"}, nil, []string{"Bash(psql:*)"})
	require.NoError(t, err)
	assert.JSONEq(t, s, string(again), "idempotent")

	fresh, err := MergePermissions(nil, []string{"x"}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []any{"x"}, gjson.GetBytes(fresh, "permissions.allow").Value())

	_, err = MergePermissions([]byte("{not json"), nil, nil, nil)
	require.Error(t, err)
}

func TestAddSessionStartHook(t *testing.T) {
	t.Parallel()
	out, err := AddSessionStartHook([]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"x"}]}]}}`), "memorable hook session-start")
	require.NoError(t, err)
	assert.Equal(t, "memorable hook session-start", gjson.GetBytes(out, "hooks.SessionStart.0.hooks.0.command").String())
	assert.Equal(t, "command", gjson.GetBytes(out, "hooks.SessionStart.0.hooks.0.type").String())
	assert.Equal(t, "x", gjson.GetBytes(out, "hooks.PreToolUse.0.hooks.0.command").String())

	again, err := AddSessionStartHook(out, "memorable hook session-start")
	require.NoError(t, err)
	assert.JSONEq(t, string(out), string(again))
}

func TestRegisterMCP(t *testing.T) {
	t.Parallel()
	var calls [][]string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if args[1] == "get" {
			return nil, errors.New("not found")
		}
		return nil, nil
	}
	already, err := RegisterMCP(context.Background(), run, "/usr/local/bin/memorable")
	require.NoError(t, err)
	assert.False(t, already)
	assert.Equal(t, [][]string{
		{"claude", "mcp", "get", "memorable"},
		{"claude", "mcp", "add", "--scope", "user", "memorable", "--", "/usr/local/bin/memorable", "serve"},
	}, calls)

	exists := func(context.Context, string, ...string) ([]byte, error) { return []byte("memorable: ok"), nil }
	already, err = RegisterMCP(context.Background(), exists, "/x")
	require.NoError(t, err)
	assert.True(t, already)
}

func TestDiff(t *testing.T) {
	t.Parallel()
	d := Diff(Change{Path: "f", Before: "a\nb\nc\n", After: "a\nB\nc\nd\n"})
	assert.Contains(t, d, "--- f")
	assert.Contains(t, d, "-b")
	assert.Contains(t, d, "+B")
	assert.Contains(t, d, "+d")
	assert.Contains(t, d, " a")
	assert.Empty(t, Diff(Change{Path: "f", Before: "same", After: "same"}))
}

func TestSnippetMatchesDocs(t *testing.T) {
	t.Parallel()
	doc, err := os.ReadFile("../../docs/claude-md-snippet.md")
	require.NoError(t, err)
	assert.Contains(t, string(doc), strings.TrimSpace(Snippet))
}

func TestDiff_ElidesUnchangedLines(t *testing.T) {
	t.Parallel()
	before := strings.Repeat("same\n", 20)
	d := Diff(Change{Path: "f", Before: before, After: before + "new\n"})
	assert.Equal(t, 3, strings.Count(d, " same"), d)
	assert.Contains(t, d, "  ...")
	assert.Contains(t, d, "+new")
}

func TestRulesDenyUI(t *testing.T) {
	t.Parallel()
	_, _, deny := Rules("/home/x/.config/memorable/credentials.json")
	assert.Contains(t, deny, "Bash(memorable ui:*)")
}

func TestRules_AllowsTagVocabulary(t *testing.T) {
	t.Parallel()
	allow, _, _ := Rules("/creds")
	assert.Contains(t, allow, "mcp__memorable__memory_tags")
}
