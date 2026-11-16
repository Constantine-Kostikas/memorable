package webui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const (
	adminLockedMessage = "admin locked; restart with memorable ui --admin"
	pagePrune          = "prune"
)

var (
	errAdminLocked      = errors.New("admin locked")
	errAdminUnavailable = errors.New("admin unavailable")
)

type adminOpener func(ctx context.Context, name string) (AdminOps, error)

// adminLock owns the --admin unlock: the opener (whose closure holds the admin password) and one
// cached AdminOps per context. Every admin use must come within idle of the previous one; the first
// use after that drops the opener and every cached handle, so admin stays locked until a restart.
type adminLock struct {
	idle time.Duration
	now  func() time.Time

	// opening serializes opener calls so two first requests for a context never open two pools;
	// mu stays free meanwhile, so pages that only read the lock state never wait on a connection.
	opening sync.Mutex

	mu      sync.Mutex
	open    adminOpener
	lastUse time.Time
	ops     map[string]AdminOps
}

func newAdminLock(open adminOpener, idle time.Duration, now func() time.Time) *adminLock {
	return &adminLock{idle: idle, now: now, open: open, lastUse: now(), ops: map[string]AdminOps{}}
}

// acquire checks the idle limit, records the use, and returns the cached or newly opened admin
// handle for the context. Opener errors are returned as is; callers must not show or log them.
func (l *adminLock) acquire(ctx context.Context, name string) (AdminOps, error) {
	if ops, _, err := l.touch(name); ops != nil || err != nil {
		return ops, err
	}
	l.opening.Lock()
	defer l.opening.Unlock()
	ops, open, err := l.touch(name)
	if ops != nil || err != nil {
		return ops, err
	}
	ops, err = open(ctx, name)
	if err != nil {
		return nil, err
	}
	if ops == nil {
		return nil, errors.New("admin opener returned no operations")
	}
	if !l.store(name, ops) {
		closeAdminOps([]AdminOps{ops})
		return nil, errAdminLocked
	}
	return ops, nil
}

func (l *adminLock) touch(name string) (AdminOps, adminOpener, error) {
	ops, open, released, err := l.touchLocked(name)
	closeAdminOps(released)
	return ops, open, err
}

func (l *adminLock) touchLocked(name string) (AdminOps, adminOpener, []AdminOps, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open == nil {
		return nil, nil, nil, errAdminLocked
	}
	now := l.now()
	if now.Sub(l.lastUse) >= l.idle {
		return nil, nil, l.releaseLocked(), errAdminLocked
	}
	l.lastUse = now
	return l.ops[name], l.open, nil, nil
}

func (l *adminLock) store(name string, ops AdminOps) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open == nil {
		return false
	}
	l.ops[name] = ops
	return true
}

// status reports the whole minutes left before the unlock expires, rounded up, without counting
// as a use. A nil lock (no --admin) is neither unlocked nor locked.
func (l *adminLock) status() (minutes int, locked bool) {
	if l == nil {
		return 0, false
	}
	left, released := l.remaining()
	closeAdminOps(released)
	if left <= 0 {
		return 0, true
	}
	return int((left + time.Minute - 1) / time.Minute), false
}

func (l *adminLock) remaining() (time.Duration, []AdminOps) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open == nil {
		return 0, nil
	}
	left := l.idle - l.now().Sub(l.lastUse)
	if left <= 0 {
		return 0, l.releaseLocked()
	}
	return left, nil
}

func (l *adminLock) minutesLeft() int {
	minutes, _ := l.status()
	return minutes
}

// release locks admin for good: the opener and every cached handle become unreachable.
func (l *adminLock) release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	released := l.releaseLocked()
	l.mu.Unlock()
	closeAdminOps(released)
}

func (l *adminLock) releaseLocked() []AdminOps {
	released := slices.Collect(maps.Values(l.ops))
	l.open, l.ops = nil, nil
	return released
}

func closeAdminOps(released []AdminOps) {
	for _, ops := range released {
		ops.Close()
	}
}

type adminHandler func(w http.ResponseWriter, r *http.Request, rc requestContext, ops AdminOps)

func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	routes := map[string]adminHandler{
		"POST /c/{context}/memories/{id}/approve": s.handleApprove,
		"POST /c/{context}/memories/{id}/purge":   s.handlePurge,
		"GET /c/{context}/prune":                  s.handlePrunePage,
		"POST /c/{context}/prune":                 s.handlePrune,
		"POST /c/{context}/profile":               s.handleProfileAdd,
		"POST /c/{context}/profile/{id}/update":   s.handleProfileUpdate,
		"POST /c/{context}/profile/{id}/delete":   s.handleProfileDelete,
	}
	for pattern, h := range routes {
		if s.admin == nil {
			mux.HandleFunc(pattern, s.adminRouteAbsent)
			continue
		}
		mux.HandleFunc(pattern, s.inContext(s.withAdmin(h)))
	}
}

