package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"
)

type hookInput struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
}

func newHookCmd(d deps, flags *rootFlags) *cobra.Command {
	hook := &cobra.Command{
		Use:     "hook",
		Short:   "Claude Code hook entry points",
		GroupID: groupSetup,
	}
	hook.AddCommand(&cobra.Command{
		Use:   "session-start",
		Short: "Print the preload set for a SessionStart hook (never fails the session)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A hook must never block or break a session: every failure prints nothing and exits 0.
			text, err := sessionStartText(cmd, d, flags)
			if err == nil && text != "" {
				_, _ = io.WriteString(cmd.OutOrStdout(), text)
			}
			return nil
		},
	})
	return hook
}

func sessionStartText(cmd *cobra.Command, d deps, flags *rootFlags) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading hook input: %w", err)
	}
	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil || in.Cwd == "" {
		return "", fmt.Errorf("parsing hook input: %w", err)
	}
	logger := slog.New(slog.DiscardHandler)
	a, err := d.build(cmd.Context(), flags.configPath, []string{in.Cwd}, logger)
	if err != nil {
		return "", err
	}
	defer a.Close()
	items, err := a.svc.Preload(cmd.Context())
	if err != nil || len(items) == 0 {
		return "", err //nolint:wrapcheck // the hook swallows every error
	}
	var b strings.Builder
	b.WriteString("Relevant memories (memorable). Fetch full content with memory_get:\n")
	for _, s := range items {
		ref := s.ID.String()
		if s.Slug != nil {
			ref = *s.Slug
		}
		fmt.Fprintf(&b, "- [%s] %s: %s\n", ref, oneLine(s.Title), oneLine(s.Snippet))
	}
	return b.String(), nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
