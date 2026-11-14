package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/claudecfg"
	"github.com/constantine-kostikas/memorable/internal/config"
	"github.com/constantine-kostikas/memorable/internal/dockerpg"
	"github.com/constantine-kostikas/memorable/internal/doctor"
	"github.com/constantine-kostikas/memorable/internal/scheduler"
	"github.com/constantine-kostikas/memorable/internal/ui"
)

// setupRun walks the setup steps; in dry-run mode it only prints what it would do.
type setupRun struct {
	ctx     context.Context
	cmd     *cobra.Command
	d       deps
	p       *ui.Printer
	dry     bool
	home    string
	cfgPath string
	cfg     *config.Config
	adminPW string
	bin     string
	step    int
}

func (s *setupRun) heading(title string) {
	s.step++
	s.p.Line("")
	s.p.Line(fmt.Sprintf("Step %d · %s", s.step, title))
}

func (s *setupRun) confirm(question string, def bool) (bool, error) {
	if s.dry {
		s.p.Line(s.p.Muted("  would ask: " + question))
		return def, nil
	}
	ok := def
	err := runForm(s.cmd.InOrStdin(), s.cmd.ErrOrStderr(), huh.NewConfirm().Title(question).Value(&ok))
	return ok, err
}

func (s *setupRun) input(title, def string) (string, error) {
	if s.dry {
		return def, nil
	}
	v := def
	err := runForm(s.cmd.InOrStdin(), s.cmd.ErrOrStderr(), huh.NewInput().Title(title).Value(&v))
	return strings.TrimSpace(v), err
}

func readOptional(path string) (string, fs.FileMode, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the user's own config files
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0o600, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("reading %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, fmt.Errorf("reading %s: %w", path, err)
	}
	return string(b), info.Mode().Perm(), nil
}

