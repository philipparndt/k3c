package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"k3c/cluster"
	"k3c/config"
)

// snapSortModel builds a loaded model with one expanded machine whose snapshots
// are deliberately out of name/date agreement, so name and date orderings
// differ. "zeta" has no creation date, to exercise the unknown-last path.
func snapSortModel() model {
	m := model{
		width:    120,
		height:   30,
		loaded:   true,
		expanded: map[string]bool{"vehub": true},
		clusters: []cluster.ClusterInfo{
			{Name: "vehub", Server: "running"},
		},
		snapsByMachine: map[string][]cluster.SnapshotInfo{
			"vehub": {
				{Name: "alpha", Mode: "warm", Created: "2026-06-01T10:00:00+02:00"},
				{Name: "bravo", Mode: "warm", Created: "2026-06-03T10:00:00+02:00"},
				{Name: "charlie", Mode: "warm", Created: "2026-06-02T10:00:00+02:00"},
				{Name: "zeta", Mode: "cold", Created: ""},
			},
		},
	}
	m.rebuildRows()
	return m
}

func snapOrder(m model) []string {
	var names []string
	for _, r := range m.rows {
		if r.kind == rowSnapshot && r.snapName != "" {
			names = append(names, r.snapName)
		}
	}
	return names
}

func TestHeaderIsFrameless(t *testing.T) {
	m := snapSortModel()
	header := m.headerView()
	// panelBox used a rounded border; its corner glyphs must not appear now.
	for _, glyph := range []string{"╭", "╮", "╰", "╯"} {
		if strings.Contains(header, glyph) {
			t.Fatalf("header should be frameless, found border glyph %q in:\n%s", glyph, header)
		}
	}
}

func TestSnapshotSortNameAndDate(t *testing.T) {
	m := snapSortModel()

	// default: name order, indicator shows "by name"
	if got := snapOrder(m); !equalStrings(got, []string{"alpha", "bravo", "charlie", "zeta"}) {
		t.Fatalf("default snapshot order = %v, want name order", got)
	}
	if !strings.Contains(m.machinesTitle(), "by name") {
		t.Fatalf("title should indicate 'by name':\n%s", m.machinesTitle())
	}

	// toggle to date (newest first): bravo(6/3), charlie(6/2), alpha(6/1), then
	// zeta (no date) last
	next, _ := m.cycleSort()
	m = next.(model)
	if got := snapOrder(m); !equalStrings(got, []string{"bravo", "charlie", "alpha", "zeta"}) {
		t.Fatalf("date order = %v, want newest-first with unknown last", got)
	}
	if !strings.Contains(m.machinesTitle(), "by date") {
		t.Fatalf("title should indicate 'by date':\n%s", m.machinesTitle())
	}

	// toggle back to name order
	next, _ = m.cycleSort()
	m = next.(model)
	if got := snapOrder(m); !equalStrings(got, []string{"alpha", "bravo", "charlie", "zeta"}) {
		t.Fatalf("order after second toggle = %v, want name order", got)
	}
}

func TestMachineOrderStableAcrossSort(t *testing.T) {
	m := snapSortModel()
	m.clusters = []cluster.ClusterInfo{{Name: "vehub"}, {Name: "docker", Kind: "docker"}}
	m.expanded["docker"] = false
	m.rebuildRows()

	machines := func() []string {
		var names []string
		for _, r := range m.rows {
			if r.kind == rowMachine {
				names = append(names, m.clusters[r.machine].Name)
			}
		}
		return names
	}
	before := machines()
	next, _ := m.cycleSort()
	m = next.(model)
	if got := machines(); !equalStrings(got, before) {
		t.Fatalf("machine order changed by sort toggle: before %v, after %v", before, got)
	}
}

func TestCycleSortPreservesSnapshotSelection(t *testing.T) {
	m := snapSortModel()
	// select snapshot "charlie"
	for i, r := range m.rows {
		if r.kind == rowSnapshot && r.snapName == "charlie" {
			m.cur = i
		}
	}
	next, _ := m.cycleSort()
	m = next.(model)
	if r, ok := m.curRow(); !ok || r.kind != rowSnapshot || r.snapName != "charlie" {
		t.Fatalf("selection after sort toggle = %+v, want snapshot charlie", r)
	}
}

func TestApplyThemeOverride(t *testing.T) {
	defer applyTheme(config.UITheme{}) // restore defaults for other tests

	applyTheme(config.UITheme{Accent: "#89D7FB"})
	want := lipgloss.AdaptiveColor{Light: "#89D7FB", Dark: "#89D7FB"}
	if accent != want {
		t.Fatalf("accent = %+v, want %+v", accent, want)
	}
	// an empty field keeps the built-in default rather than blanking it
	applyTheme(config.UITheme{})
	if accent == (lipgloss.AdaptiveColor{}) {
		t.Fatalf("empty override should keep the default accent, got zero value")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
