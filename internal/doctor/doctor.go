// Package doctor diagnoses a memorable installation.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/constantine-kostikas/memorable/internal/claudecfg"
	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/memory"
	"github.com/constantine-kostikas/memorable/internal/rank"
	"github.com/constantine-kostikas/memorable/internal/store"
)

// Status is a check outcome.
type Status string

// Check outcomes.
const (
	StatusOK      Status = "ok"
	StatusFail    Status = "fail"
	StatusSkipped Status = "skipped"
)

// Check is one diagnostic result.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// Probe queries a database as the app role.
type Probe interface {
	Ping(ctx context.Context) error
	Extensions(ctx context.Context) (map[string]string, error)
	SchemaVersion(ctx context.Context, schema string) (int64, error)
	EmbeddingDimensions(ctx context.Context) (int, error)
	LatestCalibration(ctx context.Context, model string, dims int) (*memory.StoredCalibration, error)
	Close()
}

// Deps are the doctor's inputs.
type Deps struct {
	Config      *config.Config
	ConfigErr   error
	ContextName string
	ContextErr  error
	Password    func() (string, error)
	Docker      func(ctx context.Context, args ...string) ([]byte, error)
	Dial        func(ctx context.Context, network, addr string) (net.Conn, error)
	Open        func(ctx context.Context, password string) (Probe, error)
	HTTP        *http.Client
	// ClaudeSettings reads ~/.claude/settings.json; nil means it is unknown.
	ClaudeSettings func() ([]byte, error)
	// ProfileOnly is set when the probed schema is the profile schema, which has no calibrations.
	ProfileOnly bool
}

