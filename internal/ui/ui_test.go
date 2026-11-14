package ui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noEnv(string) string { return "" }

func TestPrinter_PlainWhenNotTTY(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p, err := NewPrinter(&buf, "text", noEnv)
	require.NoError(t, err)
	p.Panel("Context", "context personal")
	p.Table([]string{"ID", "Title"}, [][]string{{"1", "Ports"}, {"2", "Database"}})
	p.Success("done")
	p.Warn("careful")
	out := buf.String()
	assert.NotContains(t, out, "\x1b[", "no ANSI escapes when not a terminal")
	assert.Contains(t, out, "Context")
	assert.Contains(t, out, "Ports")
	assert.Contains(t, out, "done")
	assert.True(t, strings.Contains(out, "ID") && strings.Contains(out, "Title"))
}

func TestPrinter_NoColorForcesPlain(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p, err := NewPrinter(&buf, "text", func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	})
	require.NoError(t, err)
	assert.False(t, p.Styled())
}

func TestPrinter_JSON(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p, err := NewPrinter(&buf, "json", noEnv)
	require.NoError(t, err)
	assert.True(t, p.IsJSON())
	require.NoError(t, p.JSON(map[string]int{"a": 1}))
	var got map[string]int
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, 1, got["a"])
}

func TestPrinter_BadFormat(t *testing.T) {
	t.Parallel()
	_, err := NewPrinter(&bytes.Buffer{}, "yaml", noEnv)
	require.Error(t, err)
}

func TestPlainTable_Aligns(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p, err := NewPrinter(&buf, "text", noEnv)
	require.NoError(t, err)
	p.Table([]string{"A", "B"}, [][]string{{"long value", "x"}, {"s", "y"}})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, strings.Index(lines[1], "x"), strings.Index(lines[2], "y"))
}
