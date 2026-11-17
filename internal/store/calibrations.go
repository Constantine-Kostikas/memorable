package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

// SaveCalibration implements memory.Repository.
func (s *Store) SaveCalibration(ctx context.Context, c memory.StoredCalibration) error {
	b := c.Coefficients
	_, err := s.pool.Exec(ctx, `INSERT INTO calibrations (id, owner, model, dimensions, b0, b1, b2, b3, b4, positives, negatives, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		newID(), s.owner, c.Model, c.Dimensions, b.B0, b.B1, b.B2, b.B3, b.B4, c.Positives, c.Negatives, c.Source)
	return mapErr("save calibration", err)
}

// LatestCalibration implements memory.Repository.
func (s *Store) LatestCalibration(ctx context.Context, model string, dims int) (*memory.StoredCalibration, error) {
	c := memory.StoredCalibration{Model: model, Dimensions: dims}
	b := &c.Coefficients
	err := s.pool.QueryRow(ctx, `SELECT b0, b1, b2, b3, b4, positives, negatives, source, fitted_at FROM calibrations
		WHERE owner = $1 AND model = $2 AND dimensions = $3 ORDER BY fitted_at DESC, id DESC LIMIT 1`, s.owner, model, dims).
		Scan(&b.B0, &b.B1, &b.B2, &b.B3, &b.B4, &c.Positives, &c.Negatives, &c.Source, &c.FittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // no calibration is a normal state, documented on the interface
	}
	if err != nil {
		return nil, mapErr("latest calibration", err)
	}
	return &c, nil
}