// adminRouteAbsent answers admin routes without --admin. It is registered explicitly because
// "POST /c/{context}/profile" would otherwise be a 405 next to the profile page's GET route.
func (s *Server) adminRouteAbsent(w http.ResponseWriter, r *http.Request) {
	s.renderErrorFor(w, r, http.StatusNotFound, s.authenticatedPage("Not found", "Page not found."))
}

func (s *Server) withAdmin(h adminHandler) func(http.ResponseWriter, *http.Request, requestContext) {
	return func(w http.ResponseWriter, r *http.Request, rc requestContext) {
		ops, err := s.adminOps(r, rc)
		if err != nil {
			s.adminUnavailable(w, r, rc, err)
			return
		}
		h(w, r, rc, ops)
	}
}

// adminOps acquires the admin handle of rc. An opener error may carry connection details, so only
// the context name and the error type are logged, and callers get errAdminUnavailable.
func (s *Server) adminOps(r *http.Request, rc requestContext) (AdminOps, error) {
	ops, err := s.admin.acquire(r.Context(), rc.Name)
	switch {
	case err == nil:
		return ops, nil
	case errors.Is(err, errAdminLocked):
		return nil, errAdminLocked
	}
	s.logger.ErrorContext(r.Context(), "open admin access failed", "context", rc.Name, "error_type", fmt.Sprintf("%T", err))
	return nil, errAdminUnavailable
}

func adminUnavailableMessage(contextName string) string {
	return "Admin access to the " + contextName + " context could not be opened. Run memorable doctor."
}

// adminUnavailable answers a locked or unopenable admin request. htmx does not swap a 403, so a
// locked fragment request also gets HX-Refresh: the reloaded page shows the lock banner.
func (s *Server) adminUnavailable(w http.ResponseWriter, r *http.Request, rc requestContext, err error) {
	status, title, message := http.StatusServiceUnavailable, "Admin unavailable", adminUnavailableMessage(rc.Name)
	if errors.Is(err, errAdminLocked) {
		status, title, message = http.StatusForbidden, "Admin locked", adminLockedMessage
	}
	w.Header().Set("Vary", "HX-Request")
	if isFragmentRequest(r) {
		if status == http.StatusForbidden {
			w.Header().Set("HX-Refresh", "true")
		}
		s.renderStatus(w, status, "error.html", pageData{Title: title, Message: message})
		return
	}
	s.renderError(w, status, s.contextPage(r, rc, title, message))
}

type quarantinedItem struct {
	Base      string
	ID        string
	Title     string
	Slug      string
	Confirm   string
	Source    string
	Type      string
	Project   string
	CreatedAt string
	Content   string
}

type quarantinedView struct {
	Context string
	CSRF    string
	Items   []quarantinedItem
	Notice  string
	Error   string
}

func (s *Server) loadQuarantined(r *http.Request, rc requestContext, ops AdminOps) quarantinedView {
	view := quarantinedView{Context: rc.Name, CSRF: s.session.csrf}
	mems, err := ops.QuarantinedList(r.Context())
	if err != nil {
		_, view.Error = s.actionStatus(r, fmt.Errorf("list quarantined: %w", err))
		return view
	}
	view.Items = make([]quarantinedItem, 0, len(mems))
	for _, m := range mems {
		item := quarantinedItem{
			Base: memoryBase(rc.Name, m.ID), ID: m.ID.String(), Title: m.Title, Confirm: purgeConfirmation(m),
			Source: m.Source.String(), Type: m.Type.String(), Project: projectOf(m), CreatedAt: formatTime(m.CreatedAt), Content: m.Content,
		}
		if m.Slug != nil {
			item.Slug = *m.Slug
		}
		view.Items = append(view.Items, item)
	}
	return view
}

// purgeConfirmation is what the user must type to purge m: its slug, or its id without one.
func purgeConfirmation(m memory.Memory) string {
	if m.Slug != nil && *m.Slug != "" {
		return *m.Slug
	}
	return m.ID.String()
}

