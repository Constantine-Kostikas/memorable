package rank

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// synthetic draws n samples whose labels follow truth, with signals spread like real search results.
func synthetic(t *testing.T, truth Calibration, n int, seed uint64) []Sample {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0))
	out := make([]Sample, 0, n)
	for range n {
		s := Signals{Similarity: rng.Float64() * 0.9, FTS: rng.IntN(2) == 1, Trigram: rng.IntN(3) == 0}
		out = append(out, Sample{Signals: s, Relevant: rng.Float64() < truth.Relevance(s)})
	}
	return out
}

func TestRelevanceIsSigmoid(t *testing.T) {
	t.Parallel()
	assert.InDelta(t, 0.5, Calibration{}.Relevance(Signals{}), 1e-9)
	assert.Greater(t, Calibration{B1: 4}.Relevance(Signals{Similarity: 0.9}), 0.95)
	assert.InDelta(t, 1/(1+math.Exp(-(1+2+3))), Calibration{B0: 1, B2: 2, B3: 3}.Relevance(Signals{FTS: true, Trigram: true}), 1e-9)
}

func TestFitRecoversKnownCoefficients(t *testing.T) {
	t.Parallel()
	truth := Calibration{B0: -4, B1: 8, B2: 1.5, B3: 0.5}
	got := Fit(synthetic(t, truth, 20000, 42), Calibration{}, 1e-4)
	assert.InDelta(t, truth.B0, got.B0, 0.4)
	assert.InDelta(t, truth.B1, got.B1, 0.8)
	assert.InDelta(t, truth.B2, got.B2, 0.4)
	assert.InDelta(t, truth.B3, got.B3, 0.4)
}

func TestFitStaysNearPriorWithFewSamples(t *testing.T) {
	t.Parallel()
	prior := Calibration{B0: -3, B1: 6, B2: 1, B3: 1}
	got := Fit(synthetic(t, Calibration{B0: 2}, 10, 1), prior, 1.0)
	assert.InDelta(t, prior.B1, got.B1, 0.5)
}

func TestFitWithoutSamplesReturnsPrior(t *testing.T) {
	t.Parallel()
	prior := Calibration{B0: -3, B1: 6}
	assert.Equal(t, prior, Fit(nil, prior, 1.0))
}

func TestReliabilityBuckets(t *testing.T) {
	t.Parallel()
	c := Calibration{B0: math.Log(0.85 / 0.15)} // every prediction is 0.85
	samples := []Sample{{Relevant: true}, {Relevant: true}, {Relevant: false}, {Relevant: true}}
	buckets := Reliability(samples, c)
	require.Len(t, buckets, 1)
	assert.InDelta(t, 0.8, buckets[0].Low, 1e-9)
	assert.InDelta(t, 0.9, buckets[0].High, 1e-9)
	assert.InDelta(t, 0.85, buckets[0].Predicted, 1e-9)
	assert.InDelta(t, 0.75, buckets[0].Observed, 1e-9)
	assert.Equal(t, 4, buckets[0].N)
}

func TestReliabilityTopBucketIncludesOne(t *testing.T) {
	t.Parallel()
	buckets := Reliability([]Sample{{Relevant: true}}, Calibration{B0: 100})
	require.Len(t, buckets, 1)
	assert.InDelta(t, 0.9, buckets[0].Low, 1e-9)
}

func TestDefaultCalibrationUnknownModel(t *testing.T) {
	t.Parallel()
	_, ok := DefaultCalibration("nope", 1)
	assert.False(t, ok)
}
