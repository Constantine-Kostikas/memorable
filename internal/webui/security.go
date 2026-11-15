package webui

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
)

const (
	contentSecurityPolicy = "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	csrfHeader            = "X-CSRF-Token"
	csrfField             = "csrf"
	maxFormBytes          = 1 << 20
	restartMessage        = "This link was already used or the session is missing: restart memorable ui to get a new link."
)

// secure wraps next in the middleware chain, outermost first: security headers, Host check,
// session check, cross-origin protection, and CSRF check. ServeMux patterns enforce methods.
func (s *Server) secure(port int, next http.Handler) http.Handler {
	h := s.requireCSRF(next)
	h = s.crossOriginProtection(h)
	h = s.requireSession(h)
	h = s.requireHost(port, h)
	return securityHeaders(h)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// requireHost defeats DNS rebinding: a rebound name reaches 127.0.0.1 with a foreign Host.
func (s *Server) requireHost(port int, next http.Handler) http.Handler {
	allowed := [...]string{fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("localhost:%d", port)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != allowed[0] && r.Host != allowed[1] {
			s.reject(w, r, http.StatusMisdirectedRequest, "Misdirected request",
				"Open memorable through the link it printed.", "host not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sessionExempt reports whether r may proceed without a session cookie: the token exchange at
// the root and the static assets. The path must be clean so that "/static/../x" is not exempt.
func sessionExempt(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if path.Clean(p) != p {
		return false
	}
	return p == "/" || strings.HasPrefix(p, "/static/")
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sessionExempt(r) && !s.session.authenticated(r) {
			s.forbidden(w, r, "missing or invalid session")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) crossOriginProtection(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.reject(w, r, http.StatusForbidden, "Forbidden", "Cross-origin requests are not allowed.", "cross-origin request")
	}))
	return cop.Handler(next)
}

// requireCSRF checks the per-session CSRF token on every state-changing request. The token
// comes from the X-CSRF-Token header (htmx) or the csrf field of a form body, never the URL.
func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		token := r.Header.Get(csrfHeader)
		if token == "" {
			if err := r.ParseForm(); err != nil {
				if maxErr := (*http.MaxBytesError)(nil); errors.As(err, &maxErr) {
					s.reject(w, r, http.StatusRequestEntityTooLarge, "Request too large",
						"The submitted form is too large.", "body too large")
					return
				}
				s.reject(w, r, http.StatusBadRequest, "Bad request", "The submitted form could not be read.", "malformed form")
				return
			}
			token = r.PostForm.Get(csrfField)
		}
		if !s.session.csrfValid(token) {
			s.reject(w, r, http.StatusForbidden, "Forbidden", "The form expired; reload the page and try again.", "csrf token mismatch")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) forbidden(w http.ResponseWriter, r *http.Request, reason string) {
	s.reject(w, r, http.StatusForbidden, "Session required", restartMessage, reason)
}

// reject logs reason and renders an error page without session data. It never logs the query
// string, cookies, or headers, which carry the launch token and session secrets.
func (s *Server) reject(w http.ResponseWriter, r *http.Request, status int, title, message, reason string) {
	s.logger.WarnContext(r.Context(), "request rejected",
		"reason", reason, "status", status, "method", r.Method, "path", r.URL.Path)
	s.renderError(w, status, pageData{Title: title, Message: message})
}
