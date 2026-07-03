package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"k3c/cluster"
)

func restoreModel(mode string) model {
	m := model{
		loaded:   true,
		clusters: []cluster.ClusterInfo{{Name: "vehub", Server: "running"}},
		expanded: map[string]bool{"vehub": true},
		snapsByMachine: map[string][]cluster.SnapshotInfo{
			"vehub": {{Name: "golden", Mode: mode, Created: "2026-06-01T10:00:00+02:00"}},
		},
	}
	m.rebuildRows()
	for i, r := range m.rows {
		if r.kind == rowSnapshot {
			m.cur = i
		}
	}
	return m
}

// A warm snapshot offers the restore-tier choice: Cancel (default), warm
// (resume the saved machine, today's behavior) first, then cold (boot
// fresh, less RAM held).
func TestRestoreWarmSnapshotOffersColdChoice(t *testing.T) {
	m := restoreModel(string(cluster.ModeWarm))
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	nm := mustModel(t, next)

	if nm.confirm == nil {
		t.Fatal("enter on a warm snapshot row did not open a dialog")
	}
	btns := nm.confirm.buttons()
	if len(btns) != 3 {
		t.Fatalf("dialog has %d buttons, want 3 (Cancel/Restore warm/Restore cold)", len(btns))
	}
	if nm.confirm.focus != 0 {
		t.Errorf("default focus = %d, want 0 (Cancel)", nm.confirm.focus)
	}
	if btns[0].label != "Cancel" || btns[1].label != "Restore warm" || btns[2].label != "Restore cold" {
		t.Errorf("button labels = %q/%q/%q, want Cancel/Restore warm/Restore cold",
			btns[0].label, btns[1].label, btns[2].label)
	}
	if !btns[1].destructive || !btns[2].destructive {
		t.Errorf("both restore buttons must be destructive (replace current state): cold=%v warm=%v",
			btns[1].destructive, btns[2].destructive)
	}
	if !strings.Contains(nm.confirm.prompt, "cold boots fresh") {
		t.Errorf("prompt does not explain the warm/cold difference: %q", nm.confirm.prompt)
	}
}

// A cold (or frozen) snapshot has no saved machine state, so the plain
// two-button restore dialog remains.
func TestRestoreColdSnapshotKeepsPlainDialog(t *testing.T) {
	for _, mode := range []string{string(cluster.ModeCold), string(cluster.ModeFrozen)} {
		m := restoreModel(mode)
		next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
		nm := mustModel(t, next)

		if nm.confirm == nil {
			t.Fatalf("enter on a %s snapshot row did not open a dialog", mode)
		}
		btns := nm.confirm.buttons()
		if len(btns) != 2 {
			t.Fatalf("%s snapshot dialog has %d buttons, want 2 (Cancel/Restore)", mode, len(btns))
		}
		if btns[1].label != "Restore" {
			t.Errorf("%s snapshot affirmative label = %q, want Restore", mode, btns[1].label)
		}
	}
}
