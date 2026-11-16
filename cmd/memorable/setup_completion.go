package main

import (
	"fmt"
	"strings"

	"github.com/constantine-kostikas/memorable/internal/shellcomp"
)

const generatedFileMode = 0o644

// writeGenerated writes a generated file without a diff: the content is too long to review line by line.
func (s *setupRun) writeGenerated(path, content, what string) error {
	before, _, err := readOptional(path)
	if err != nil {
		return err
	}
	if before == content {
		s.p.Success(path + " is already up to date")
		return nil
	}
	s.p.Line(fmt.Sprintf("  %s: %s", what, path))
	ok, err := s.confirm("Write "+path+"?", true)
	if err != nil || !ok || s.dry {
		return err
	}
	if err := writeAtomic(path, content, generatedFileMode); err != nil {
		return err
	}
	s.p.Success("wrote " + path)
	return nil
}

func (s *setupRun) shellCompletion() error {
	s.heading("Shell completion")
	sh, ok := shellcomp.Detect(s.d.getenv("SHELL"))
	if !ok {
		s.p.Warn("skipped: unrecognized shell; see `memorable completion --help`")
		return nil
	}
	yes, err := s.confirm(fmt.Sprintf("Install tab completion for %s?", sh), true)
	if err != nil || !yes {
		return err
	}
	plan := shellcomp.Plan(sh, shellcomp.Env{GOOS: s.d.goos, Home: s.home, Getenv: s.d.getenv})
	var script strings.Builder
	if err := generateCompletion(s.cmd.Root(), string(sh), &script); err != nil {
		return err
	}
	if err := s.writeGenerated(plan.ScriptPath, script.String(), string(sh)+" completion script"); err != nil {
		return err
	}
	if plan.RCPath != "" {
		before, _, err := readOptional(plan.RCPath)
		if err != nil {
			return err
		}
		if err := s.writeFile(plan.RCPath, shellcomp.UpsertBlock(before, plan.RCBlock)); err != nil {
			return err
		}
	}
	for _, note := range plan.Notes {
		s.p.Line("  " + s.p.Muted(note))
	}
	return nil
}