// writeFile shows the diff and writes after confirmation, keeping the file's existing permissions.
func (s *setupRun) writeFile(path, after string) error {
	before, perm, err := readOptional(path)
	if err != nil {
		return err
	}
	diff := claudecfg.Diff(claudecfg.Change{Path: path, Before: before, After: after})
	if diff == "" {
		s.p.Success(path + " is already up to date")
		return nil
	}
	s.p.Line(diff)
	ok, err := s.confirm("Write "+path+"?", true)
	if err != nil || !ok || s.dry {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".memorable-tmp"
	if err := os.WriteFile(tmp, []byte(after), perm); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	s.p.Success("wrote " + path)
	return nil
}

// command shows a command and runs it after confirmation; it reports whether it ran.
func (s *setupRun) command(question, name string, args ...string) (bool, error) {
	s.p.Line("  $ " + name + " " + strings.Join(args, " "))
	ok, err := s.confirm(question, true)
	if err != nil || !ok || s.dry {
		return false, err
	}
	if _, err := s.d.run(s.ctx, name, args...); err != nil {
		return false, err
	}
	return true, nil
}

func (s *setupRun) prerequisites() error {
	s.heading("Prerequisites")
	if _, err := s.d.lookPath("docker"); err != nil {
		if !s.dry {
			return errors.New("docker is not installed; install Docker and rerun `memorable setup`")
		}
		s.p.Warn("docker not found")
	} else {
		s.p.Success("docker found")
	}
	if _, err := s.d.lookPath("ollama"); err != nil {
		s.p.Warn("ollama not found: install it from https://ollama.com/download (search falls back to keywords without it)")
	} else {
		s.p.Success("ollama found")
	}
	return nil
}

func (s *setupRun) container() error {
	db := s.cfg.Database
	s.heading("Postgres container " + db.Container)
	state, err := dockerpg.ContainerState(s.ctx, s.d.docker, db.Container)
	if err != nil {
		return err //nolint:wrapcheck // shown to the user
	}
	switch state {
	case dockerpg.StateRunning:
		s.p.Success(db.Container + " is running")
		return nil
	case dockerpg.StateStopped:
		s.p.Line("  $ docker start " + db.Container)
		ok, err := s.confirm("Start the container?", true)
		if err != nil || !ok || s.dry {
			return err
		}
		if _, err := s.d.docker(s.ctx, "start", db.Container); err != nil {
			return err
		}
	default:
		shown, err := dockerpg.RunArgs(db, "<generated>")
		if err != nil {
			return err //nolint:wrapcheck // shown to the user
		}
		s.p.Line("  $ docker " + strings.Join(shown, " "))
		ok, err := s.confirm("Create the container with a generated admin password?", true)
		if err != nil || !ok || s.dry {
			return err
		}
		s.adminPW = generatePassword()
		if err := dockerpg.Start(s.ctx, s.d.docker, db, s.adminPW); err != nil {
			return err //nolint:wrapcheck // shown to the user
		}
		s.p.Panel("Admin password (shown once)", s.adminPW+
			"\n\nStore it in your password manager. memorable never saves it; admin commands ask for it.")
	}
	s.p.Line("  waiting for Postgres to accept connections...")
	return dockerpg.WaitReady(s.ctx, s.d.docker, db.Container, db.AdminUser, time.Second, time.Minute) //nolint:wrapcheck // shown to the user
}

func encodeConfig(c config.Config) (string, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(c); err != nil {
		return "", fmt.Errorf("encoding config: %w", err)
	}
	return b.String(), nil
}

func (s *setupRun) contexts() error {
	s.heading("Contexts")
	if len(s.cfg.Contexts) > 0 {
		for _, n := range s.cfg.ContextNames() {
			s.p.Line(fmt.Sprintf("  %s: %s", n, strings.Join(s.cfg.Contexts[n].Paths, ", ")))
		}
		s.p.Success("using the contexts in " + s.cfgPath + " (change them with `memorable context add`)")
		return nil
	}
	cwd, _ := s.d.cwd()
	name, err := s.input("Context name (for example personal or work)", "personal")
	if err != nil {
		return err
	}
	paths, err := s.input("Directories for this context (comma separated)", cwd)
	if err != nil {
		return err
	}
	ctx := config.Context{}
	for _, p := range strings.Split(paths, ",") {
		if p = strings.TrimSpace(p); p != "" {
			abs, err := config.ExpandPath(p)
			if err != nil {
				return err //nolint:wrapcheck // shown to the user
			}
			ctx.Paths = append(ctx.Paths, abs)
		}
	}
	s.cfg.Contexts = map[string]config.Context{name: ctx}
	fallback, err := s.confirm("Allow read-only access to "+name+" from directories outside every context?", false)
	if err != nil {
		return err
	}
	if fallback {
		s.cfg.FallbackContext = name
	}
	if err := s.cfg.Validate(); err != nil {
		return usageError{err}
	}
	text, err := encodeConfig(*s.cfg)
	if err != nil {
		return err
	}
	return s.writeFile(s.cfgPath, text)
}

func (s *setupRun) database() error {
	s.heading("Database roles and schemas")
	schemas := []string{"mem_profile"}
	for _, n := range s.cfg.ContextNames() {
		schemas = append(schemas, config.SchemaName(n))
	}
	if s.dry {
		s.p.Line("  would create the app role, schemas " + strings.Join(schemas, ", ") +
			", apply migrations, and store the app password in the OS keyring")
		return nil
	}
	if s.adminPW == "" {
		pw, err := s.d.adminPassword(s.cmd)
		if err != nil {
			return err
		}
		s.adminPW = pw
	}
	if _, err := migrateAll(s.ctx, s.d, s.cfg, s.adminPW); err != nil {
		return err
	}
	s.p.Success("database ready: " + strings.Join(schemas, ", "))
	return nil
}

func (s *setupRun) model() error {
	s.heading("Embedding model")
	if _, err := s.d.lookPath("ollama"); err != nil && !s.dry {
		s.p.Warn("skipped: install Ollama, then run `ollama pull " + s.cfg.Embedding.Model + "`")
		return nil //nolint:nilerr // memorable works with keyword search until Ollama is installed
	}
	_, err := s.command("Pull "+s.cfg.Embedding.Model+" (fast when already present)?", "ollama", "pull", s.cfg.Embedding.Model)
	return err
}

func (s *setupRun) mcp() error {
	s.heading("Register the MCP server with Claude Code")
	args := []string{"mcp", "add", "--scope", "user", "memorable", "--", s.bin, "serve"}
	if s.dry {
		s.p.Line("  $ claude " + strings.Join(args, " "))
		return nil
	}
	if _, err := s.d.lookPath("claude"); err != nil {
		s.p.Warn("claude CLI not found; run this yourself: claude " + strings.Join(args, " "))
		return nil //nolint:nilerr // a missing claude CLI is reported, not fatal
	}
	s.p.Line("  $ claude " + strings.Join(args, " "))
	ok, err := s.confirm("Register memorable globally?", true)
	if err != nil || !ok {
		return err
	}
	already, err := claudecfg.RegisterMCP(s.ctx, s.d.run, s.bin)
	if err != nil {
		return err //nolint:wrapcheck // shown to the user
	}
	if already {
		s.p.Success("memorable is already registered")
	} else {
		s.p.Success("registered; new Claude Code sessions will load it")
	}
	return nil
}

func (s *setupRun) claudeMD() error {
	s.heading("Global CLAUDE.md instructions")
	path := filepath.Join(s.home, ".claude", "CLAUDE.md")
	before, _, err := readOptional(path)
	if err != nil {
		return err
	}
	return s.writeFile(path, claudecfg.UpsertClaudeMD(before, claudecfg.Snippet))
}

func (s *setupRun) credentialsPath() string {
	if s.d.goos == "darwin" {
		return filepath.Join(s.home, "Library", "Application Support", "memorable", "credentials.json")
	}
	if x := s.d.getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "memorable", "credentials.json")
	}
	return filepath.Join(s.home, ".config", "memorable", "credentials.json")
}

