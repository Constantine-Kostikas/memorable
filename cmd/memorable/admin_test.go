package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func TestPromptAdminPassword_NoTTY(t *testing.T) {
	t.Parallel()
	_, err := promptAdminPassword(strings.NewReader("secret\n"), &bytes.Buffer{}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal")
}

func TestPurge_RequiresConfirm(t *testing.T) {
	t.Parallel()
	d := fakeDeps(t)
	called := false
	d.adminPassword = func(*cobra.Command) (string, error) {
		called = true
		return "", nil
	}
	_, err := runRoot(t, d, "", "purge", uuid.NewString())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--yes")
	assert.Equal(t, 2, exitCode(err))
	assert.False(t, called, "no password prompt before confirmation")
}

func TestEdit_RoundTrip(t *testing.T) {
	t.Parallel()
	d := fakeDeps(t)
	script := filepath.Join(t.TempDir(), "editor.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"+
		"sed -i 's/^title = .*/title = \"Spelling rules\"/; s/US spelling/US spelling and Oxford commas/' \"$1\"\n"), 0o700))
	d.getenv = func(k string) string {
		if k == "EDITOR" {
			return script
		}
		return ""
	}
	out, err := runRoot(t, d, "", "edit", "style/spelling")
	require.NoError(t, err, out)
	assert.Contains(t, out, "version 2")

	a, err := d.build(context.Background(), "", nil, nil)
	require.NoError(t, err)
	got, err := a.svc.Get(context.Background(), memory.GetRequest{Slugs: []string{"style/spelling"}, Passive: true})
	require.NoError(t, err)
	assert.Equal(t, "Spelling rules", got[0].Memory.Title)
	assert.Equal(t, "Use US spelling and Oxford commas everywhere.", got[0].Memory.Content)
}

func TestEdit_NoChange(t *testing.T) {
	t.Parallel()
	d := fakeDeps(t)
	d.getenv = func(k string) string {
		if k == "EDITOR" {
			return "true"
		}
		return ""
	}
	out, err := runRoot(t, d, "", "edit", "style/spelling")
	require.NoError(t, err, out)
	assert.Contains(t, out, "no changes")
}

func TestEditorDocument_RoundTrip(t *testing.T) {
	t.Parallel()
	slug := "a/b"
	m := memory.Memory{Title: "T \"q\"", Content: "line one\n+++\nline two", Type: memory.TypeProject, Tags: []string{"x"}, Slug: &slug}
	doc := editorDocument(m)
	got, err := parseEditorDocument(doc)
	require.NoError(t, err)
	assert.Equal(t, m.Title, got.Title)
	assert.Equal(t, m.Content, got.Content)
	assert.Equal(t, []string{"x"}, got.Tags)
	assert.Equal(t, "project", got.Type)
}
