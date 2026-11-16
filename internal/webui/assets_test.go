package webui

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedAssets(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"static/app.css", "static/htmx.min.js"} {
		data, err := assetFS.ReadFile(name)
		require.NoError(t, err, name)
		assert.NotEmpty(t, data, name)
	}

	tmpl, err := parseTemplates()
	require.NoError(t, err)

	var out bytes.Buffer
	err = tmpl.ExecuteTemplate(&out, "layout.html", pageData{
		Title:       "<script>x</script>",
		Context:     "personal",
		Contexts:    []string{"personal", "work"},
		CSRF:        "token-123",
		ReviewCount: 3,
	})
	require.NoError(t, err)

	body := out.String()
	assert.NotContains(t, body, "<script>x</script>")
	assert.Contains(t, body, "&lt;script&gt;x&lt;/script&gt;")
	assert.Contains(t, body, `name="htmx-config"`)
	assert.Contains(t, body, `name="csrf-token" content="token-123"`)
	assert.Contains(t, body, "Review (3)")
}

func TestErrorTemplateEscapesMessage(t *testing.T) {
	t.Parallel()

	tmpl, err := parseTemplates()
	require.NoError(t, err)

	var out bytes.Buffer
	err = tmpl.ExecuteTemplate(&out, "error.html", pageData{Title: "Not found", Message: "<b>nope</b>"})
	require.NoError(t, err)
	assert.NotContains(t, out.String(), "<b>nope</b>")
}
