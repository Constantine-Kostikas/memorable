package rank

import (
	"math"
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const halfLife = 30 * 24 * time.Hour

func TestRRF(t *testing.T) {
	t.Parallel()
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	got := RRF([]uuid.UUID{a, b}, []uuid.UUID{a, c})
	assert.InDelta(t, 2.0/61, got[a], 1e-12)
	assert.InDelta(t, 1.0/62, got[b], 1e-12)
	assert.InDelta(t, 1.0/62, got[c], 1e-12)
	assert.Len(t, got, 3)
	assert.Empty(t, RRF())
}

func TestBoost(t *testing.T) {
	t.Parallel()
	assert.InDelta(t, 0.5, Boost(0.5, false, false, 0), 1e-12)
	assert.InDelta(t, 0.5*1.2, Boost(0.5, true, false, 0), 1e-12)
	assert.InDelta(t, 0.5*1.3, Boost(0.5, true, true, 0), 1e-12)
	assert.InDelta(t, 0.5*(1+0.05*math.Log(2)), Boost(0.5, false, false, 1), 1e-12)
	assert.InDelta(t, 0.5*1.15, Boost(0.5, false, false, 1e9), 1e-12)
	assert.InDelta(t, 0.5*1.45, Boost(0.5, true, true, 1e9), 1e-12)
}

func TestBoostPartsMatchesBoost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		project, pin   bool
		effectiveScore float64
	}{
		{"plain", false, false, 0},
		{"project", true, false, 0},
		{"project and pinned", true, true, 0},
		{"capped usage", true, true, 1e9},
		{"usage", false, false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BoostParts(0.5, tc.project, tc.pin, tc.effectiveScore)
			assert.InDelta(t, Boost(0.5, tc.project, tc.pin, tc.effectiveScore), got.Final, 1e-12)
		})
	}
	p := BoostParts(0.5, true, true, 0)
	assert.InDelta(t, 0.20, p.Project, 1e-12)
	assert.InDelta(t, 0.10, p.Pinned, 1e-12)
	assert.InDelta(t, 1.30, p.Multiplier, 1e-12)
}

func TestDecay(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	assert.InDelta(t, 4.0, Decay(8, t0, t0.Add(halfLife), halfLife), 1e-9)
	assert.InDelta(t, 8.0, Decay(8, t0, t0, halfLife), 1e-12)
	assert.InDelta(t, 1.5, Bump(1, t0, t0.Add(halfLife), halfLife, 1), 1e-9)
}

func TestKey_OrderMatchesEffectiveScore(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := rand.New(rand.NewPCG(1, 2))
	type row struct {
		score   float64
		updated time.Time
	}
	rows := make([]row, 200)
	for i := range rows {
		rows[i] = row{score: r.Float64()*50 + 0.01, updated: base.Add(time.Duration(r.Int64N(int64(300 * 24 * time.Hour))))}
	}
	for _, now := range []time.Time{base.Add(301 * 24 * time.Hour), base.Add(400 * 24 * time.Hour), base.Add(5000 * 24 * time.Hour)} {
		byKey := make([]int, len(rows))
		byEff := make([]int, len(rows))
		for i := range rows {
			byKey[i], byEff[i] = i, i
		}
		sort.SliceStable(byKey, func(i, j int) bool {
			return Key(rows[byKey[i]].score, rows[byKey[i]].updated, halfLife) > Key(rows[byKey[j]].score, rows[byKey[j]].updated, halfLife)
		})
		sort.SliceStable(byEff, func(i, j int) bool {
			return Decay(rows[byEff[i]].score, rows[byEff[i]].updated, now, halfLife) > Decay(rows[byEff[j]].score, rows[byEff[j]].updated, now, halfLife)
		})
		require.Equal(t, byEff, byKey, "order differs at now=%s", now)
	}
}

func TestKey_ZeroScoreSortsLast(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	zero := Key(0, now, halfLife)
	assert.True(t, math.IsInf(zero, -1))
	assert.Less(t, zero, Key(1e-9, now.Add(-1000*24*time.Hour), halfLife))
}
