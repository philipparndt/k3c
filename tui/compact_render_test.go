package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"k3c/cluster"
)

// compactModel builds a model with one expanded machine carrying many snapshot
// rows — more than fit on a small terminal — so the scrolling/truncation paths
// are exercised.
func compactModel(width, height int) model {
	m := model{
		width:    width,
		height:   height,
		loaded:   true,
		expanded: map[string]bool{"vehub": true},
		clusters: []cluster.ClusterInfo{
			{Name: "vehub", Server: "running", RAM: "53.7 GB", Context: "k3d-vehub", Active: true},
		},
	}
	m.rows = []treeRow{{kind: rowMachine, machine: 0}}
	for i := 0; i < 20; i++ {
		m.rows = append(m.rows, treeRow{
			kind:     rowSnapshot,
			machine:  0,
			snapName: fmt.Sprintf("snapshot-%02d", i),
			snapMode: "warm",
			snapSize: "26.0 GB",
			snapWhen: "2026-06-29T14:52:50+02:00",
		})
	}
	return m
}

// rowLabel is the text that identifies row i (machine name or snapshot name),
// used to assert the selected row is on screen.
func rowLabel(r treeRow, m model) string {
	if r.kind == rowMachine {
		return m.clusters[r.machine].Name
	}
	return r.snapName
}

func TestCompactViewNoWrapAndScrolls(t *testing.T) {
	const w, h = 50, 15
	base := compactModel(w, h)
	if !base.compact() {
		t.Fatalf("%dx%d should be compact", w, h)
	}

	// selection at first, middle and last row — each must stay on screen, and
	// the frame must never wrap or overflow the terminal.
	for _, cur := range []int{0, len(base.rows) / 2, len(base.rows) - 1} {
		m := base
		m.cur = cur
		m.clampScroll()
		out := m.compactView()
		lines := strings.Split(out, "\n")

		if len(lines) > h {
			t.Errorf("cur=%d: %d lines exceeds height %d\n%s", cur, len(lines), h, out)
		}
		for i, ln := range lines {
			if lw := lipgloss.Width(ln); lw > w {
				t.Errorf("cur=%d: line %d width %d exceeds %d: %q", cur, i, lw, w, ln)
			}
		}
		if want := rowLabel(m.rows[cur], m); !strings.Contains(out, want) {
			t.Errorf("cur=%d: selected row %q not visible\n%s", cur, want, out)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		w    int
		want int // expected visible width of the result
	}{
		{"short fits", "abc", 10, 3},
		{"exact fit", "abcde", 5, 5},
		{"clipped", "abcdefghij", 5, 5}, // 4 runes + "…"
		{"zero width", "abc", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncate(c.in, c.w)
			if w := lipgloss.Width(got); w != c.want {
				t.Errorf("truncate(%q, %d) width = %d, want %d (%q)", c.in, c.w, w, c.want, got)
			}
		})
	}

	// styled input is measured by visible width, not byte length: the ANSI
	// escapes must not count toward the budget and must stay intact.
	styled := keySt.Render("running") // 7 visible cols wrapped in escape codes
	if got := truncate(styled, 20); got != styled {
		t.Errorf("styled string within width was altered: %q", got)
	}
	if got := truncate(styled, 4); lipgloss.Width(got) != 4 {
		t.Errorf("styled truncate width = %d, want 4 (%q)", lipgloss.Width(got), got)
	}
}

// The wide terminal must keep the original side-by-side layout untouched.
func TestWideTerminalNotCompact(t *testing.T) {
	m := compactModel(120, 40)
	if m.compact() {
		t.Fatal("120x40 should not be compact")
	}
}

// A long confirm prompt must wrap to fit a narrow terminal rather than overflow.
func TestConfirmDialogWrapsOnNarrowTerminal(t *testing.T) {
	const w = 56
	m := model{width: w, height: 24}
	m.confirm = &confirm{
		prompt:      `Restore snapshot "before-model" into "vehub"? Its current state is replaced and host daemons restart.`,
		yesLabel:    "Restore",
		destructive: true,
	}
	out := m.confirmScreen()
	lines := strings.Split(out, "\n")
	wrapped := false
	for i, ln := range lines {
		if lw := lipgloss.Width(ln); lw > w {
			t.Errorf("confirm line %d width %d exceeds %d: %q", i, lw, w, ln)
		}
		if strings.Contains(ln, "current state") && !strings.Contains(ln, "Restore snapshot") {
			wrapped = true // the prompt spilled onto a second line
		}
	}
	if !wrapped {
		t.Errorf("expected the long prompt to wrap across lines\n%s", out)
	}
}

// The help dialog packs into the available width, drops its frame, and scrolls
// rather than overflowing a small terminal.
func TestHelpDialogFitsNarrowTerminal(t *testing.T) {
	const w, h = 56, 16
	m := model{width: w, height: h}
	m.openHelp()
	out := m.helpScreen()
	lines := strings.Split(out, "\n")
	if len(lines) > h {
		t.Errorf("help is %d lines, exceeds height %d\n%s", len(lines), h, out)
	}
	for i, ln := range lines {
		if lw := lipgloss.Width(ln); lw > w {
			t.Errorf("help line %d width %d exceeds %d: %q", i, lw, w, ln)
		}
	}
	if strings.Contains(out, "╭") {
		t.Errorf("compact help should drop its frame\n%s", out)
	}
	if !strings.Contains(out, "GENERAL") {
		t.Errorf("help body missing\n%s", out)
	}
}

// Scrolling keys drive the help viewport instead of closing the dialog.
func TestHelpScrollKeysDoNotClose(t *testing.T) {
	m := model{width: 56, height: 16}
	m.openHelp()
	top := m.helpVP.YOffset
	next, _ := m.Update(key("j"))
	nm := next.(model)
	if !nm.showHelp {
		t.Fatal("scroll key closed the help dialog; want scroll")
	}
	if nm.helpVP.YOffset <= top {
		t.Errorf("help did not scroll: offset %d -> %d", top, nm.helpVP.YOffset)
	}
	// esc still closes.
	closed, _ := nm.Update(key("esc"))
	if closed.(model).showHelp {
		t.Error("esc did not close the help dialog")
	}
}
