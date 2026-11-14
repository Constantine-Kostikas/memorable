package main

import (
	"context"
	"log/slog"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/mcpserver"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/worker"
)

const rootsWait = 5 * time.Second

// session resolves the service lazily: after the client's roots are known, and again after a failure.
type session struct {
	build   func(ctx context.Context, dirs []string) (*app, error)
	cwd     func() (string, error)
	logger  *slog.Logger
	runCtx  context.Context
	startWk func(*app)

	ready     chan struct{}
	readyOnce sync.Once
	mu        sync.Mutex
	roots     []string
	app       *app
}

func (s *session) markReady() { s.readyOnce.Do(func() { close(s.ready) }) }

func (s *session) onInitialized(_ context.Context, req *mcp.InitializedRequest) {
	params := req.Session.InitializeParams()
	//nolint:staticcheck // roots are deprecated (SEP-2577) but remain the only signal for the client's project directory
	if params == nil || params.Capabilities == nil || params.Capabilities.RootsV2 == nil {
		s.markReady()
		return
	}
	// Listing roots is a request back to the client; it must not block the message loop.
	go func() {
		defer s.markReady()
		ctx, cancel := context.WithTimeout(s.runCtx, 2*time.Second)
		defer cancel()
		res, err := req.Session.ListRoots(ctx, nil) //nolint:staticcheck // roots are deprecated but still the only cwd signal
		if err != nil {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, r := range res.Roots {
			if u, err := url.Parse(r.URI); err == nil && u.Scheme == "file" && u.Path != "" {
				s.roots = append(s.roots, u.Path)
			}
		}
	}()
}

func (s *session) service(ctx context.Context) (*memory.Service, error) {
	select {
	case <-s.ready:
	case <-time.After(rootsWait):
	case <-ctx.Done():
		return nil, ctx.Err() //nolint:wrapcheck // cancellation passes through
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.app != nil {
		return s.app.svc, nil
	}
	cwd, _ := s.cwd()
	dirs := append(slices.Clone(s.roots), cwd)
	a, err := s.build(s.runCtx, dirs)
	if err != nil {
		s.logger.WarnContext(ctx, "memorable startup failed", "error", err.Error())
		return nil, err
	}
	s.app = a
	if !a.res.ReadOnly && s.startWk != nil {
		s.startWk(a)
	}
	return a.svc, nil
}

func newServeCmd(d deps, flags *rootFlags, info buildInfo) *cobra.Command {
	return &cobra.Command{
		Use:     "serve",
		Short:   "Run the MCP server over stdio (Claude Code starts this)",
		GroupID: groupSetup,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			logger := newLogger(cmd.ErrOrStderr(), d.getenv)
			var wg sync.WaitGroup
			s := &session{cwd: d.cwd, logger: logger, runCtx: ctx, ready: make(chan struct{})}
			s.build = func(ctx context.Context, dirs []string) (*app, error) {
				return d.build(ctx, flags.configPath, dirs, logger)
			}
			s.startWk = func(a *app) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_ = worker.New(a.store, a.store, a.emb, logger).Run(ctx)
				}()
			}
			srv := mcpserver.New(s.service, mcpserver.Info{Version: info.Version, OnInitialized: s.onInitialized}, logger)
			err := srv.Run(ctx, &mcp.StdioTransport{})
			wg.Wait()
			s.mu.Lock()
			if s.app != nil {
				s.app.Close()
			}
			s.mu.Unlock()
			return err //nolint:wrapcheck // already contextual
		},
	}
}