// Failed reports whether any check failed.
func Failed(checks []Check) bool {
	for _, c := range checks {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

type runner struct {
	checks []Check
}

func (r *runner) add(name string, ok bool, detail, hint string) bool {
	c := Check{Name: name, Status: StatusOK, Detail: detail}
	if !ok {
		c.Status, c.Hint = StatusFail, hint
	}
	r.checks = append(r.checks, c)
	return ok
}

func (r *runner) skip(names ...string) {
	for _, n := range names {
		r.checks = append(r.checks, Check{Name: n, Status: StatusSkipped, Detail: "depends on an earlier failed check"})
	}
}

// Run executes every check in order, skipping those whose prerequisites failed.
func Run(ctx context.Context, d Deps) []Check {
	r := &runner{}
	if !r.add("config", d.ConfigErr == nil, errDetail(d.ConfigErr, "valid"), "fix the config file, or run `memorable setup`") {
		return r.checks
	}
	cfg := d.Config
	r.add("context", d.ContextErr == nil, contextDetail(d), "map this directory with `memorable context add <path> <name>`")
	hook := recallHookInstalled(d)
	hookDetail := "not installed (optional: `memorable setup` adds it)"
	if hook {
		hookDetail = "installed"
	}
	r.add("recall hook", true, hookDetail, "")
	dbOK := r.database(ctx, d, cfg, hook)
	r.ollama(ctx, d, cfg)
	if !dbOK {
		r.skip("dimensions")
	}
	return r.checks
}

func errDetail(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

func contextDetail(d Deps) string {
	if d.ContextErr != nil {
		return d.ContextErr.Error()
	}
	return d.ContextName
}

var dbChecks = []string{"port", "app login", "extensions", "schemas", "calibration"}

func recallHookInstalled(d Deps) bool {
	if d.ClaudeSettings == nil {
		return false
	}
	b, err := d.ClaudeSettings()
	return err == nil && claudecfg.HasHook(b, "UserPromptSubmit", claudecfg.PromptHookCommand)
}

func (r *runner) database(ctx context.Context, d Deps, cfg *config.Config, hook bool) bool {
	password, err := d.Password()
	r.add("app password", err == nil, errDetail(err, "stored"), "run `memorable setup` (or `memorable migrate`) to create it")

	out, derr := d.Docker(ctx, "inspect", "-f", "{{.State.Running}}", cfg.Database.Container)
	running := derr == nil && strings.TrimSpace(string(out)) == "true"
	detail := cfg.Database.Container + " running"
	if !running {
		detail = strings.TrimSpace(errDetail(derr, cfg.Database.Container+" is not running"))
	}
	if !r.add("container", running, detail, "docker start "+cfg.Database.Container) {
		r.skip(dbChecks...)
		return false
	}
	addr := net.JoinHostPort(cfg.Database.Host, strconv.Itoa(cfg.Database.Port))
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	conn, err := d.Dial(dialCtx, "tcp", addr)
	cancel()
	if err == nil {
		_ = conn.Close()
	}
	if !r.add("port", err == nil, errDetail(err, addr+" reachable"), "check [database] host and port, and the container's port mapping") {
		r.skip(dbChecks[1:]...)
		return false
	}
	if password == "" {
		r.skip(dbChecks[1:]...)
		return false
	}
	probe, err := d.Open(ctx, password)
	if err == nil {
		err = probe.Ping(ctx)
	}
	if !r.add("app login", err == nil, errDetail(err, cfg.Database.AppUser+" connected"), "run `memorable migrate` to (re)create the app role") {
		r.skip(dbChecks[2:]...)
		return false
	}
	defer probe.Close()
	r.extensions(ctx, probe)
	r.schemas(ctx, probe, cfg)
	dims, err := probe.EmbeddingDimensions(ctx)
	ok := err == nil && dims == cfg.Embedding.Dimensions
	r.add("dimensions", ok, fmt.Sprintf("column vector(%d), config %d", dims, cfg.Embedding.Dimensions), "run `memorable reembed --all`")
	r.calibration(ctx, d, probe, cfg, hook)
	return true
}

// calibration reports which relevance calibration prompt recall uses. A missing one only fails when the
// recall hook is installed, because then every prompt silently gets nothing.
func (r *runner) calibration(ctx context.Context, d Deps, probe Probe, cfg *config.Config, hook bool) {
	model, dims := cfg.Embedding.Model, cfg.Embedding.Dimensions
	if d.ProfileOnly {
		r.checks = append(r.checks, Check{Name: "calibration", Status: StatusSkipped, Detail: "no context schema to read"})
		return
	}
	stored, err := probe.LatestCalibration(ctx, model, dims)
	switch {
	case err != nil:
		r.add("calibration", false, err.Error(), "run `memorable migrate`")
	case stored != nil:
		r.add("calibration", true, fmt.Sprintf("usage fit from %d relevant and %d irrelevant labels, %s",
			stored.Positives, stored.Negatives, stored.FittedAt.Format(time.DateOnly)), "")
	default:
		if _, ok := rank.DefaultCalibration(model, dims); ok {
			r.add("calibration", true, "built-in default for "+model, "")
			return
		}
		detail := fmt.Sprintf("no calibration for %s with %d dimensions", model, dims)
		if hook {
			r.add("calibration", false, detail+"; prompt recall shows nothing",
				"switch to embeddinggemma, or fit a default for this model with `make test-eval`")
			return
		}
		r.checks = append(r.checks, Check{Name: "calibration", Status: StatusSkipped, Detail: detail + "; prompt recall disabled"})
	}
}

func (r *runner) extensions(ctx context.Context, probe Probe) {
	ext, err := probe.Extensions(ctx)
	vector, trgm := ext["vector"], ext["pg_trgm"]
	ok := err == nil && trgm != "" && versionAtLeast(vector, 0, 8)
	r.add("extensions", ok, fmt.Sprintf("vector %s, pg_trgm %s", orNone(vector), orNone(trgm)),
		"use the pgvector/pgvector image with pgvector 0.8 or newer")
}

func (r *runner) schemas(ctx context.Context, probe Probe, cfg *config.Config) {
	want := store.LatestVersion(store.SchemaContext)
	schemas := []string{store.ProfileSchema}
	for _, n := range cfg.ContextNames() {
		schemas = append(schemas, config.SchemaName(n))
	}
	var behind []string
	for _, s := range schemas {
		v, err := probe.SchemaVersion(ctx, s)
		if err != nil || v < want {
			behind = append(behind, s)
		}
	}
	detail := fmt.Sprintf("%d schemas at version %d", len(schemas), want)
	if len(behind) > 0 {
		detail = "behind: " + strings.Join(behind, ", ")
	}
	r.add("schemas", len(behind) == 0, detail, "run `memorable migrate`")
}

func (r *runner) ollama(ctx context.Context, d Deps, cfg *config.Config) {
	base := strings.TrimRight(cfg.Embedding.URL, "/")
	var version struct {
		Version string `json:"version"`
	}
	if err := getJSON(ctx, d.HTTP, base+"/api/version", &version); !r.add("ollama", err == nil, errDetail(err, "version "+version.Version),
		"install Ollama and make sure it listens on "+cfg.Embedding.URL) {
		r.skip("model")
		return
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	err := getJSON(ctx, d.HTTP, base+"/api/tags", &tags)
	found := false
	for _, m := range tags.Models {
		if m.Name == cfg.Embedding.Model || strings.TrimSuffix(m.Name, ":latest") == cfg.Embedding.Model {
			found = true
		}
	}
	r.add("model", err == nil && found, cfg.Embedding.Model, "ollama pull "+cfg.Embedding.Model)
}

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decoding %s: %w", url, err)
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "missing"
	}
	return s
}

func versionAtLeast(v string, major, minor int) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	maj, err1 := strconv.Atoi(parts[0])
	mn, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return maj > major || (maj == major && mn >= minor)
}