func (s *setupRun) settings() error {
	s.heading("Claude Code permissions and hook")
	path := filepath.Join(s.home, ".claude", "settings.json")
	before, _, err := readOptional(path)
	if err != nil {
		return err
	}
	allow, ask, deny := claudecfg.Rules(s.credentialsPath())
	after, err := claudecfg.MergePermissions([]byte(before), allow, ask, deny)
	if err != nil {
		return err //nolint:wrapcheck // shown to the user
	}
	hook, err := s.confirm("Also load your top memories at the start of every session (SessionStart hook, uses tokens)?", false)
	if err != nil {
		return err
	}
	if hook {
		if after, err = claudecfg.AddSessionStartHook(after, claudecfg.HookCommand); err != nil {
			return err //nolint:wrapcheck // shown to the user
		}
	}
	return s.writeFile(path, string(after))
}

func (s *setupRun) backupTimer() error {
	s.heading("Daily backups")
	files, cmds, err := scheduler.Plan(s.d.goos, s.bin, s.home, os.Getuid())
	if err != nil {
		s.p.Warn(err.Error())
		return nil //nolint:nilerr // unsupported platforms skip scheduled backups
	}
	ok, err := s.confirm("Install a daily backup job?", true)
	if err != nil || !ok {
		return err
	}
	for _, f := range files {
		if err := s.writeFile(f.Path, f.Content); err != nil {
			return err
		}
	}
	for _, c := range cmds {
		if _, err := s.command("Run this?", c[0], c[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (s *setupRun) check(flags *rootFlags) error {
	s.heading("Check")
	if s.dry {
		s.p.Line("  would run `memorable doctor`")
		return nil
	}
	checks := doctor.Run(s.ctx, doctorDeps(s.d, flags))
	printChecks(s.p, checks)
	if doctor.Failed(checks) {
		s.p.Warn("some checks failed; fix them and run `memorable doctor` again")
	}
	return nil
}

func newSetupCmd(d deps, flags *rootFlags) *cobra.Command {
	var dry bool
	cmd := &cobra.Command{
		Use:     "setup",
		Short:   "Guided setup: database, config, Claude Code integration, backups",
		Example: "  memorable setup --dry-run   # preview every change\n  memorable setup",
		GroupID: groupSetup,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dry && !d.isTTY(cmd) {
				return fmt.Errorf("setup asks before every change: %w (use --dry-run to preview)", errNeedsTerminal)
			}
			p, err := newPrinter(cmd, &rootFlags{output: "text"}, d.getenv)
			if err != nil {
				return err
			}
			home := d.getenv("HOME")
			if home == "" {
				if home, err = os.UserHomeDir(); err != nil {
					return fmt.Errorf("locating home: %w", err)
				}
			}
			cfgPath, err := resolveConfigPath(flags.configPath, d.getenv)
			if err != nil {
				return err
			}
			cfg, err := config.Load(cfgPath, d.getenv)
			if err != nil {
				return err //nolint:wrapcheck // shown to the user
			}
			bin, err := d.executable()
			if err != nil {
				return fmt.Errorf("locating the memorable binary: %w", err)
			}
			s := &setupRun{ctx: cmd.Context(), cmd: cmd, d: d, p: p, dry: dry, home: home, cfgPath: cfgPath, cfg: cfg, bin: bin}
			p.Panel("memorable setup", "Each step shows what it changes and asks first.")
			for _, step := range []func() error{s.prerequisites, s.container, s.contexts, s.database, s.model, s.mcp,
				s.claudeMD, s.settings, s.backupTimer, func() error { return s.check(flags) }} {
				if err := step(); err != nil {
					return err
				}
			}
			p.Line("")
			if dry {
				p.Line("dry run: nothing was changed")
				return nil
			}
			p.Success("setup complete; start a new Claude Code session to use memorable")
			return nil
		},
	}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "show every planned change without applying anything")
	return cmd
}
