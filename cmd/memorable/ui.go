package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/adminops"
	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/embed"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/scan"
	"github.com/constantine-kostikas/memorable/internal/store"
	"github.com/constantine-kostikas/memorable/internal/webui"
	"github.com/constantine-kostikas/memorable/internal/worker"
)

const (
	uiShutdownTimeout = 5 * time.Second
	uiPingTimeout     = 5 * time.Second
	pgInvalidPassword = "28P01"
)

var (
	errAdminPasswordRejected = errors.New("admin password rejected")
	errAdminConnect          = fmt.Errorf("admin connection failed: %w", memory.ErrUnavailable)
	errUIClosed              = errors.New("memorable ui is shutting down")
)

type uiFlags struct {
	admin     bool
	context   string
	noBrowser bool
	port      int
}

func newUICmd(d deps, flags *rootFlags) *cobra.Command {
	opts := &uiFlags{}
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Browse and manage memories in a local web interface",
		Long: "ui serves a web interface on 127.0.0.1 until interrupted. It prints a one-time launch URL to stderr " +
			"and opens it in the browser unless --no-browser is set. With --admin it prompts for the admin password " +
			"and unlocks quarantine review, purge, prune, and profile edits for 15 idle minutes.",
		GroupID: groupMemory,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUI(cmd, d, flags, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.admin, "admin", false, "prompt for the admin password and unlock admin actions")
	cmd.Flags().StringVar(&opts.context, "context", "", "context to open first (default: from the working directory)")
	cmd.Flags().BoolVar(&opts.noBrowser, "no-browser", false, "only print the launch URL; do not open a browser")
	cmd.Flags().IntVar(&opts.port, "port", 0, "port on 127.0.0.1 (default: a random free port)")
	return cmd
}

func runUI(cmd *cobra.Command, d deps, flags *rootFlags, opts *uiFlags) error {
	ctx := cmd.Context()
	if opts.port < 0 || opts.port > 65535 {
		return usageError{fmt.Errorf("invalid --port %d: use 0 to 65535", opts.port)}
	}
	cfg, cfgPath, err := loadConfig(d, flags.configPath)
	if err != nil {
		return err
	}
	contexts := slices.Sorted(maps.Keys(cfg.Contexts))
	if len(contexts) == 0 {
		return fmt.Errorf("no contexts configured; run `memorable setup`: %w", memory.ErrNoContext)
	}
	initial, err := initialUIContext(cfg, contexts, opts.context, d)
	if err != nil {
		return err
	}
	logger := newLogger(cmd.ErrOrStderr(), d.getenv)

	backends := newUIBackends(contexts, func(ctx context.Context, name string) (*app, error) {
		return d.buildFor(ctx, cfgPath, name, logger, map[string]any{"via": "ui"})
	}, logger)
	defer backends.close()

	srv, err := newUIServer(cmd, d, cfg, contexts, initial, opts.admin, backends, logger)
	if err != nil {
		return err
	}
	defer srv.Close()

	ln, err := listenLoopback(opts.port)
	if err != nil {
		return err
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return fmt.Errorf("unexpected listener address %s", ln.Addr())
	}
	port := addr.Port
	httpSrv := &http.Server{
		Handler:           srv.Handler(port),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	launch := srv.LaunchURL(port)
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "memorable ui is running at %s\nPress Ctrl-C to stop.\n", launch); err != nil {
		logger.WarnContext(ctx, "print launch URL", "error", err)
	}
	if !opts.noBrowser {
		dir, err := launchInBrowser(d.openBrowser, launch, logger)
		if err != nil {
			logger.WarnContext(ctx, "could not prepare the browser launch file; open the URL above", "error", err)
		}
		defer removeLaunchDir(dir, logger)
	}

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		runErr = fmt.Errorf("web server stopped: %w", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uiShutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.WarnContext(ctx, "graceful shutdown timed out; closing connections", "error", err)
		_ = httpSrv.Close()
	}
	backends.close()
	srv.Close()
	return runErr
}

// initialUIContext picks --context, else the working directory's context, else the first configured one.
func initialUIContext(cfg *config.Config, contexts []string, flag string, d deps) (string, error) {
	if flag != "" {
		if !slices.Contains(contexts, flag) {
			return "", usageError{fmt.Errorf("context %q is not configured", flag)}
		}
		return flag, nil
	}
	if cwd, err := d.cwd(); err == nil {
		if res, err := resolveScope(cfg, []string{cwd}, d.getenv); err == nil && slices.Contains(contexts, res.Context) {
			return res.Context, nil
		}
	}
	return contexts[0], nil
}

