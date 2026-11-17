package rank

import (
	"math"
	"strconv"
)

// Signals are the retrieval evidence for one search result.
type Signals struct {
	// Similarity is the vector cosine similarity, 0 when the result came from text retrieval only.
	Similarity float64
	FTS        bool
	Trigram    bool
	// VectorRank is the 1-based position in the vector candidate list, 0 when absent. It enters the model
	// as 1/VectorRank: the evaluation set showed rank carries most of the relevance signal similarity misses.
	VectorRank int
}

// Calibration is a logistic model mapping Signals to the probability that a result is relevant.
type Calibration struct {
	B0, B1, B2, B3, B4 float64
}

// Sample is one labeled result used to fit a Calibration.
type Sample struct {
	Signals  Signals
	Relevant bool
}

// Bucket is one row of a reliability table: predictions in [Low, High) and how often they were right.
type Bucket struct {
	Low, High float64
	Predicted float64
	Observed  float64
	N         int
}

const (
	newtonIterations = 50
	// ridgeFloor keeps the Hessian invertible when a signal never varies in the samples.
	ridgeFloor      = 1e-9
	reliabilityBins = 10
	featureCount    = 5
)

// defaults holds the built-in calibrations, keyed by "model/dimensions". Each is fitted by `make test-eval`,
// which prints the literal to paste here.
var defaults = map[string]Calibration{
	"embeddinggemma/768": {B0: -4.5528, B1: 1.4749, B2: 3.4224, B3: 0.2246, B4: 5.2589},
}

// DefaultCalibration returns the built-in calibration for an embedding model and dimension.
func DefaultCalibration(model string, dims int) (Calibration, bool) {
	c, ok := defaults[model+"/"+strconv.Itoa(dims)]
	return c, ok
}

// Relevance returns the calibrated probability, between 0 and 1, that a result with these signals is relevant.
func (c Calibration) Relevance(s Signals) float64 {
	x := features(s)
	return sigmoid(dot(c.vector(), x))
}

// Fit fits a Calibration to samples by Newton's method on the mean log loss plus lambda·‖b − prior‖².
// With few samples the result stays near prior; with many, the data dominates. No samples returns prior.
func Fit(samples []Sample, prior Calibration, lambda float64) Calibration {
	if len(samples) == 0 {
		return prior
	}
	p0 := prior.vector()
	b := p0
	n := float64(len(samples))
	for range newtonIterations {
		var grad [featureCount]float64
		var hess [featureCount][featureCount]float64
		for _, s := range samples {
			x := features(s.Signals)
			p := sigmoid(dot(b, x))
			y := 0.0
			if s.Relevant {
				y = 1
			}
			w := p * (1 - p)
			for i := range featureCount {
				grad[i] += (p - y) * x[i] / n
				for j := range featureCount {
					hess[i][j] += w * x[i] * x[j] / n
				}
			}
		}
		for i := range featureCount {
			grad[i] += 2 * lambda * (b[i] - p0[i])
			hess[i][i] += 2*lambda + ridgeFloor
		}
		step, ok := solve(hess, grad)
		if !ok {
			break
		}
		for i := range featureCount {
			b[i] -= step[i]
		}
	}
	return Calibration{B0: b[0], B1: b[1], B2: b[2], B3: b[3], B4: b[4]}
}

// Reliability groups samples into ten equal-width prediction buckets and reports, for each non-empty one,
// the mean prediction and the observed share of relevant samples.
func Reliability(samples []Sample, c Calibration) []Bucket {
	var sumP [reliabilityBins]float64
	var hits, count [reliabilityBins]int
	for _, s := range samples {
		p := c.Relevance(s.Signals)
		i := min(int(p*reliabilityBins), reliabilityBins-1)
		sumP[i] += p
		count[i]++
		if s.Relevant {
			hits[i]++
		}
	}
	var out []Bucket
	for i := range reliabilityBins {
		if count[i] == 0 {
			continue
		}
		out = append(out, Bucket{
			Low:       float64(i) / reliabilityBins,
			High:      float64(i+1) / reliabilityBins,
			Predicted: sumP[i] / float64(count[i]),
			Observed:  float64(hits[i]) / float64(count[i]),
			N:         count[i],
		})
	}
	return out
}

func (c Calibration) vector() [featureCount]float64 {
	return [featureCount]float64{c.B0, c.B1, c.B2, c.B3, c.B4}
}

func features(s Signals) [featureCount]float64 {
	inverseRank := 0.0
	if s.VectorRank > 0 {
		inverseRank = 1 / float64(s.VectorRank)
	}
	return [featureCount]float64{1, s.Similarity, boolFloat(s.FTS), boolFloat(s.Trigram), inverseRank}
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func dot(a, b [featureCount]float64) float64 {
	var s float64
	for i := range featureCount {
		s += a[i] * b[i]
	}
	return s
}

func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

// solve returns x with a·x = b by Gaussian elimination with partial pivoting; ok is false when a is singular.
func solve(a [featureCount][featureCount]float64, b [featureCount]float64) ([featureCount]float64, bool) {
	for col := range featureCount {
		pivot := col
		for r := col + 1; r < featureCount; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[pivot][col]) {
				pivot = r
			}
		}
		if math.Abs(a[pivot][col]) < 1e-15 {
			return b, false
		}
		a[col], a[pivot] = a[pivot], a[col]
		b[col], b[pivot] = b[pivot], b[col]
		for r := col + 1; r < featureCount; r++ {
			f := a[r][col] / a[col][col]
			for k := col; k < featureCount; k++ {
				a[r][k] -= f * a[col][k]
			}
			b[r] -= f * b[col]
		}
	}
	var x [featureCount]float64
	for r := featureCount - 1; r >= 0; r-- {
		s := b[r]
		for k := r + 1; k < featureCount; k++ {
			s -= a[r][k] * x[k]
		}
		x[r] = s / a[r][r]
	}
	return x, true
}
