package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/constantine-kostikas/memorable/internal/ui"
	"github.com/constantine-kostikas/memorable/internal/update"
)

// runSelfUpdate wires the real updater: GitHub over HTTPS, the embedded release key, and sudo on the terminal.
func runSelfUpdate(d deps, cmd *cobra.Command, current string, opts update.Options, report update.Reporter) (update.Result, error) {
	var gh update.RunFunc
	if _, err := d.lookPath("gh"); err == nil {
		gh = d.run
	}
	u := &update.Updater{
		Current: current,
		GitHub: &update.GitHub{
			BaseURL:   update.DefaultBaseURL,
			Repo:      update.DefaultRepo,
			Token:     update.Token(cmd.Context(), d.getenv, gh),
			UserAgent: "memorable/" + current,
			HTTP:      update.NewHTTPClient(nil, update.RedirectAllowed),
		},
		PublicKey:  update.ReleaseKey,
		GOOS:       d.goos,
		GOARCH:     runtime.GOARCH,
		Executable: d.executable,
		IsTTY:      d.isTTY(cmd),
		Sudo:       sudoOnTerminal(cmd),
		Report:     report,
	}
	return u.Run(cmd.Context(), opts) //nolint:wrapcheck // errors from internal/update are already contextual
}

// sudoOnTerminal runs sudo with the user's terminal so it can prompt for a password. Its output goes to stderr,
// keeping stdout clean for -o json.
func sudoOnTerminal(cmd *cobra.Command) func(ctx context.Context, argv ...string) error {
	return func(ctx context.Context, argv ...string) error {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "The install directory needs root. Running: %s\n", shellQuote(argv))
		c := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: argv is `sudo install -m 0755 <staged> <target>` built by internal/update; no shell
		c.Stdin = cmd.InOrStdin()
		c.Stdout = cmd.ErrOrStderr()
		c.Stderr = cmd.ErrOrStderr()
		if err := c.Run(); err != nil {
			return fmt.Errorf("%s: %w", argv[0], err)
		}
		return nil
	}
}

func newUpdateCmd(d deps, flags *rootFlags, info buildInfo) *cobra.Command {
	var opts update.Options
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Replace this binary with the newest verified release",
		Long: "update finds the newest GitHub release, verifies its GPG-signed checksums, and replaces the running binary.\n" +
			"Stable installs only move to stable releases unless --pre is given; pre-release installs also see pre-releases.\n" +
			"The repository is private, so update reads a token from GH_TOKEN, GITHUB_TOKEN, or `gh auth token`.",
		Example: "  memorable update --check\n  memorable update\n  memorable update --version v0.2.0",
		GroupID: groupSetup,
		Args:    argsUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newPrinter(cmd, flags, d.getenv)
			if err != nil {
				return err
			}
			var report update.Reporter
			if !p.IsJSON() {
				report = newProgressReporter(cmd.ErrOrStderr(), d.getenv)
			}
			res, err := d.selfUpdate(cmd, info.Version, opts, report)
			if errors.Is(err, update.ErrInvalidVersion) {
				return usageError{err}
			}
			if err != nil {
				return err
			}
			if p.IsJSON() {
				if err := p.JSON(res); err != nil {
					return err
				}
			} else {
				printUpdate(p, res)
			}
			if res.Status == update.StatusNeedsRoot {
				return errors.New("update not installed: run the command above to finish") //nolint:err113 // shown to the user
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.Check, "check", false, "report whether an update exists; change nothing")
	f.BoolVar(&opts.Pre, "pre", false, "include pre-releases")
	f.BoolVar(&opts.Force, "force", false, "replace a development build, or reinstall the same version")
	f.StringVar(&opts.Version, "version", "", "install this version, for example v0.2.0")
	return cmd
}

func printUpdate(p *ui.Printer, res update.Result) {
	switch res.Status {
	case update.StatusUpToDate:
		p.Success(fmt.Sprintf("memorable %s is up to date", res.Current))
	case update.StatusAvailable:
		p.Line(fmt.Sprintf("Update available: %s -> %s", res.Current, res.Target))
		p.Line(p.Muted("Run `memorable update` to install it."))
	case update.StatusNeedsRoot:
		p.Warn(fmt.Sprintf("%s is not writable. %s is verified and staged; install it with:", filepath.Dir(res.Path), res.Target))
		p.Line("  " + shellQuote(res.Command))
		p.Line(p.Muted("Do not run `sudo memorable update` instead: root has no GitHub token."))
	case update.StatusUpdated:
		p.Success(fmt.Sprintf("Updated memorable %s -> %s at %s", res.Current, res.Target, res.Path))
		p.Panel("Next steps", strings.Join([]string{
			"1. Restart your Claude Code sessions: running `memorable serve` processes keep the old binary.",
			"2. Run `memorable doctor`, and `memorable migrate` if it reports schema versions behind.",
		}, "\n"))
	}
}

// shellQuote renders argv so it can be pasted into a POSIX shell.
func shellQuote(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.Trim(a, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:@+,") == "" {
			quoted[i] = a
			continue
		}
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