// newUIServer prompts for and verifies the admin password when asked, and builds the server. The
// password lives on only inside the admin opener, which webui.New takes over.
func newUIServer(cmd *cobra.Command, d deps, cfg *config.Config, contexts []string, initial string, admin bool,
	backends *uiBackends, logger *slog.Logger,
) (*webui.Server, error) {
	var adminOpener func(ctx context.Context, name string) (webui.AdminOps, error)
	if admin {
		password, err := d.adminPassword(cmd)
		if err != nil {
			return nil, err
		}
		owner := ownerName(d.getenv)
		probe, err := openUIAdmin(cmd.Context(), cfg.Database, password, config.SchemaName(initial), owner)
		if err != nil {
			return nil, err
		}
		probe.Close()
		adminOpener = newUIAdminOpener(cfg, password, owner)
	}
	return webui.New(webui.Config{ //nolint:wrapcheck // webui errors are already contextual
		Contexts: contexts,
		Initial:  initial,
		Backend:  backends.get,
		Admin:    adminOpener,
		Logger:   logger,
	})
}

// uiAdminOps adds the Close the web UI needs to release the admin pool.
type uiAdminOps struct {
	*adminops.Ops
	admin *store.Admin
}

var _ webui.AdminOps = (*uiAdminOps)(nil)

func (o *uiAdminOps) Close() { o.admin.Close() }

func newUIAdminOpener(cfg *config.Config, password, owner string) func(ctx context.Context, name string) (webui.AdminOps, error) {
	return func(ctx context.Context, name string) (webui.AdminOps, error) {
		if _, ok := cfg.Contexts[name]; !ok {
			return nil, fmt.Errorf("context %q is not configured: %w", name, memory.ErrNoContext)
		}
		admin, err := openUIAdmin(ctx, cfg.Database, password, config.SchemaName(name), owner)
		if err != nil {
			return nil, err
		}
		sec, err := scan.NewSecretScanner()
		if err != nil {
			admin.Close()
			return nil, fmt.Errorf("secret scanner: %w", err)
		}
		var emb memory.Embedder
		if ollama, err := embed.NewOllama(cfg.Embedding, nil); err == nil {
			emb = ollama
		}
		return &uiAdminOps{Ops: adminops.New(admin, emb, sec, cfg.Retention.AccessEventsDays, time.Now), admin: admin}, nil
	}
}

// openUIAdmin opens and pings an admin pool for schema. Its errors are fixed messages: driver errors
// can name the role, host, and database, and nothing about the admin connection belongs in a log.
func openUIAdmin(ctx context.Context, db config.Database, password, schema, owner string) (*store.Admin, error) {
	admin, err := store.OpenAdmin(ctx, db, password, schema, owner, store.WithAuditDetails(map[string]any{"via": "ui"}))
	if err != nil {
		return nil, errAdminConnect
	}
	pingCtx, cancel := context.WithTimeout(ctx, uiPingTimeout)
	defer cancel()
	if err := admin.Ping(pingCtx); err != nil {
		admin.Close()
		if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == pgInvalidPassword {
			return nil, errAdminPasswordRejected
		}
		return nil, errAdminConnect
	}
	return admin, nil
}

// uiBackends opens one app per context on first use and caches it; failed opens are not cached.
type uiBackends struct {
	contexts []string
	open     func(ctx context.Context, name string) (*app, error)
	logger   *slog.Logger
	runCtx   context.Context //nolint:containedctx // workers outlive the requests that open their backends
	cancel   context.CancelFunc
	workers  sync.WaitGroup

	mu      sync.Mutex
	opening map[string]*sync.Mutex
	apps    map[string]*app
	closed  bool
}

func newUIBackends(contexts []string, open func(ctx context.Context, name string) (*app, error), logger *slog.Logger) *uiBackends {
	runCtx, cancel := context.WithCancel(context.Background())
	return &uiBackends{
		contexts: slices.Clone(contexts), open: open, logger: logger, runCtx: runCtx, cancel: cancel,
		opening: map[string]*sync.Mutex{}, apps: map[string]*app{},
	}
}

