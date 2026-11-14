// Package ui renders CLI output: styled on terminals, plain when piped or NO_COLOR is set, JSON on request.
package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/term"
)

// Printer writes command output.
type Printer struct {
	out    io.Writer
	json   bool
	styled bool
}

var (
	accent  = lipgloss.Color("#7D56F4")
	green   = lipgloss.Color("#04B575")
	yellow  = lipgloss.Color("#E5C07B")
	red     = lipgloss.Color("#E06C75")
	muted   = lipgloss.Color("#8A8A8A")
	title   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	panel   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1)
	okStyle = lipgloss.NewStyle().Foreground(green)
	warnSty = lipgloss.NewStyle().Foreground(yellow)
	errSty  = lipgloss.NewStyle().Foreground(red)
	header  = lipgloss.NewStyle().Bold(true).Foreground(accent).Padding(0, 1)
	cell    = lipgloss.NewStyle().Padding(0, 1)
)

// NewPrinter returns a Printer for format text or json.
func NewPrinter(out io.Writer, format string, getenv func(string) string) (*Printer, error) {
	switch format {
	case "", "text":
	case "json":
		return &Printer{out: out, json: true}, nil
	default:
		return nil, fmt.Errorf("unknown output format %q: use text or json", format)
	}
	styled := false
	if f, ok := out.(*os.File); ok && term.IsTerminal(f.Fd()) && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb" {
		styled = true
	}
	return &Printer{out: out, styled: styled}, nil
}

// IsJSON reports whether output is JSON.
func (p *Printer) IsJSON() bool { return p.json }

// Styled reports whether output uses colors and borders.
func (p *Printer) Styled() bool { return p.styled }

// JSON writes v as indented JSON.
func (p *Printer) JSON(v any) error {
	enc := json.NewEncoder(p.out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("writing json: %w", err)
	}
	return nil
}

func (p *Printer) println(s string) {
	if p.styled {
		_, _ = lipgloss.Fprintln(p.out, s)
		return
	}
	_, _ = fmt.Fprintln(p.out, s)
}

// Panel prints a titled block.
func (p *Printer) Panel(heading, body string) {
	if !p.styled {
		p.println(heading)
		p.println(strings.Repeat("-", len(heading)))
		p.println(body)
		return
	}
	p.println(panel.Render(title.Render(heading) + "\n" + body))
}

// Table prints rows under headers.
func (p *Printer) Table(headers []string, rows [][]string) {
	if !p.styled {
		tw := tabwriter.NewWriter(p.out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, strings.Join(headers, "\t"))
		for _, r := range rows {
			_, _ = fmt.Fprintln(tw, strings.Join(r, "\t"))
		}
		_ = tw.Flush()
		return
	}
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(muted)).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return header
			}
			return cell
		})
	p.println(t.String())
}

// Success prints a positive status line.
func (p *Printer) Success(msg string) { p.status("✓", okStyle, msg) }

// Warn prints a warning line.
func (p *Printer) Warn(msg string) { p.status("!", warnSty, msg) }

// Error prints an error line.
func (p *Printer) Error(msg string) { p.status("✗", errSty, msg) }

// Line prints plain text.
func (p *Printer) Line(msg string) { p.println(msg) }

// Muted renders s dimmed on terminals.
func (p *Printer) Muted(s string) string {
	if !p.styled {
		return s
	}
	return lipgloss.NewStyle().Foreground(muted).Render(s)
}

func (p *Printer) status(mark string, style lipgloss.Style, msg string) {
	if !p.styled {
		p.println(mark + " " + msg)
		return
	}
	p.println(style.Render(mark) + " " + msg)
}
