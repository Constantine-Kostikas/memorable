package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/rank"
)

type coefficientsView struct {
	B0 float64 `json:"b0"`
	B1 float64 `json:"b1"`
	B2 float64 `json:"b2"`
	B3 float64 `json:"b3"`
	B4 float64 `json:"b4"`
}

type bucketView struct {
	Low       float64 `json:"low"`
	High      float64 `json:"high"`
	Predicted float64 `json:"predicted"`
	Observed  float64 `json:"observed"`
	N         int     `json:"n"`
}

type calibrateView struct {
	Model        string           `json:"model"`
	Dimensions   int              `json:"dimensions"`
	Positives    int              `json:"relevant_labels"`
	Negatives    int              `json:"irrelevant_labels"`
	Coefficients coefficientsView `json:"coefficients"`
	Prior        coefficientsView `json:"prior"`
	Saved        bool             `json:"saved"`
	Reliability  []bucketView     `json:"reliability"`
}

func coefficients(c rank.Calibration) coefficientsView {
	return coefficientsView{B0: c.B0, B1: c.B1, B2: c.B2, B3: c.B3, B4: c.B4}
}

func newCalibrateCmd(d deps, flags *rootFlags) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "calibrate",
		Short: "Fit prompt-recall relevance to how you actually use memories",
		Long: "Learns from searches and prompt recalls whose results Claude fetched (relevant) or ignored for a day " +
			"(irrelevant), and saves a fit once there are 20 of each. The fit stays close to the built-in default " +
			"until the data says otherwise.",
		Example: "  memorable calibrate --dry-run   # show the fit and its reliability without saving\n  memorable calibrate",
		GroupID: groupAdmin,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			return withApp(cmd, d, flags, func(ctx context.Context, a *app) error {
				res, err := a.svc.Calibrate(ctx, dryRun)
				if err != nil {
					return err //nolint:wrapcheck // shown to the user as is
				}
				v := calibrateView{Model: res.Model, Dimensions: res.Dimensions, Positives: res.Positives, Negatives: res.Negatives,
					Coefficients: coefficients(res.Coefficients), Prior: coefficients(res.Prior), Saved: res.Saved}
				for _, b := range res.Reliability {
					v.Reliability = append(v.Reliability, bucketView(b))
				}
				if p.IsJSON() {
					return p.JSON(v)
				}
				c := res.Coefficients
				p.Panel(fmt.Sprintf("calibration · %s/%d", res.Model, res.Dimensions), fmt.Sprintf(
					"labels     %d relevant, %d irrelevant\ncoefficients  b0 %.3f  b1 %.3f  b2 %.3f  b3 %.3f  b4 %.3f",
					res.Positives, res.Negatives, c.B0, c.B1, c.B2, c.B3, c.B4))
				if len(res.Reliability) > 0 {
					rows := make([][]string, 0, len(res.Reliability))
					for _, b := range res.Reliability {
						rows = append(rows, []string{fmt.Sprintf("%.0f%%-%.0f%%", b.Low*100, b.High*100),
							fmt.Sprintf("%.0f%%", b.Predicted*100), fmt.Sprintf("%.0f%%", b.Observed*100), fmt.Sprint(b.N)})
					}
					p.Table([]string{"RANGE", "PREDICTED", "OBSERVED", "N"}, rows)
				}
				switch {
				case res.Saved:
					p.Success("saved; prompt recall now uses this fit")
				case res.Positives < memory.MinCalibrationLabels || res.Negatives < memory.MinCalibrationLabels:
					p.Warn(fmt.Sprintf("not saved: need %d relevant and %d irrelevant labels, have %d and %d",
						memory.MinCalibrationLabels, memory.MinCalibrationLabels, res.Positives, res.Negatives))
				default:
					p.Warn("dry run: not saved")
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the fit without saving it")
	return cmd
}
