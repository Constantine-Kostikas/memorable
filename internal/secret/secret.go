// Package secret stores credentials in the OS keyring, falling back to an owner-only file.
package secret

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/zalando/go-keyring"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const service = "memorable"

// Store reads and writes secrets.
type Store struct {
	fallbackPath string
	noKeyring    bool
	mu           sync.Mutex
}

// Option configures a Store.
type Option func(*Store)

// WithoutKeyring stores secrets only in the fallback file (headless machines and tests).
func WithoutKeyring() Option { return func(s *Store) { s.noKeyring = true } }

// New returns a Store using fallbackPath when the keyring is unavailable.
func New(fallbackPath string, opts ...Option) *Store {
	s := &Store{fallbackPath: fallbackPath}
	for _, o := range opts {
		o(s)
	}
	return s
}

// DefaultFallbackPath returns the platform path for the credentials fallback file.
func DefaultFallbackPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating config dir: %w", err)
	}
	return filepath.Join(dir, "memorable", "credentials.json"), nil
}

// Account builds the account key for a database role.
func Account(user, host string, port int, db string) string {
	return user + "@" + host + ":" + strconv.Itoa(port) + "/" + db
}

// Get returns the secret for account or memory.ErrNotFound.
func (s *Store) Get(account string) (string, error) {
	if !s.noKeyring {
		if v, err := keyring.Get(service, account); err == nil {
			return v, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ferr := s.readFile()
	if ferr != nil {
		return "", ferr
	}
	if v, ok := m[account]; ok {
		return v, nil
	}
	return "", fmt.Errorf("secret %s: %w", account, memory.ErrNotFound)
}

// Set stores the secret, using the fallback file when the keyring fails.
func (s *Store) Set(account, value string) error {
	if !s.noKeyring {
		if err := keyring.Set(service, account, value); err == nil {
			return nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readFile()
	if err != nil {
		return err
	}
	m[account] = value
	return s.writeFile(m)
}

// Delete removes the secret from both the keyring and the fallback file.
func (s *Store) Delete(account string) error {
	// The keyring may be unavailable or hold nothing for account; the fallback file is cleaned either way.
	if !s.noKeyring {
		_ = keyring.Delete(service, account)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readFile()
	if err != nil {
		return err
	}
	if _, ok := m[account]; !ok {
		return nil
	}
	delete(m, account)
	return s.writeFile(m)
}

func (s *Store) readFile() (map[string]string, error) {
	b, err := os.ReadFile(s.fallbackPath)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading credentials: %w", err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parsing credentials: %w", err)
	}
	return m, nil
}

func (s *Store) writeFile(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.fallbackPath), 0o700); err != nil {
		return fmt.Errorf("creating credentials dir: %w", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding credentials: %w", err)
	}
	tmp := s.fallbackPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}
	if err := os.Rename(tmp, s.fallbackPath); err != nil {
		return fmt.Errorf("replacing credentials: %w", err)
	}
	return nil
}
