package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/memory/memorytest"
	"github.com/constantine-kostikas/memorable/internal/scan"
)

// gemmaDeps serves a fake app whose embedder reports embeddinggemma, so the built-in default applies.
func gemmaDeps(t *testing.T) deps {
	t.Helper()
	d := defaultDeps()
	a := fakeApp(t)
	a.emb = &memorytest.Embedder{ModelName: "embeddinggemma", Dims: 768, Vectors: map[string][]float32{}}
	repo := memorytest.NewRepo()
	sec, err := scan.NewSecretScanner()
	require.NoError(t, err)
	a.svc = memory.NewService(repo, repo, a.emb, sec, scan.Injection, memory.Settings{Owner: "dev", Search: a.cfg.Search})
	d.build = func(context.Context, string, []string, *slog.Logger) (*app, error) { return a, nil }
	d.cwd = func() (string, error) { return "/home/x/projects/demo", nil }
	return d
}

func TestCalibrateCommandNoDefault(t *testing.T) {
	t.Parallel()
	out, err := runRoot(t, fakeDeps(t), "", "calibrate")
	require.Error(t, err)
	assert.Contains(t, err.Error()+out, "no default calibration for fake")
}

func TestCalibrateCommandPlainOutput(t *testing.T) {
	t.Parallel()
	out, err := runRoot(t, gemmaDeps(t), "", "calibrate", "--dry-run")
	require.NoError(t, err, out)
	assert.Contains(t, out, "embeddinggemma")
	assert.Contains(t, out, "labels     0 relevant, 0 irrelevant")
	assert.Contains(t, out, "not saved: need 20 relevant and 20 irrelevant labels, have 0 and 0")
}

func TestCalibrateCommandJSON(t *testing.T) {
	t.Parallel()
	out, err := runRoot(t, gemmaDeps(t), "", "calibrate", "--dry-run", "-o", "json")
	require.NoError(t, err, out)
	var v calibrateView
	require.NoError(t, json.Unmarshal([]byte(out), &v), out)
	assert.Equal(t, "embeddinggemma", v.Model)
	assert.Equal(t, 768, v.Dimensions)
	assert.False(t, v.Saved)
	assert.NotZero(t, v.Coefficients.B4)
}
