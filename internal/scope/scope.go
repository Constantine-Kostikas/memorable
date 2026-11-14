package scope

import (
	"path/filepath"
	"strings"

	"github.com/constantine-kostikas/memorable/internal/config"
)

// Resolution is the context and project a session is bound to.
type Resolution struct {
	Context  string
	ReadOnly bool
	Project  *string
}

// Resolve maps directories (MCP roots first, then cwd) to a context.
// The longest matching context path wins; unmatched directories use the read-only fallback when configured.
func Resolve(cfg *config.Config, dirs []string) (Resolution, error) {
	if len(dirs) == 0 {
		return Resolution{}, ErrNoContext
	}
	res := Resolution{}
	if project, err := ProjectID(dirs[0]); err == nil {
		res.Project = &project
	}
	for _, d := range dirs {
		if name, ok := match(cfg, realPath(d)); ok {
			res.Context = name
			return res, nil
		}
	}
	if cfg.FallbackContext != "" {
		res.Context = cfg.FallbackContext
		res.ReadOnly = true
		return res, nil
	}
	return Resolution{}, ErrNoContext
}

func match(cfg *config.Config, dir string) (string, bool) {
	best, bestLen := "", -1
	for _, name := range cfg.ContextNames() {
		for _, p := range cfg.Contexts[name].Paths {
			rp := realPath(p)
			if within(dir, rp) && len(rp) > bestLen {
				best, bestLen = name, len(rp)
			}
		}
	}
	return best, bestLen >= 0
}

func within(dir, root string) bool {
	if dir == root {
		return true
	}
	return strings.HasPrefix(dir, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

func realPath(p string) string {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	return clean
}
