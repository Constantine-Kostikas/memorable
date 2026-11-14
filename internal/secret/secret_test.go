package secret

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

// The keyring mock is process-global, so these tests do not run in parallel.

func TestStore_Keyring(t *testing.T) { //nolint:paralleltest // global keyring mock
	keyring.MockInit()
	fallback := filepath.Join(t.TempDir(), "credentials.json")
	s := New(fallback)

	require.NoError(t, s.Set("memorable_app@127.0.0.1:5434/memorable", "pw1"))
	got, err := s.Get("memorable_app@127.0.0.1:5434/memorable")
	require.NoError(t, err)
	assert.Equal(t, "pw1", got)
	assert.NoFileExists(t, fallback)

	require.NoError(t, s.Delete("memorable_app@127.0.0.1:5434/memorable"))
	_, err = s.Get("memorable_app@127.0.0.1:5434/memorable")
	require.ErrorIs(t, err, memory.ErrNotFound)
}

func TestStore_FallbackWhenKeyringFails(t *testing.T) { //nolint:paralleltest // global keyring mock
	keyring.MockInitWithError(errors.New("no dbus"))
	fallback := filepath.Join(t.TempDir(), "dir", "credentials.json")
	s := New(fallback)

	require.NoError(t, s.Set("acct", "pw2"))
	info, err := os.Stat(fallback)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, err := s.Get("acct")
	require.NoError(t, err)
	assert.Equal(t, "pw2", got)

	require.NoError(t, s.Set("other", "pw3"))
	got, err = s.Get("acct")
	require.NoError(t, err)
	assert.Equal(t, "pw2", got)

	require.NoError(t, s.Delete("acct"))
	_, err = s.Get("acct")
	require.ErrorIs(t, err, memory.ErrNotFound)
}

func TestStore_NotFound(t *testing.T) { //nolint:paralleltest // global keyring mock
	keyring.MockInit()
	s := New(filepath.Join(t.TempDir(), "credentials.json"))
	_, err := s.Get("missing")
	require.ErrorIs(t, err, memory.ErrNotFound)
}

func TestAccount(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "memorable_app@127.0.0.1:5434/memorable", Account("memorable_app", "127.0.0.1", 5434, "memorable"))
}

func TestStore_WithoutKeyring(t *testing.T) {
	t.Parallel()
	fallback := filepath.Join(t.TempDir(), "credentials.json")
	s := New(fallback, WithoutKeyring())
	require.NoError(t, s.Set("acct", "pw"))
	assert.FileExists(t, fallback)
	got, err := s.Get("acct")
	require.NoError(t, err)
	assert.Equal(t, "pw", got)
}
