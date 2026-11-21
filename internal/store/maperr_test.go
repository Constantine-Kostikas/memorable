package store

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func TestMapErrClassifiesAuthFailures(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"28P01", "28000"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			driverErr := fmt.Errorf("failed to connect: %w", &pgconn.PgError{
				Severity: "FATAL",
				Code:     code,
				Message:  `password authentication failed for user "memorable_app"`,
			})
			err := mapErr("begin", driverErr)
			require.ErrorIs(t, err, memory.ErrAuthFailed)
			assert.ErrorIs(t, err, memory.ErrUnavailable)
		})
	}
}