func (s *Server) respondQuarantined(w http.ResponseWriter, r *http.Request, rc requestContext, ops AdminOps, notice string, actionErr error) {
	status, message := http.StatusOK, ""
	if actionErr != nil {
		status, message = s.actionStatus(r, actionErr)
		notice = ""
	}
	view := s.loadQuarantined(r, rc, ops)
	view.Notice = notice
	if message != "" {
		view.Error = message
	}
	w.Header().Set("Vary", "HX-Request")
	if isFragmentRequest(r) {
		s.renderStatus(w, status, "quarantined.html", view)
		return
	}
	page, err := s.reviewPage(r, rc)
	if err != nil {
		s.fail(w, r, rc, err)
		return
	}
	page.Review.Quarantined = &view
	s.renderStatus(w, status, "layout.html", page)
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request, rc requestContext, ops AdminOps) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.respondQuarantined(w, r, rc, ops, "", memory.ErrNotFound)
		return
	}
	mems, err := ops.QuarantinedList(r.Context())
	if err != nil {
		s.respondQuarantined(w, r, rc, ops, "", fmt.Errorf("list quarantined: %w", err))
		return
	}
	if !slices.ContainsFunc(mems, func(m memory.Memory) bool { return m.ID == id }) {
		s.respondQuarantined(w, r, rc, ops, "", fmt.Errorf("approve: %w", memory.ErrNotFound))
		return
	}
	if err := ops.Approve(r.Context(), id); err != nil {
		s.respondQuarantined(w, r, rc, ops, "", fmt.Errorf("approve: %w", err))
		return
	}
	s.respondQuarantined(w, r, rc, ops, "Memory approved.", nil)
}

func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request, rc requestContext, ops AdminOps) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.respondQuarantined(w, r, rc, ops, "", memory.ErrNotFound)
		return
	}
	target, err := s.purgeTarget(r, rc, ops, id)
	if err != nil {
		s.respondQuarantined(w, r, rc, ops, "", err)
		return
	}
	if strings.TrimSpace(r.PostFormValue("confirm")) != purgeConfirmation(target) {
		s.respondQuarantined(w, r, rc, ops, "", fmt.Errorf(
			"%w: to purge, type the memory's slug (or its id when it has no slug) in the confirmation field", memory.ErrValidation))
		return
	}
	if err := ops.Purge(r.Context(), id); err != nil {
		s.respondQuarantined(w, r, rc, ops, "", fmt.Errorf("purge: %w", err))
		return
	}
	s.respondQuarantined(w, r, rc, ops, "Memory purged.", nil)
}

// purgeTarget loads the memory named by id in rc: from the admin quarantine list, whose slugs the
// app role cannot read, or else through a passive app-role Get.
func (s *Server) purgeTarget(r *http.Request, rc requestContext, ops AdminOps, id uuid.UUID) (memory.Memory, error) {
	mems, err := ops.QuarantinedList(r.Context())
	if err != nil {
		return memory.Memory{}, fmt.Errorf("list quarantined: %w", err)
	}
	if i := slices.IndexFunc(mems, func(m memory.Memory) bool { return m.ID == id }); i >= 0 {
		return mems[i], nil
	}
	fetched, err := s.fetchPassive(r, rc, id)
	if errors.Is(err, memory.ErrQuarantined) {
		return memory.Memory{}, fmt.Errorf("purge: %w", memory.ErrNotFound)
	}
	if err != nil {
		return memory.Memory{}, err
	}
	return fetched.Memory, nil
}

type pruneView struct {
	Context string
	CSRF    string
	Action  string
	Count   int64
	Notice  string
	Error   string
}

func (s *Server) handlePrunePage(w http.ResponseWriter, r *http.Request, rc requestContext, ops AdminOps) {
	s.respondPrune(w, r, rc, ops, "", http.StatusOK, "")
}

func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request, rc requestContext, ops AdminOps) {
	deleted, err := ops.Prune(r.Context())
	if err != nil {
		status, message := s.actionStatus(r, fmt.Errorf("prune: %w", err))
		s.respondPrune(w, r, rc, ops, "", status, message)
		return
	}
	s.respondPrune(w, r, rc, ops, fmt.Sprintf("Deleted %d access events.", deleted), http.StatusOK, "")
}

func (s *Server) respondPrune(w http.ResponseWriter, r *http.Request, rc requestContext, ops AdminOps, notice string, status int, message string) {
	view := pruneView{Context: rc.Name, CSRF: s.session.csrf, Action: contextBase(rc.Name) + "/prune", Notice: notice, Error: message}
	count, err := ops.PruneCount(r.Context())
	if err != nil {
		status, view.Error = s.actionStatus(r, fmt.Errorf("prune count: %w", err))
	}
	view.Count = count
	w.Header().Set("Vary", "HX-Request")
	if isFragmentRequest(r) {
		s.renderStatus(w, status, "prune.html", view)
		return
	}
	page := s.contextPage(r, rc, "Prune", "")
	page.Page, page.Prune = pagePrune, &view
	s.renderStatus(w, status, "layout.html", page)
}
