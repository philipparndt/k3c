// Package ui renders the output of k3c's informational commands (version,
// info, status, daemons, config, cluster list). It centralizes two concerns:
//
//   - a machine-readable JSON mode (--json) for automation, and
//   - clean, colorized human output that automatically degrades to plain text
//     when stdout is not a terminal or NO_COLOR is set.
//
// Color handling rides on lipgloss, whose default renderer detects the
// stdout color profile, so a redirected/piped invocation prints uncolored
// text without any extra work here.
package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// jsonOut records whether --json was given; informational commands branch on
// JSON() to emit machine-readable output instead of the human rendering.
var jsonOut bool

// SetJSON sets the global JSON output mode (wired from the root --json flag).
func SetJSON(v bool) { jsonOut = v }

// JSON reports whether the user asked for machine-readable JSON output.
func JSON() bool { return jsonOut }

// IsTTY reports whether stdout is an interactive terminal. Color is applied
// by lipgloss regardless, but commands can use this for layout decisions.
func IsTTY() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	sectStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")) // bright blue
	keyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))             // grey
	headStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("8"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	mutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// EmitJSON marshals v as indented JSON to stdout. Used by every command's
// JSON branch so the formatting is uniform.
func EmitJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// Title prints a bold top-level heading.
func Title(s string) { fmt.Println(titleStyle.Render(s)) }

// Section prints a blank line then a colored section heading.
func Section(s string) { fmt.Println("\n" + sectStyle.Render(s)) }

// KV prints an aligned "key: value" line indented under a section. width is
// the column the values align to.
func KV(key, value string, width int) {
	fmt.Printf("  %s %s\n", keyStyle.Render(pad(key+":", width+1)), value)
}

// Muted renders secondary text (e.g. "none", hints).
func Muted(s string) string { return mutedStyle.Render(s) }

// OK / Warn / Err render text in the healthy / caution / problem colors.
func OK(s string) string   { return okStyle.Render(s) }
func Warn(s string) string { return warnStyle.Render(s) }
func Err(s string) string  { return errStyle.Render(s) }

// State colorizes a state word: greens for healthy, reds for down, yellow for
// in-between. Unknown values are returned unstyled.
func State(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "running", "up", "ready", "enabled", "active", "true":
		return okStyle.Render(s)
	case "stopped", "down", "disabled", "notready", "not ready", "false", "error":
		return errStyle.Render(s)
	case "paused", "suspended", "pending", "starting", "unknown":
		return warnStyle.Render(s)
	default:
		return s
	}
}

// Table renders rows under a header with each column padded to its widest
// cell. The header is styled; cells pass through cellStyle (nil = identity),
// which receives the column index and raw value and returns the rendered
// string. Width is measured on the raw values so styling never skews columns.
func Table(header []string, rows [][]string, cellStyle func(col int, val string) string) {
	cols := len(header)
	w := make([]int, cols)
	for i, h := range header {
		w[i] = lipgloss.Width(h)
	}
	for _, r := range rows {
		for i := 0; i < cols && i < len(r); i++ {
			if cw := lipgloss.Width(r[i]); cw > w[i] {
				w[i] = cw
			}
		}
	}
	var b strings.Builder
	line := func(cells []string, style func(col int, val string) string) {
		var s strings.Builder
		for i := range cols {
			val := ""
			if i < len(cells) {
				val = cells[i]
			}
			rendered := val
			if style != nil {
				rendered = style(i, val)
			}
			if i == cols-1 {
				// pad on the raw width, then swap in the styled cell so ANSI
				// codes don't count toward the column width.
				s.WriteString(strings.Replace(pad(val, w[i]), val, rendered, 1))
			} else {
				s.WriteString(strings.Replace(pad(val, w[i]), val, rendered, 1))
				s.WriteString("  ")
			}
		}
		// trim trailing padding so empty trailing cells leave no whitespace.
		b.WriteString(strings.TrimRight(s.String(), " "))
		b.WriteByte('\n')
	}
	line(header, func(col int, val string) string { return headStyle.Render(val) })
	for _, r := range rows {
		line(r, cellStyle)
	}
	fmt.Print(b.String())
}

// pad right-pads s with spaces to width (measuring display width so emoji and
// wide runes don't misalign). Never truncates.
func pad(s string, width int) string {
	if n := width - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}
