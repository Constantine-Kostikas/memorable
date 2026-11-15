package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/embed"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/scan"
	"github.com/constantine-kostikas/memorable/internal/scope"
	"github.com/constantine-kostikas/memorable/internal/secret"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/update"
)

// app is a fully wired session: config, resolved scope, store, and service.
type app struct {
	cfg     *config.Config
	cfgPath string
	res     scope.Resolution
	store   *store.Store
	emb     memory.Embedder
	svc     *memory.Service
	secrets *secret.Store
}

func (a *app) Close() {
	if a.store != nil {
		a.store.Close()
	}
}

// deps holds process-level dependencies so commands can be tested without real infrastructure.
type deps struct {
	getenv        func(string) string
	cwd           func() (string, error)
	build         func(ctx context.Context, cfgPath string, dirs []string, logger *slog.Logger) (*app, error)
	buildFor      func(ctx context.Context, cfgPath, contextName string, logger *slog.Logger, details map[string]any) (*app, error)
	isTTY         func(*cobra.Command) bool
	adminPassword func(*cobra.Command) (string, error)
	docker        func(ctx context.Context, args ...string) ([]byte, error)
	run           func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookPath      func(string) (string, error)
	executable    func() (string, error)
	goos          string
	selfUpdate    func(cmd *cobra.Command, current string, opts update.Options) (update.Result, error)
	openBrowser   func(path string) error
}

func defaultDeps() deps {
	d := deps{getenv: os.Getenv, cwd: os.Getwd, isTTY: stdinIsTTY, docker: runDocker, run: runCommand,
		lookPath: exec.LookPath, executable: os.Executable, goos: runtime.GOOS}
	d.adminPassword = func(cmd *cobra.Command) (string, error) {
		return promptAdminPassword(cmd.InOrStdin(), cmd.ErrOrStderr(), d.isTTY(cmd))
	}
	d.build = func(ctx context.Context, cfgPath string, dirs []string, logger *slog.Logger) (*app, error) {
		return buildApp(ctx, d, cfgPath, dirs, logger)
	}
	d.buildFor = func(ctx context.Context, cfgPath, contextName string, logger *slog.Logger, details map[string]any) (*app, error) {
		return buildAppFor(ctx, d, cfgPath, contextName, logger, details)
	}
	d.openBrowser = openBrowserWith(d.run, d.goos)
	d.selfUpdate = func(cmd *cobra.Command, current string, opts update.Options) (update.Result, error) {
		return runSelfUpdate(d, cmd, current, opts)
	}
	return d
}

var processStart = time.Now()

func newLogger(w io.Writer, getenv func(string) string) *slog.Logger {
	level := slog.LevelInfo
	if strings.EqualFold(getenv("MEMORABLE_LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// resolveConfigPath applies --config > MEMORABLE_CONFIG > platform default.
func resolveConfigPath(flag string, getenv func(string) string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if p := getenv("MEMORABLE_CONFIG"); p != "" {
		return p, nil
	}
	return config.DefaultPath() //nolint:wrapcheck // already contextual
}

func loadConfig(d deps, flag string) (*config.Config, string, error) {
	path, err := resolveConfigPath(flag, d.getenv)
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path, d.getenv)
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already contextual
	}
	return cfg, path, nil
}

func resolveScope(cfg *config.Config, dirs []string, getenv func(string) string) (scope.Resolution, error) {
	forced := getenv("MEMORABLE_CONTEXT")
	if forced == "" {
		return scope.Resolve(cfg, dirs) //nolint:wrapcheck // sentinel errors pass through
	}
	if _, ok := cfg.Contexts[forced]; !ok {
		return scope.Resolution{}, fmt.Errorf("MEMORABLE_CONTEXT %q is not a configured context: %w", forced, memory.ErrNoContext)
	}
	res := scope.Resolution{Context: forced}
	if len(dirs) > 0 {
		if p, err := scope.ProjectID(dirs[0]); err == nil {
			res.Project = &p
		}
	}
	return res, nil
}

func secretStore(getenv func(string) string) (*secret.Store, error) {
	path, err := secret.DefaultFallbackPath()
	if err != nil {
		return nil, err //nolint:wrapcheck // already contextual
	}
	if strings.EqualFold(getenv("MEMORABLE_KEYRING"), "off") {
		return secret.New(path, secret.WithoutKeyring()), nil
	}
	return secret.New(path), nil
}

func appAccount(db config.Database) string {
	return secret.Account(db.AppUser, db.Host, db.Port, db.Name)
}

func ownerName(getenv func(string) string) string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if u := getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}

