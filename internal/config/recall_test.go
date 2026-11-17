package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecallDefaults(t *testing.T) {
	t.Parallel()
	r := Default().Recall
	assert.InDelta(t, 0.70, r.MinRelevance, 1e-9)
	assert.Equal(t, 3, r.MaxItems)
	assert.Equal(t, 3, r.MinWords)
	assert.Equal(t, 1500, r.DeadlineMS)
}

func TestRecallValidation(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Recall){
		"relevance zero": func(r *Recall) { r.MinRelevance = 0 },
		"relevance one":  func(r *Recall) { r.MinRelevance = 1 },
		"too many items": func(r *Recall) { r.MaxItems = 11 },
		"no words":       func(r *Recall) { r.MinWords = 0 },
		"deadline short": func(r *Recall) { r.DeadlineMS = 100 },
		"deadline long":  func(r *Recall) { r.DeadlineMS = 6000 },
	} {
		c := Default()
		mutate(&c.Recall)
		assert.ErrorContains(t, c.Validate(), "recall", name)
	}
	c := Default()
	assert.NoError(t, c.Validate())
}
