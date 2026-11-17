package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/constantine-kostikas/memorable/internal/rank"
)

const (
	// MinCalibrationLabels is how many relevant and how many irrelevant labels a usage fit needs to be saved.
	MinCalibrationLabels = 20
	// labelWait is how long a shown result has to be fetched before it counts as irrelevant.
	labelWait = 24 * time.Hour
	// priorWeight is how many samples' worth of weight the built-in default carries in a usage fit.
	priorWeight = 10.0
)

// CalibrateResult reports a usage fit: the labels it learned from, the coefficients, and how well the
// fitted predictions match what was observed.
type CalibrateResult struct {
	Model        string
	Dimensions   int
	Prior        rank.Calibration
	Coefficients rank.Calibration
	Positives    int
	Negatives    int
	Saved        bool
	Reliability  []rank.Bucket
}

// Calibrate fits relevance calibration to labeled usage, shrinking toward the built-in default for the
// embedding model so few labels cannot pull it far. The fit is saved only with at least
// MinCalibrationLabels relevant and irrelevant labels, and never on a dry run.
func (s *Service) Calibrate(ctx context.Context, dryRun bool) (CalibrateResult, error) {
	model, dims := s.emb.Model(), s.emb.Dimensions()
	prior, ok := rank.DefaultCalibration(model, dims)
	if !ok {
		return CalibrateResult{}, fmt.Errorf("%w: no default calibration for %s with %d dimensions", ErrValidation, model, dims)
	}
	samples, err := s.repo.LabeledResults(ctx, s.now().Add(-labelWait), model, dims)
	if err != nil {
		return CalibrateResult{}, err //nolint:wrapcheck // repository errors are already contextual
	}
	res := CalibrateResult{Model: model, Dimensions: dims, Prior: prior, Coefficients: prior}
	for _, smp := range samples {
		if smp.Relevant {
			res.Positives++
		} else {
			res.Negatives++
		}
	}
	if len(samples) > 0 {
		res.Coefficients = rank.Fit(samples, prior, priorWeight/float64(len(samples)))
	}
	res.Reliability = rank.Reliability(samples, res.Coefficients)
	if dryRun || res.Positives < MinCalibrationLabels || res.Negatives < MinCalibrationLabels {
		return res, nil
	}
	if err := s.repo.SaveCalibration(ctx, StoredCalibration{Model: model, Dimensions: dims, Coefficients: res.Coefficients,
		Positives: res.Positives, Negatives: res.Negatives, Source: CalibrationUsage}); err != nil {
		return res, err //nolint:wrapcheck // repository errors are already contextual
	}
	res.Saved = true
	return res, nil
}