// appSpec is what differs between a session resolved from directories and one opened for a named context.
type appSpec struct {
	resolve      func(cfg *config.Config) (scope.Resolution, error)
	cwd          string
	process      string
	auditDetails map[string]any
}

func buildApp(ctx context.Context, d deps, cfgPath string, dirs []string, logger *slog.Logger) (*app, error) {
	cwd := ""
	if len(dirs) > 0 {
		cwd = dirs[len(dirs)-1]
	}
	return openApp(ctx, d, cfgPath, logger, appSpec{
		resolve: func(cfg *config.Config) (scope.Resolution, error) { return resolveScope(cfg, dirs, d.getenv) },
		cwd:     cwd,
		process: processID(),
	})
}

// buildAppFor opens the named context read-write with no project, as the web UI does for every
// configured context, including the fallback context.
func buildAppFor(ctx context.Context, d deps, cfgPath, contextName string, logger *slog.Logger, details map[string]any) (*app, error) {
	return openApp(ctx, d, cfgPath, logger, appSpec{
		resolve: func(cfg *config.Config) (scope.Resolution, error) {
			if _, ok := cfg.Contexts[contextName]; !ok {
				return scope.Resolution{}, fmt.Errorf("context %q is not configured: %w", contextName, memory.ErrNoContext)
			}
			return scope.Resolution{Context: contextName}, nil
		},
		process:      "ui:" + processID(),
		auditDetails: details,
	})
}

func processID() string {
	return fmt.Sprintf("%d@%s", os.Getpid(), processStart.UTC().Format(time.RFC3339))
}

func openApp(ctx context.Context, d deps, cfgPath string, logger *slog.Logger, spec appSpec) (*app, error) {
	cfg, path, err := loadConfig(d, cfgPath)
	if err != nil {
		return nil, err
	}
	res, err := spec.resolve(cfg)
	if err != nil {
		return nil, err
	}
	secrets, err := secretStore(d.getenv)
	if err != nil {
		return nil, err
	}
	password, err := secrets.Get(appAccount(cfg.Database))
	if err != nil {
		return nil, fmt.Errorf("%w: no app password stored; run `memorable setup`: %w", memory.ErrUnavailable, err)
	}
	owner := ownerName(d.getenv)
	st, err := store.Open(ctx, cfg.Database, password, config.SchemaName(res.Context), owner)
	if err != nil {
		return nil, err //nolint:wrapcheck // already contextual
	}
	if err := st.Ping(ctx); err != nil {
		st.Close()
		return nil, err //nolint:wrapcheck // already an ErrUnavailable
	}
	emb, err := embed.NewOllama(cfg.Embedding, nil)
	if err != nil {
		st.Close()
		return nil, err //nolint:wrapcheck // already contextual
	}
	sec, err := scan.NewSecretScanner()
	if err != nil {
		st.Close()
		return nil, err //nolint:wrapcheck // already contextual
	}
	svc := memory.NewService(st, st, emb, sec, scan.Injection, memory.Settings{
		Owner: owner, Scope: res, Search: cfg.Search, Preload: cfg.Preload,
		WritesPerMinute: cfg.Limits.WritesPerMinute, Cwd: spec.cwd,
		Process: spec.process, AuditDetails: spec.auditDetails,
	})
	logger.DebugContext(ctx, "session ready", "context", res.Context, "read_only", res.ReadOnly)
	return &app{cfg: cfg, cfgPath: path, res: res, store: st, emb: emb, svc: svc, secrets: secrets}, nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed binaries chosen by memorable, arguments without a shell
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
