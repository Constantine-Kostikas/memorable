package update

import (
	"context"
	"strings"
	"time"
)

// RunFunc runs a command without a shell and returns its stdout.
type RunFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Token returns a GitHub token from GH_TOKEN, GITHUB_TOKEN, or `gh auth token`, in that order.
// It returns "" when none is available, which works for public repositories. run is nil when gh is not installed.
func Token(ctx context.Context, getenv func(string) string, run RunFunc) string {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
	}
	if run == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := run(ctx, "gh", "auth", "token")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