// get returns the backend of the named context. Opens are serialized per context only, so a slow or
// broken context never delays another.
func (b *uiBackends) get(ctx context.Context, name string) (webui.Backend, error) {
	if !slices.Contains(b.contexts, name) {
		return webui.Backend{}, fmt.Errorf("context %q is not configured: %w", name, memory.ErrNoContext)
	}
	lock, a, err := b.cached(name)
	if a != nil || err != nil {
		return backendOf(a), err
	}
	lock.Lock()
	defer lock.Unlock()
	if _, a, err := b.cached(name); a != nil || err != nil {
		return backendOf(a), err
	}
	a, err = b.open(ctx, name)
	if err != nil {
		return webui.Backend{}, redactOpenError(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		a.Close()
		return webui.Backend{}, errUIClosed
	}
	b.apps[name] = a
	b.startWorker(a)
	return backendOf(a), nil
}

func (b *uiBackends) cached(name string) (*sync.Mutex, *app, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, errUIClosed
	}
	if a, ok := b.apps[name]; ok {
		return nil, a, nil
	}
	lock, ok := b.opening[name]
	if !ok {
		lock = &sync.Mutex{}
		b.opening[name] = lock
	}
	return lock, nil, nil
}

func backendOf(a *app) webui.Backend {
	if a == nil {
		return webui.Backend{}
	}
	return webui.Backend{Memories: a.svc, Store: a.store}
}

// startWorker runs the embedding worker for a, like serve does. Callers hold b.mu.
func (b *uiBackends) startWorker(a *app) {
	if a.store == nil {
		return
	}
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		if _, err := a.store.RequeueMissing(b.runCtx); err != nil {
			b.logger.WarnContext(b.runCtx, "requeue missing embeddings failed", "error", err.Error())
		}
		_ = worker.New(a.store, a.store, a.emb, b.logger).Run(b.runCtx)
	}()
}

// close stops the workers, waits for them, and closes every app. It is safe to call more than once.
func (b *uiBackends) close() {
	b.mu.Lock()
	b.closed = true
	apps := b.apps
	b.apps = map[string]*app{}
	b.mu.Unlock()
	b.cancel()
	b.workers.Wait()
	for _, a := range apps {
		a.Close()
	}
}

// redactOpenError keeps only the error class: open errors are logged and must not carry connection details.
func redactOpenError(err error) error {
	for _, class := range []error{memory.ErrNoContext, memory.ErrUnavailable} {
		if errors.Is(err, class) {
			return fmt.Errorf("opening the context failed: %w", class)
		}
	}
	return fmt.Errorf("opening the context failed (%T)", err)
}

// listenLoopback listens on 127.0.0.1 only; port 0 picks a free port.
func listenLoopback(port int) (net.Listener, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("listening on 127.0.0.1:%d: %w", port, err)
	}
	return ln, nil
}

// openBrowserWith opens a local file in the default browser. It is given a file path, never the token
// URL, because command-line arguments are readable by every local user.
func openBrowserWith(run func(ctx context.Context, name string, args ...string) ([]byte, error), goos string) func(path string) error {
	return func(path string) error {
		var name string
		switch goos {
		case "linux":
			name = "xdg-open"
		case "darwin":
			name = "open"
		default:
			return fmt.Errorf("opening a browser is not supported on %s", goos)
		}
		_, err := run(context.Background(), name, path)
		return err
	}
}

// launchInBrowser writes a private redirect file to the launch URL and opens it in the background,
// so a browser launcher that stays in the foreground never holds up the UI. It returns the
// directory to remove on shutdown.
func launchInBrowser(openBrowser func(path string) error, launchURL string, logger *slog.Logger) (string, error) {
	dir, path, err := writeLaunchFile(launchURL)
	if err != nil {
		return dir, err
	}
	go func() {
		if err := openBrowser(path); err != nil {
			logger.Warn("could not open a browser; open the URL above", "error", err)
		}
	}()
	return dir, nil
}

func writeLaunchFile(launchURL string) (string, string, error) {
	dir, err := os.MkdirTemp("", "memorable-ui-")
	if err != nil {
		return "", "", fmt.Errorf("creating launch directory: %w", err)
	}
	path := filepath.Join(dir, "launch.html")
	page := `<!doctype html><meta charset="utf-8"><title>memorable</title>` +
		`<meta http-equiv="refresh" content="0;url=` + html.EscapeString(launchURL) + `">` + "\n"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: path is inside the directory just created by os.MkdirTemp
	if err != nil {
		return dir, "", fmt.Errorf("creating launch file: %w", err)
	}
	if _, err := f.WriteString(page); err != nil {
		_ = f.Close()
		return dir, "", fmt.Errorf("writing launch file: %w", err)
	}
	if err := f.Close(); err != nil {
		return dir, "", fmt.Errorf("writing launch file: %w", err)
	}
	return dir, path, nil
}

func removeLaunchDir(dir string, logger *slog.Logger) {
	if dir == "" {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		logger.Warn("remove launch file", "error", err)
	}
}
