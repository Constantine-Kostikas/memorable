package webui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/adminops"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/store"
)

// Memories is the memory.Service method set the UI calls.
type Memories interface {
	List(ctx context.Context, r memory.ListRequest) (memory.ListResponse, error)
	Search(ctx context.Context, r memory.SearchRequest) (memory.SearchResponse, error)
	Get(ctx context.Context, r memory.GetRequest) ([]memory.Fetched, error)
	History(ctx context.Context, id uuid.UUID) ([]memory.Version, error)
	Activity(ctx context.Context, id uuid.UUID) (memory.Activity, error)
	Update(ctx context.Context, id uuid.UUID, expected *int, p memory.Patch) (memory.Memory, error)
	Delete(ctx context.Context, id uuid.UUID) error
	Restore(ctx context.Context, id uuid.UUID, version *int) (memory.Memory, error)
	Feedback(ctx context.Context, id uuid.UUID, k memory.FeedbackKind, note string) error
	Link(ctx context.Context, l memory.Link, remove bool) error
	Supersede(ctx context.Context, old, replacement uuid.UUID) error
	Activate(ctx context.Context, id uuid.UUID) error
	Review(ctx context.Context) (memory.ReviewQueue, error)
}

// StoreReads is the read-only store access the UI needs beyond memory.Service.
type StoreReads interface {
	Stats(ctx context.Context) (store.Stats, error)
	ProfileList(ctx context.Context) ([]memory.Memory, error)
}

// AdminOps is the adminops.Ops method set the UI calls when started with --admin, plus Close,
// which must release the admin pool (wrap *adminops.Ops to add it) and is called once per handle.
type AdminOps interface {
	QuarantinedList(ctx context.Context) ([]memory.Memory, error)
	Approve(ctx context.Context, id uuid.UUID) error
	Purge(ctx context.Context, id uuid.UUID) error
	PruneCount(ctx context.Context) (int64, error)
	Prune(ctx context.Context) (int64, error)
	ProfileList(ctx context.Context) ([]memory.Memory, error)
	ProfileDelete(ctx context.Context, id uuid.UUID) error
	ProfileAdd(ctx context.Context, in adminops.ProfileInput) (memory.Memory, error)
	ProfileEdit(ctx context.Context, id uuid.UUID, in adminops.ProfileInput) (memory.Memory, error)
	Close()
}

var _ Memories = (*memory.Service)(nil)

// Backend is the per-context application access.
type Backend struct {
	Memories Memories
	Store    StoreReads
}

// Config configures a Server.
type Config struct {
	Contexts []string
	Initial  string
	// Backend opens or returns the application backend of the named context.
	Backend func(ctx context.Context, name string) (Backend, error)
	// Admin opens the admin operations of the named context; nil without --admin. The server
	// calls it at most once per context and drops it when the unlock expires; every returned
	// handle is closed then, or by Server.Close.
	Admin     func(ctx context.Context, name string) (AdminOps, error)
	AdminIdle time.Duration
	// OnExchange, if set, is called once, right after the launch token is exchanged for the
	// session cookie, so the caller can delete anything that still holds the spent token.
	OnExchange func()
	Now        func() time.Time
	Logger     *slog.Logger
}

// Server is the web UI for one `memorable ui` launch.
type Server struct {
	cfg       Config
	logger    *slog.Logger
	tmpl      *template.Template
	static    fs.FS
	session   *session
	admin     *adminLock
	closeOnce sync.Once
}

const defaultAdminIdle = 15 * time.Minute

// New validates cfg and returns a Server with fresh launch secrets.
func New(cfg Config) (*Server, error) {
	switch {
	case len(cfg.Contexts) == 0:
		return nil, errors.New("webui: no contexts configured")
	case !slices.Contains(cfg.Contexts, cfg.Initial):
		return nil, fmt.Errorf("webui: initial context %q is not configured", cfg.Initial)
	case cfg.Backend == nil:
		return nil, errors.New("webui: no backend opener")
	}
	cfg.Contexts = slices.Clone(cfg.Contexts)
	if cfg.AdminIdle <= 0 {
		cfg.AdminIdle = defaultAdminIdle
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	static, err := fs.Sub(assetFS, "static")
	if err != nil {
		return nil, fmt.Errorf("webui: static assets: %w", err)
	}
	sess, err := newSession()
	if err != nil {
		return nil, err
	}
	var admin *adminLock
	if cfg.Admin != nil {
		admin = newAdminLock(cfg.Admin, cfg.AdminIdle, cfg.Now)
		cfg.Admin = nil
	}
	return &Server{cfg: cfg, logger: cfg.Logger, tmpl: tmpl, static: static, session: sess, admin: admin}, nil
}

// Handler returns the UI handler for a listener on 127.0.0.1:port.
func (s *Server) Handler(port int) http.Handler {
	return s.handler(port, nil)
}

// handler builds the routed, secured handler; extra registers additional routes (tests only).
func (s *Server) handler(port int, extra func(*http.ServeMux)) http.Handler {
	cookieName := sessionCookieName(port)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleRoot(cookieName))
	mux.HandleFunc("GET /static/{file}", s.handleStatic)
	s.routes(mux)
	if extra != nil {
		extra(mux)
	}
	return s.secure(port, cookieName, mux)
}

