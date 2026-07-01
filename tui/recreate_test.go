package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"k3c/cluster"
)

func recreateModel() model {
	m := model{
		loaded:   true,
		clusters: []cluster.ClusterInfo{{Name: "vehub", Server: "running"}},
		expanded: map[string]bool{"vehub": true},
		snapsByMachine: map[string][]cluster.SnapshotInfo{
			"vehub": {{Name: "golden", Mode: "warm", Created: "2026-06-01T10:00:00+02:00"}},
		},
	}
	m.rebuildRows()
	return m
}

func keyC() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}} }

func TestCreateOnSnapshotOpensRecreateDialog(t *testing.T) {
	m := recreateModel()
	for i, r := range m.rows {
		if r.kind == rowSnapshot {
			m.cur = i
		}
	}
	next, _ := m.handleKey(keyC())
	nm := mustModel(t, next)

	if nm.confirm == nil {
		t.Fatal("c on a snapshot row did not open a dialog")
	}
	if nm.input != nil {
		t.Error("c on a snapshot row opened the wizard directly instead of the dialog")
	}
	btns := nm.confirm.buttons()
	if len(btns) != 3 {
		t.Fatalf("dialog has %d buttons, want 3 (Cancel/New/Recreate)", len(btns))
	}
	if nm.confirm.focus != 1 {
		t.Errorf("default focus = %d, want 1 (New snapshot)", nm.confirm.focus)
	}
	if btns[1].label != "New snapshot" || btns[2].label != "Recreate" {
		t.Errorf("button labels = %q/%q, want New snapshot/Recreate", btns[1].label, btns[2].label)
	}
	if btns[1].destructive {
		t.Error("the New snapshot button should not be destructive")
	}
	if !btns[2].destructive {
		t.Error("the Recreate button is not marked destructive")
	}
}

func TestCreateOnMachineOpensWizard(t *testing.T) {
	m := recreateModel()
	m.cur = 0 // machine row
	next, _ := m.handleKey(keyC())
	nm := mustModel(t, next)

	if nm.input == nil {
		t.Fatal("c on a machine row did not open the create wizard")
	}
	if nm.confirm != nil {
		t.Error("c on a machine row opened a dialog instead of the wizard")
	}
}

func mustModel(t *testing.T, m tea.Model) model {
	t.Helper()
	mm, ok := m.(model)
	if !ok {
		t.Fatalf("handleKey returned %T, want model", m)
	}
	return mm
}
