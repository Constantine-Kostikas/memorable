package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	webuiImportSuffix = "internal/webui"
	repoRoot          = "../.."
)

var listenerSelectors = map[string][]string{
	"net":      {"Listen", "ListenTCP", "ListenUnix", "ListenPacket", "ListenUDP", "ListenIP", "ListenConfig", "ListenMulticastUDP"},
	"net/http": {"Server", "ListenAndServe", "ListenAndServeTLS", "Serve", "ServeTLS"},
}

func TestServeHasNoListener(t *testing.T) {
	t.Parallel()

	t.Run("mcpserver does not depend on webui", func(t *testing.T) {
		t.Parallel()
		cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{.ImportPath}}", "./internal/mcpserver")
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		deps := strings.Fields(string(out))
		require.NotEmpty(t, deps)
		for _, dep := range deps {
			assert.False(t, importsWebUI(dep), "internal/mcpserver depends on %s", dep)
		}
	})

	t.Run("only ui.go and webui open listeners", func(t *testing.T) {
		t.Parallel()
		found := listenerUses(t)
		require.NotEmpty(t, found, "the scan must see the listeners ui.go opens, or it is not scanning")
		for file, uses := range found {
			assert.True(t, mayListen(file), "%s opens a listener or HTTP server: %v", file, uses)
		}
		assert.Contains(t, found, "cmd/memorable/ui.go")
	})

	t.Run("serve.go imports neither net/http nor webui", func(t *testing.T) {
		t.Parallel()
		for _, imp := range importPaths(t, "serve.go") {
			assert.NotEqual(t, "net/http", imp)
			assert.False(t, importsWebUI(imp), "serve.go imports %s", imp)
		}
	})
}

func importsWebUI(importPath string) bool {
	return strings.HasSuffix(importPath, webuiImportSuffix) || strings.Contains(importPath, webuiImportSuffix+"/")
}

func mayListen(file string) bool {
	return file == "cmd/memorable/ui.go" || strings.HasPrefix(file, "internal/webui/")
}

func importPaths(t *testing.T, name string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
	require.NoError(t, err)
	paths := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		require.NoError(t, err)
		paths = append(paths, p)
	}
	return paths
}

// listenerUses maps each non-test Go file under cmd/memorable and internal, as a slash path from
// the repository root, to the listener and HTTP server references found in its syntax tree.
// Parsing the syntax tree ignores comments and string literals, and resolving import names catches aliases.
func listenerUses(t *testing.T) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	for _, dir := range []string{"cmd/memorable", "internal"} {
		root := filepath.Join(repoRoot, dir)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			file, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
			if err != nil {
				return err
			}
			if uses := fileListenerUses(file); len(uses) > 0 {
				found[rel] = uses
			}
			return nil
		})
		require.NoError(t, err)
	}
	return found
}

func fileListenerUses(file *ast.File) []string {
	local := map[string]string{}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || listenerSelectors[importPath] == nil {
			continue
		}
		name := path.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		local[name] = importPath
	}
	var uses []string
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if importPath, imported := local[pkg.Name]; imported && slices.Contains(listenerSelectors[importPath], sel.Sel.Name) {
			uses = append(uses, importPath+"."+sel.Sel.Name)
		}
		return true
	})
	return uses
}
