// Package rank implements result fusion, ranking boosts, and time-decayed access scores.
package rank

import (
	"math"
	"time"

	"github.com/google/uuid"
)

const rrfK = 60

// RRF fuses ranked lists with reciprocal rank fusion: Σ 1/(60 + rank), rank starting at 1.
func RRF(lists ...[]uuid.UUID) map[uuid.UUID]float64 {
	scores := make(map[uuid.UUID]float64)
	for _, list := range lists {
		for i, id := range list {
			scores[id] += 1.0 / float64(rrfK+i+1)
		}
	}
	return scores
}

// Boost applies the project, pinned, and capped popularity multipliers to a fused score.
func Boost(rrf float64, projectMatch, pinned bool, effectiveScore float64) float64 {
	m := 1.0
	if projectMatch {
		m += 0.20
	}
	if pinned {
		m += 0.10
	}
	m += math.Min(0.15, 0.05*math.Log1p(math.Max(0, effectiveScore)))
	return rrf * m
}

// Decay returns score decayed from `from` to `to` with the given half-life.
func Decay(score float64, from, to time.Time, halfLife time.Duration) float64 {
	return score * math.Exp2(-to.Sub(from).Seconds()/halfLife.Seconds())
}

// Bump decays score to now and adds weight.
func Bump(score float64, updatedAt, now time.Time, halfLife time.Duration, weight float64) float64 {
	return Decay(score, updatedAt, now, halfLife) + weight
}

// Key returns ln(score) + λ·t, which orders rows exactly like their decayed score at any common instant.
// A zero score returns -Inf.
func Key(score float64, updatedAt time.Time, halfLife time.Duration) float64 {
	if score <= 0 {
		return math.Inf(-1)
	}
	lambda := math.Ln2 / halfLife.Seconds()
	return math.Log(score) + lambda*float64(updatedAt.Unix())
}
