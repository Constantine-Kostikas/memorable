package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func listProfile(ctx context.Context, pool *pgxpool.Pool) ([]memory.Memory, error) {
	rows, err := pool.Query(ctx, `SELECT `+memCols+`, true AS is_profile FROM mem_profile.memories
		WHERE status <> 'deleted' ORDER BY created_at`)
	if err != nil {
		return nil, mapErr("profile list", err)
	}
	return collectMemories(rows, "profile list")
}

// ProfileList returns non-deleted profile memories. The app role reads the profile schema but cannot write it.
func (s *Store) ProfileList(ctx context.Context) ([]memory.Memory, error) {
	return listProfile(ctx, s.pool)
}
