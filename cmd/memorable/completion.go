package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/constantine-kostikas/memorable/internal/config"
)

var memoryTypeCompletions = []cobra.Completion{"user", "feedback", "project", "reference"}

var listStatusCompletions = []cobra.Completion{"active", "unverified", "flagged", "superseded", "deleted"}

func generateCompletion(root *cobra.Command, shell string, w io.Writer) error {
	switch shell {
	case "bash":
		return root.GenBashCompletionV2(w, true) //nolint:wrapcheck // shown to the user as is
	case "zsh":
		return root.GenZshCompletion(w) //nolint:wrapcheck // shown to the user as is
	case "fish":
		return root.GenFishCompletion(w, true) //nolint:wrapcheck // shown to the user as is
	default:
		return fmt.Errorf("unsupported shell %q: use bash, zsh, or fish", shell) //nolint:err113 // shown to the user
	}
}

// completeContextNames reads context names from the config file only: tab never opens the
// database or the keyring.
func completeContextNames(d deps, flags *rootFlags) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		path, err := resolveConfigPath(flags.configPath, d.getenv)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		cfg, err := config.Load(path, d.getenv)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := cfg.ContextNames()
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

func pathArgCompletion(first cobra.ShellCompDirective) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return nil, first
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func mustFlagCompletion(cmd *cobra.Command, name string, fn cobra.CompletionFunc) {
	if err := cmd.RegisterFlagCompletionFunc(name, fn); err != nil {
		panic(fmt.Sprintf("registering completion for --%s on %s: %v", name, cmd.CommandPath(), err))
	}
}

func mustFind(root *cobra.Command, path ...string) *cobra.Command {
	cmd, _, err := root.Find(path)
	if err != nil || cmd == root && len(path) > 0 {
		panic(fmt.Sprintf("completion target %v not found", path))
	}
	return cmd
}

func registerCompletions(root *cobra.Command, d deps, flags *rootFlags) {
	mustFlagCompletion(root, "output", cobra.FixedCompletions([]cobra.Completion{"text", "json"}, cobra.ShellCompDirectiveNoFileComp))
	if err := root.MarkPersistentFlagFilename("config", "toml"); err != nil {
		panic(err)
	}

	types := cobra.FixedCompletions(memoryTypeCompletions, cobra.ShellCompDirectiveNoFileComp)
	mustFlagCompletion(mustFind(root, "search"), "type", types)
	mustFlagCompletion(mustFind(root, "list"), "type", types)
	mustFlagCompletion(mustFind(root, "profile", "add"), "type", types)
	mustFlagCompletion(mustFind(root, "list"), "status", cobra.FixedCompletions(listStatusCompletions, cobra.ShellCompDirectiveNoFileComp))
	mustFlagCompletion(mustFind(root, "password"), "role", cobra.FixedCompletions([]cobra.Completion{roleApp}, cobra.ShellCompDirectiveNoFileComp))

	export := mustFind(root, "export")
	mustFlagCompletion(export, "format", cobra.FixedCompletions([]cobra.Completion{"json", "md"}, cobra.ShellCompDirectiveNoFileComp))
	if err := export.MarkFlagFilename("out"); err != nil {
		panic(err)
	}

	contexts := completeContextNames(d, flags)
	for _, name := range []string{"approve", "purge", "prune"} {
		mustFlagCompletion(mustFind(root, name), "context", contexts)
	}
	profile := mustFind(root, "profile")
	mustFlagCompletion(profile, "context", contexts)

	if err := mustFind(root, "backup").MarkFlagDirname("dir"); err != nil {
		panic(err)
	}
	mustFind(root, "import").ValidArgsFunction = pathArgCompletion(cobra.ShellCompDirectiveDefault)
	mustFind(root, "context", "add").ValidArgsFunction = pathArgCompletion(cobra.ShellCompDirectiveFilterDirs)
}

func hasPathAnnotation(f *pflag.Flag) bool {
	_, ext := f.Annotations[cobra.BashCompFilenameExt]
	_, dir := f.Annotations[cobra.BashCompSubdirsInDir]
	return ext || dir
}

// applyCompletionDefaults makes every command and flag that has no explicit completion
// complete to nothing, so tab never falls back to listing files for ids and free text.
func applyCompletionDefaults(cmd *cobra.Command) error {
	if cmd.ValidArgsFunction == nil && len(cmd.ValidArgs) == 0 {
		cmd.ValidArgsFunction = cobra.NoFileCompletions
	}
	var firstErr error
	register := func(f *pflag.Flag) {
		if firstErr != nil || f.Value.Type() == "bool" || hasPathAnnotation(f) {
			return
		}
		if fn, _ := cmd.GetFlagCompletionFunc(f.Name); fn != nil {
			return
		}
		if err := cmd.RegisterFlagCompletionFunc(f.Name, cobra.NoFileCompletions); err != nil {
			firstErr = fmt.Errorf("registering default completion for --%s on %s: %w", f.Name, cmd.CommandPath(), err)
		}
	}
	cmd.LocalNonPersistentFlags().VisitAll(register)
	cmd.PersistentFlags().VisitAll(register)
	if firstErr != nil {
		return firstErr
	}
	for _, c := range cmd.Commands() {
		if err := applyCompletionDefaults(c); err != nil {
			return err
		}
	}
	return nil
}
