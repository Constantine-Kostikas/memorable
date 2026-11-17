package memory_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/rank"
)

// gemmaEnv reports the embeddinggemma model so the built-in default calibration applies.
func gemmaEnv(t *testing.T) *env {
	t.Helper()
	e := newRecallEnv(t, nil, nil)
	e.emb.ModelName, e.emb.Dims = "embeddinggemma", 768
	return e
}

// labeled logs an MCP search showing one memory per label and fetches the relevant ones, then ages the
// events past the 24-hour wait by moving the clock forward.
func labeled(t *testing.T, e *env, relevant, irrelevant int) {
	t.Helper()
	ctx := context.Background()
	m := created(t, e, note("Postgres port", "postgres runs on port 5434"))
	log := func(fetch bool, rank int) {
		id, err := e.repo.LogEvent(ctx, memory.Event{Kind: memory.EventSearch, Returned: []uuid.UUID{m.ID},
			Details: memory.EventDetails{Origin: memory.OriginMCP, Model: "embeddinggemma", Dimensions: 768,
				Signals: []memory.ResultSignals{{ID: m.ID, Similarity: 0.5, FTS: fetch, VectorRank: rank}}}})
		require.NoError(t, err)
		if fetch {
			require.NoError(t, e.repo.LogGet(ctx, m.ID, &id, nil))
		}
	}
	for range relevant {
		log(true, 1)
	}
	for range irrelevant {
		log(false, 4)
	}
	e.now = e.now.AddDate(1, 0, 0)
}

func TestCalibrateBelowMinimumDoesNotSave(t *testing.T) {
	t.Parallel()
	e := gemmaEnv(t)
	labeled(t, e, 5, 30)
	res, err := e.svc.Calibrate(context.Background(), false)
	require.NoError(t, err)
	assert.False(t, res.Saved)
	assert.Equal(t, 5, res.Positives)
	assert.Equal(t, 30, res.Negatives)
	_, source, _, err := e.svc.ActiveCalibration(context.Background())
	require.NoError(t, err)
	assert.Equal(t, memory.CalibrationDefault, source)
}

func TestCalibrateSavesUsageFit(t *testing.T) {
	t.Parallel()
	e := gemmaEnv(t)
	labeled(t, e, 25, 25)
	res, err := e.svc.Calibrate(context.Background(), false)
	require.NoError(t, err)
	require.True(t, res.Saved)
	def, _ := rank.DefaultCalibration("embeddinggemma", 768)
	assert.Equal(t, def, res.Prior)
	assert.NotEqual(t, def, res.Coefficients)
	assert.NotEmpty(t, res.Reliability)

	c, source, ok, err := e.svc.ActiveCalibration(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, memory.CalibrationUsage, source)
	assert.Equal(t, res.Coefficients, c)

	again, err := e.svc.Calibrate(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, def, again.Prior, "refits regularize toward the built-in default, never a stored fit")
}

func TestCalibrateDryRun(t *testing.T) {
	t.Parallel()
	e := gemmaEnv(t)
	labeled(t, e, 25, 25)
	res, err := e.svc.Calibrate(context.Background(), true)
	require.NoError(t, err)
	assert.False(t, res.Saved)
	_, source, _, err := e.svc.ActiveCalibration(context.Background())
	require.NoError(t, err)
	assert.Equal(t, memory.CalibrationDefault, source)
}

func TestCalibrateIgnoresRecentEvents(t *testing.T) {
	t.Parallel()
	e := gemmaEnv(t)
	labeled(t, e, 25, 25)
	e.now = e.now.AddDate(-1, 0, 0)
	res, err := e.svc.Calibrate(context.Background(), true)
	require.NoError(t, err)
	assert.Zero(t, res.Positives+res.Negatives, "events younger than 24 hours are not labeled yet")
}

func TestCalibrateNoPrior(t *testing.T) {
	t.Parallel()
	e := newRecallEnv(t, nil, nil)
	_, err := e.svc.Calibrate(context.Background(), true)
	require.ErrorIs(t, err, memory.ErrValidation)
	assert.ErrorContains(t, err, "fake")
}
