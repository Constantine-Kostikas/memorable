// Package config loads, validates, and saves memorable's TOML configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
)

// Config is the full memorable configuration.
type Config struct {
	FallbackContext string             `toml:"fallback_context"`
	Database        Database           `toml:"database"`
	Embedding       Embedding          `toml:"embedding"`
	Contexts        map[string]Context `toml:"contexts"`
	Search          Search             `toml:"search"`
	Preload         Preload            `toml:"preload"`
	Limits          Limits             `toml:"limits"`
	Retention       Retention          `toml:"retention"`
}

// Database holds Postgres connection settings.
type Database struct {
	Host      string `toml:"host"`
	Port      int    `toml:"port"`
	Name      string `toml:"name"`
	AppUser   string `toml:"app_user"`
	AdminUser string `toml:"admin_user"`
	Container string `toml:"container"`
}

// Embedding holds embedding provider settings.
type Embedding struct {
	Provider    string `toml:"provider"`
	URL         string `toml:"url"`
	Model       string `toml:"model"`
	Dimensions  int    `toml:"dimensions"`
	AllowRemote bool   `toml:"allow_remote"`
}

// Context maps directories to an isolated memory context.
type Context struct {
	Paths []string `toml:"paths"`
}

// Search holds ranking and similarity thresholds.
type Search struct {
	MinSimilarity      float64 `toml:"min_similarity"`
	DuplicateThreshold float64 `toml:"duplicate_threshold"`
	ConflictThreshold  float64 `toml:"conflict_threshold"`
	HalfLifeDays       int     `toml:"half_life_days"`
}

// Preload bounds the session-start memory set.
type Preload struct {
	MaxItems int `toml:"max_items"`
	MaxChars int `toml:"max_chars"`
}

// Limits holds write limits.
type Limits struct {
	WritesPerMinute int `toml:"writes_per_minute"`
}

// Retention holds data retention settings.
type Retention struct {
	AccessEventsDays int `toml:"access_events_days"`
	BackupsKeep      int `toml:"backups_keep"`
}

var contextNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// Default returns the built-in defaults with no contexts.
func Default() Config {
	return Config{
		Database: Database{
			Host: "127.0.0.1", Port: 5434, Name: "memorable",
			AppUser: "memorable_app", AdminUser: "memorable_admin", Container: "memorable-postgres",
		},
		Embedding: Embedding{
			Provider: "ollama", URL: "http://127.0.0.1:11434", Model: "embeddinggemma", Dimensions: 768,
		},
		Contexts:  map[string]Context{},
		Search:    Search{MinSimilarity: 0.35, DuplicateThreshold: 0.95, ConflictThreshold: 0.80, HalfLifeDays: 30},
		Preload:   Preload{MaxItems: 10, MaxChars: 4000},
		Limits:    Limits{WritesPerMinute: 60},
		Retention: Retention{AccessEventsDays: 90, BackupsKeep: 14},
	}
}

// SchemaName returns the Postgres schema for a context.
func SchemaName(context string) string { return "mem_" + context }

// ContextNames returns the configured context names in sorted order.
func (c *Config) ContextNames() []string {
	names := make([]string, 0, len(c.Contexts))
	for n := range c.Contexts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Validate checks the configuration for consistency.
func (c *Config) Validate() error {
	var errs []error
	for name, ctx := range c.Contexts {
		if !contextNamePattern.MatchString(name) || name == "profile" {
			errs = append(errs, fmt.Errorf("context name %q must match %s and not be \"profile\"", name, contextNamePattern))
		}
		for _, p := range ctx.Paths {
			if !filepath.IsAbs(p) {
				errs = append(errs, fmt.Errorf("context %q path %q must be absolute", name, p))
			}
		}
	}
	if c.FallbackContext != "" {
		if _, ok := c.Contexts[c.FallbackContext]; !ok {
			errs = append(errs, fmt.Errorf("fallback_context %q is not a defined context", c.FallbackContext))
		}
	}
	if c.Database.Port < 1 || c.Database.Port > 65535 {
		errs = append(errs, fmt.Errorf("database port %d out of range", c.Database.Port))
	}
	if c.Database.Host == "" || c.Database.Name == "" || c.Database.AppUser == "" || c.Database.AdminUser == "" {
		errs = append(errs, errors.New("database host, name, app_user, and admin_user are required"))
	}
	if c.Embedding.Dimensions <= 0 {
		errs = append(errs, errors.New("embedding dimensions must be positive"))
	}
	if err := checkEmbeddingURL(c.Embedding); err != nil {
		errs = append(errs, err)
	}
	s := c.Search
	for name, v := range map[string]float64{"min_similarity": s.MinSimilarity, "duplicate_threshold": s.DuplicateThreshold, "conflict_threshold": s.ConflictThreshold} {
		if v <= 0 || v >= 1 {
			errs = append(errs, fmt.Errorf("search %s must be between 0 and 1", name))
		}
	}
	if s.ConflictThreshold >= s.DuplicateThreshold {
		errs = append(errs, errors.New("search conflict_threshold must be below duplicate_threshold"))
	}
	if s.HalfLifeDays <= 0 || c.Preload.MaxItems <= 0 || c.Preload.MaxChars <= 0 || c.Limits.WritesPerMinute <= 0 ||
		c.Retention.AccessEventsDays <= 0 || c.Retention.BackupsKeep <= 0 {
		errs = append(errs, errors.New("half_life_days, preload, limits, and retention values must be positive"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}

func checkEmbeddingURL(e Embedding) error {
	u, err := url.Parse(e.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("embedding url %q is not a valid http(s) URL", e.URL)
	}
	if e.AllowRemote {
		return nil
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("embedding url %q is not local; set allow_remote = true to send memory content off this machine", e.URL)
}
