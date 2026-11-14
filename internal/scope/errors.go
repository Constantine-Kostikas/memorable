// Package scope resolves which memory context and project a directory belongs to.
package scope

import "errors"

// ErrNoContext means no configured context (and no fallback) matches the directory.
var ErrNoContext = errors.New("no memory context for this directory")
