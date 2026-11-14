package transfer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/memory/memorytest"
	"github.com/constantine-kostikas/memorable/internal/scan"
	"github.com/constantine-kostikas/memorable/internal/scope"
)

func service(t *testing.T) (*memory.Service, *memorytest.Repo) {
	t.Helper()
	repo := memorytest.NewRepo()
	sec, err := scan.NewSecretScanner()
	require.NoError(t, err)
	cfg := config.Default()
	return memory.NewService(repo, repo, memorytest.NewEmbedder(), sec, scan.Injection, memory.Settings{
		Owner: "dev", Scope: scope.Resolution{Context: "personal"}, Search: cfg.Search, Preload: cfg.Preload, WritesPerMinute: 600,
	}), repo
}

func seed(t *testing.T, svc *memory.Service) {
	t.Helper()
	slug := "infra/ports"
	_, err := svc.Write(context.Background(), []memory.NewMemory{
		{Title: "Ports", Content: "Postgres listens on 5434.", Type: memory.TypeProject, Source: memory.SourceUserStated, Slug: &slug, Tags: []string{"infra"}, Pinned: true, Global: true},
		{Title: "Style", Content: "Use US spelling.\nNo em dashes.", Type: memory.TypeUser, Source: memory.SourceUserStated, Global: true},
	})
	require.NoError(t, err)
}

func TestExportImport_RoundTripJSON(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src, repo := service(t)
	seed(t, src)
	var buf bytes.Buffer
	n, err := ExportJSON(ctx, repo, "dev", "personal", &buf)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	items, err := ParseJSON(&buf)
	require.NoError(t, err)
	require.Len(t, items, 2)
	dst, dstRepo := service(t)
	report, err := Import(ctx, dst, items)
	require.NoError(t, err)
	assert.Equal(t, 2, report.Counts[memory.OutcomeCreated])
	assert.Equal(t, 2, dstRepo.Count())

	got, err := dst.Get(ctx, memory.GetRequest{Slugs: []string{"infra/ports"}, Passive: true})
	require.NoError(t, err)
	require.NoError(t, got[0].Err)
	assert.Equal(t, "Postgres listens on 5434.", got[0].Memory.Content)
	assert.Equal(t, []string{"infra"}, got[0].Memory.Tags)
	assert.True(t, got[0].Memory.Pinned)
	assert.Equal(t, memory.SourceImported, got[0].Memory.Source)
}

func TestExportMarkdown_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src, repo := service(t)
	seed(t, src)
	dir := t.TempDir()
	n, err := ExportMarkdown(ctx, repo, "dev", dir)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.FileExists(t, filepath.Join(dir, "infra-ports.md"))

	items, skipped, err := ParsePath(dir)
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, items, 2)
	var style memory.NewMemory
	for _, it := range items {
		if it.Title == "Style" {
			style = it
		}
	}
	assert.Equal(t, "Use US spelling.\nNo em dashes.", style.Content)
	assert.Equal(t, memory.TypeUser, style.Type)
}

func TestParseClaudeMemoryFile(t *testing.T) {
	t.Parallel()
	f, err := os.Open("testdata/claude-memory.md")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	n, err := ParseClaudeMemoryFile(f, "v2-multi-user-scope.md")
	require.NoError(t, err)
	require.NotNil(t, n.Slug)
	assert.Equal(t, "v2-multi-user-scope", *n.Slug)
	assert.Equal(t, memory.TypeProject, n.Type)
	assert.Equal(t, memory.SourceImported, n.Source)
	assert.True(t, strings.HasPrefix(n.Title, "Multi-user/shared-server support"))
	assert.Contains(t, n.Content, "memorable v1 is single-user")
	assert.NotContains(t, n.Content, "---")
	require.NoError(t, n.Validate())
}

func TestParsePath_SkipsIndexAndReportsBadFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- [x](x.md)\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ok.md"), []byte("---\nname: ok\ndescription: Fine\ntype: user\n---\nBody\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.md"), []byte("no front matter"), 0o600))
	items, skipped, err := ParsePath(dir)
	require.NoError(t, err)
	assert.Len(t, items, 1)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0], "bad.md")
}

func TestImport_RejectsSecrets(t *testing.T) {
	t.Parallel()
	svc, repo := service(t)
	items := []memory.NewMemory{{Title: "Key", Content: "token ghp_" + "9Xk2mQ7vR4tL8wN3pZ6yB1cD5fG0hJ2sK4aE",
		Type: memory.TypeReference, Source: memory.SourceImported}}
	report, err := Import(context.Background(), svc, items)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Counts[memory.OutcomeRejected])
	assert.Zero(t, repo.Count())
	require.Len(t, report.Rows, 1)
	assert.Contains(t, report.Rows[0].Reason, "secret detected")
}

func TestImport_Batches(t *testing.T) {
	t.Parallel()
	svc, repo := service(t)
	repo.NearestOverride = func() []memory.Candidate { return nil } // this test is about batching, not similarity
	items := make([]memory.NewMemory, 45)
	for i := range items {
		items[i] = memory.NewMemory{Title: "n", Content: uuid.NewString(), Type: memory.TypeUser, Source: memory.SourceImported}
	}
	report, err := Import(context.Background(), svc, items)
	require.NoError(t, err)
	assert.Equal(t, 45, report.Counts[memory.OutcomeCreated])
	assert.Equal(t, 45, repo.Count())
}
