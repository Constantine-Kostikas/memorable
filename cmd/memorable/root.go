package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

const (
	groupMemory = "memory"
	groupAdmin  = "admin"
	groupSetup  = "setup"
)

type buildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

type rootFlags struct {
	configPath string
	output     string
}

func newRootCmd(info buildInfo) *cobra.Command {
	flags := &rootFlags{}
	root := &cobra.Command{
		Use:           "memorable",
		Short:         "Persistent, searchable memory for Claude Code",
		Long:          "memorable stores memories for Claude Code in Postgres and serves them over MCP with hybrid vector and keyword search.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if flags.output != "text" && flags.output != "json" {
				return usageError{fmt.Errorf("invalid --output %q: use text or json", flags.output)}
			}
			return nil
		},
	}
	root.AddGroup(
		&cobra.Group{ID: groupMemory, Title: "Memory"},
		&cobra.Group{ID: groupAdmin, Title: "Admin"},
		&cobra.Group{ID: groupSetup, Title: "Setup"},
	)
	root.PersistentFlags().StringVar(&flags.configPath, "config", "", "path to config.toml")
	root.PersistentFlags().StringVarP(&flags.output, "output", "o", "text", "output format: text or json")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })

	root.AddCommand(newVersionCmd(info, flags))
	return root
}

func newVersionCmd(info buildInfo, flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Print version information",
		GroupID: groupSetup,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.output == "json" {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(info); err != nil {
					return fmt.Errorf("writing version: %w", err)
				}
				return nil
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "memorable %s (commit %s, built %s)\n", info.Version, info.Commit, info.Date); err != nil {
				return fmt.Errorf("writing version: %w", err)
			}
			return nil
		},
	}
}

type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }
