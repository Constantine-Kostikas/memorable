package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
)

const (
	sessionCookieName = "memorable_session"
	secretBytes       = 32
)

// session holds the launch secrets of one `memorable ui` run: the single-use launch token,
// the session cookie value it is exchanged for, and the session's CSRF token.
type session struct {
	token  string
	cookie string
	csrf   string

	mu     sync.Mutex
	used   bool
	closed bool
}

func newSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newSession() (*session, error) {
	var secrets [3]string
	for i := range secrets {
		v, err := newSecret()
		if err != nil {
			return nil, err
		}
		secrets[i] = v
	}
	return &session{token: secrets[0], cookie: secrets[1], csrf: secrets[2]}, nil
}

func secretEqual(given, want string) bool {
	return subtle.ConstantTimeCompare([]byte(given), []byte(want)) == 1
}

// exchange consumes the launch token. Only the first matching call returns true.
func (s *session) exchange(given string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used || s.closed || !secretEqual(given, s.token) {
		return false
	}
	s.used = true
	return true
}

func (s *session) active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used && !s.closed
}

// authenticated reports whether r carries the session cookie. Every cookie with the session name
// is checked, because another server on 127.0.0.1 can plant a same-name cookie that sorts first.
func (s *session) authenticated(r *http.Request) bool {
	if !s.active() {
		return false
	}
	for _, c := range r.CookiesNamed(sessionCookieName) {
		if secretEqual(c.Value, s.cookie) {
			return true
		}
	}
	return false
}

func (s *session) csrfValid(given string) bool {
	return given != "" && secretEqual(given, s.csrf)
}

func (s *session) sessionCookie() *http.Cookie {
	return &http.Cookie{ //nolint:gosec // G124: the UI is plain HTTP on loopback, so Secure is not set; HttpOnly and SameSite=Strict are.
		Name:     sessionCookieName,
		Value:    s.cookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

func (s *session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}
