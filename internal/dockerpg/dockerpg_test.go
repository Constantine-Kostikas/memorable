package dockerpg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/constantine-kostikas/memorable/internal/config"
)

func TestStart_Args(t *testing.T) {
	t.Parallel()
	var got []string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		got = args
		return []byte("abc123\n"), nil
	}
	require.NoError(t, Start(context.Background(), run, config.Default().Database, "s3cret"))
	assert.Equal(t, []string{"run", "-d", "--name", "memorable-postgres", "--restart", "unless-stopped",
		"-p", "127.0.0.1:5434:5432", "-e", "POSTGRES_USER=memorable_admin", "-e", "POSTGRES_PASSWORD=s3cret",
		"-e", "POSTGRES_DB=memorable", "-v", "memorable-pgdata:/var/lib/postgresql",
		"pgvector/pgvector:0.8.7-pg18-trixie"}, got)
}

func TestStart_RejectsNonLoopbackHost(t *testing.T) {
	t.Parallel()
	db := config.Default().Database
	db.Host = "0.0.0.0"
	err := Start(context.Background(), func(context.Context, ...string) ([]byte, error) { return nil, nil }, db, "x")
	require.Error(t, err)
}

func TestState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		out     string
		err     error
		want    State
		wantErr bool
	}{
		{out: "true\n", want: StateRunning},
		{out: "false\n", want: StateStopped},
		{err: errors.New("Error: No such object: memorable-postgres"), want: StateMissing},
	} {
		run := func(context.Context, ...string) ([]byte, error) { return []byte(tc.out), tc.err }
		got, err := ContainerState(context.Background(), run, "memorable-postgres")
		require.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}
}

func TestWaitReady(t *testing.T) {
	t.Parallel()
	calls := 0
	run := func(context.Context, ...string) ([]byte, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("no response")
		}
		return []byte("accepting connections"), nil
	}
	require.NoError(t, WaitReady(context.Background(), run, "c", "u", time.Millisecond, time.Second))
	assert.Equal(t, 3, calls)

	never := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("no") }
	require.Error(t, WaitReady(context.Background(), never, "c", "u", time.Millisecond, 20*time.Millisecond))
}