// routes registers the page and action routes, all under /c/{context}/.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /c/{context}/memories", s.inContext(s.handleMemories))
	mux.HandleFunc("GET /c/{context}/review", s.inContext(s.handleReview))
	mux.HandleFunc("GET /c/{context}/profile", s.inContext(s.handleProfile))
	mux.HandleFunc("GET /c/{context}/stats", s.inContext(s.handleStats))
	s.registerMemoryRoutes(mux)
	s.registerAdminRoutes(mux)
}

// LaunchURL returns the one-time launch URL.
func (s *Server) LaunchURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", port, s.session.token)
}

// Close invalidates the session and locks admin for good. It is safe to call more than once.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.session.close()
		s.admin.release()
	})
}

func contextBase(name string) string {
	return "/c/" + url.PathEscape(name)
}

func (s *Server) contextHome(name string) string {
	return contextBase(name) + "/memories"
}

// handleRoot exchanges the launch token for the session cookie, or sends an existing session home.
// A successful exchange answers with an interstitial page instead of a redirect: the launch page
// is a file:// document, so a redirect would continue a cross-site chain and the browser would
// withhold the SameSite=Strict cookie, while a navigation started by this page is same-site.
func (s *Server) handleRoot(cookieName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		home := s.contextHome(s.cfg.Initial)
		if s.session.authenticated(r, cookieName) {
			http.Redirect(w, r, home, http.StatusSeeOther)
			return
		}
		if !s.session.exchange(r.URL.Query().Get("token")) {
			s.forbidden(w, r, "launch token missing, invalid, or already used")
			return
		}
		if s.cfg.OnExchange != nil {
			s.cfg.OnExchange()
		}
		http.SetCookie(w, s.session.sessionCookie(cookieName))
		s.renderTemplate(w, "launch.html", home)
	}
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".woff2") {
		w.Header().Set("Content-Type", "font/woff2")
	}
	http.ServeFileFS(w, r, s.static, name) //nolint:gosec // G703: s.static is the embedded read-only asset FS and name is a validated single path element.
}

type requestContext struct {
	Name    string
	Backend Backend
}

// inContext resolves the {context} path value on every request, so a response only ever uses
// the backend of the context named in its own URL. Unknown contexts are 404.
func (s *Server) inContext(h func(http.ResponseWriter, *http.Request, requestContext)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("context")
		if !slices.Contains(s.cfg.Contexts, name) {
			s.renderErrorFor(w, r, http.StatusNotFound, s.authenticatedPage("Not found", "Unknown context."))
			return
		}
		backend, err := s.cfg.Backend(r.Context(), name)
		if err != nil {
			s.logger.ErrorContext(r.Context(), "open context", "context", name, "error", err)
			page := s.authenticatedPage("Context unavailable",
				"The "+name+" context could not be opened; other contexts still work. Run memorable doctor.")
			page.Context = name
			s.renderErrorFor(w, r, http.StatusServiceUnavailable, page)
			return
		}
		h(w, r, requestContext{Name: name, Backend: backend})
	}
}

func (s *Server) authenticatedPage(title, message string) pageData {
	page := pageData{Title: title, Message: message, Contexts: s.cfg.Contexts, CSRF: s.session.csrf}
	minutes, locked := s.admin.status()
	page.AdminUnlockedMinutes = minutes
	if locked {
		page.AdminLockedMessage = adminLockedMessage
	}
	return page
}

// renderErrorFor answers an htmx swap with only the error box and any other request with the full page.
func (s *Server) renderErrorFor(w http.ResponseWriter, r *http.Request, status int, data pageData) {
	w.Header().Set("Vary", "HX-Request")
	if isFragmentRequest(r) {
		s.renderStatus(w, status, "error.html", data)
		return
	}
	s.renderError(w, status, data)
}

func (s *Server) renderError(w http.ResponseWriter, status int, data pageData) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		s.logger.Error("render error page", "error", err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
