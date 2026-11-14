// Command memorable is a persistent memory service and CLI for Claude Code.
package main

import (
	"context"
	"errors"
	"os"
	"syscall"

	"github.com/charmbracelet/fang"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(run())
}

func run() int {
	info := buildInfo{Version: version, Commit: commit, Date: date}
	err := fang.Execute(context.Background(), newRootCmd(info),
		fang.WithVersion(version),
		fang.WithCommit(commit),
		fang.WithNotifySignal(os.Interrupt, syscall.SIGTERM),
	)
	return exitCode(err)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ue := (usageError{}); errors.As(err, &ue) {
		return 2
	}
	return 1
}
