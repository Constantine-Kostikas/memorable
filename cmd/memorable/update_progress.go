package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"github.com/constantine-kostikas/memorable/internal/ui"
	"github.com/constantine-kostikas/memorable/internal/update"
)

const (
	progressBarWidth  = 36
	progressRedrawGap = 100 * time.Millisecond
)

// progressReporter writes update progress to stderr, keeping stdout clean for -o json. On a terminal the
// download is a live progress bar; elsewhere it is one line at the start and one at the end.
type progressReporter struct {
	out  io.Writer
	live bool
	now  func() time.Time
	bar  progress.Model
}

var _ update.Reporter = (*progressReporter)(nil)

func newProgressReporter(out io.Writer, getenv func(string) string) *progressReporter {
	f, ok := out.(*os.File)
	live := ok && term.IsTerminal(f.Fd()) && getenv("TERM") != "dumb"
	return &progressReporter{out: out, live: live, now: time.Now, bar: ui.ProgressBar(progressBarWidth)}
}

func (r *progressReporter) Status(msg string) {
	_, _ = fmt.Fprintln(r.out, msg)
}

func (r *progressReporter) Transfer(name string, size int64) io.WriteCloser {
	bar := &progressBar{reporter: r, name: name, size: size}
	if r.live {
		bar.render()
	} else {
		_, _ = fmt.Fprintf(r.out, "Downloading %s%s\n", name, sizeSuffix(size))
	}
	return bar
}

type progressBar struct {
	reporter *progressReporter
	name     string
	size     int64
	received int64
	drawn    time.Time
}

func (b *progressBar) Write(p []byte) (int, error) {
	b.received += int64(len(p))
	if b.reporter.live && b.reporter.now().Sub(b.drawn) >= progressRedrawGap {
		b.render()
	}
	return len(p), nil
}

func (b *progressBar) Close() error {
	if b.reporter.live {
		b.render()
		_, _ = fmt.Fprintln(b.reporter.out)
		return nil
	}
	_, _ = fmt.Fprintf(b.reporter.out, "Downloaded %s (%s)\n", b.name, formatBytes(b.received))
	return nil
}

// render redraws the line in place. The line goes through lipgloss so colors match the terminal's profile and
// honor NO_COLOR; the carriage return and erase-line sequence are written directly so they are never stripped.
func (b *progressBar) render() {
	b.drawn = b.reporter.now()
	_, _ = fmt.Fprint(b.reporter.out, "\r\x1b[2K")
	_, _ = lipgloss.Fprint(b.reporter.out, b.line())
}

func (b *progressBar) line() string {
	if b.size <= 0 {
		return fmt.Sprintf("Downloading %s  %s", b.name, formatBytes(b.received))
	}
	fraction := min(float64(b.received)/float64(b.size), 1)
	return fmt.Sprintf("Downloading %s  %s  %s / %s",
		b.name, b.reporter.bar.ViewAs(fraction), formatBytes(b.received), formatBytes(b.size))
}

func sizeSuffix(size int64) string {
	if size <= 0 {
		return ""
	}
	return " (" + formatBytes(size) + ")"
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, prefix := float64(n)/unit, 0
	for value >= unit && prefix < 3 {
		value /= unit
		prefix++
	}
	return fmt.Sprintf("%.1f %ciB", value, "KMGT"[prefix])
}
