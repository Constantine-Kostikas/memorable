package scope

import (
	"context"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const gitTimeout = 2 * time.Second

// ProjectID identifies the project a directory belongs to: the normalized origin remote,
// else the repository root, else the directory itself.
func ProjectID(dir string) (string, error) {
	dir = realPath(dir)
	if remote, err := git(dir, "remote", "get-url", "origin"); err == nil && remote != "" {
		if id, ok := normalizeRemote(remote); ok {
			return id, nil
		}
	}
	if top, err := git(dir, "rev-parse", "--show-toplevel"); err == nil && top != "" {
		return realPath(top), nil
	}
	return dir, nil
}

func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	//nolint:gosec // fixed binary, arguments passed separately without a shell
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err //nolint:wrapcheck // callers only test for failure
}

func normalizeRemote(remote string) (string, bool) {
	var host, path string
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" && u.Host != "" {
		host, path = u.Hostname(), u.Path
	} else if at := strings.Index(remote, "@"); at >= 0 && strings.Contains(remote[at:], ":") {
		rest := remote[at+1:]
		colon := strings.Index(rest, ":")
		host, path = rest[:colon], rest[colon+1:]
	} else {
		return "", false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return "", false
	}
	return strings.ToLower(host) + "/" + filepath.ToSlash(path), true
}
