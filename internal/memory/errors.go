package memory

import (
	"errors"

	"github.com/constantine-kostikas/memorable/internal/scope"
)

// Sentinel errors returned by the memory service and its adapters.
var (
	ErrNotFound       = errors.New("memory not found")
	ErrConflict       = errors.New("memory changed concurrently")
	ErrValidation     = errors.New("invalid input")
	ErrSecretDetected = errors.New("secret detected")
	ErrQuarantined    = errors.New("memory is quarantined")
	ErrReadOnly       = errors.New("context is read-only")
	ErrUnavailable    = errors.New("dependency unavailable")
	ErrAuthFailed     = errors.New("database rejected the app password")
	ErrRateLimited    = errors.New("write rate limit reached")
	ErrSchemaBehind   = errors.New("database schema is behind; run `memorable migrate`")
	ErrNoContext      = scope.ErrNoContext
)
