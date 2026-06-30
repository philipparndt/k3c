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
