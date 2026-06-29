package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// key builds a KeyMsg for the given string-ish key, matching how Update
// switches on msg.String().
func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestConfirmScreenRendersButtons(t *testing.T) {
	m := model{width: 100, height: 30}
	m.confirm = &confirm{
		prompt:      "Delete snapshot \"foo\" of \"bar\"?",
		cmd:         func() tea.Msg { return nil },
		yesLabel:    "Delete",
		destructive: true,
	}
	out := m.confirmScreen()
	for _, want := range []string{"Confirm", "Cancel", "Delete", "select"} {
		if !strings.Contains(out, want) {
			t.Errorf("confirm dialog missing %q\n%s", want, out)
		}
	}
	t.Logf("\n%s", out)
}

// The affirmative button defaults to unfocused (Cancel is focused first), so a
// bare Enter on a destructive dialog cancels rather than firing the action.
func TestConfirmEnterDefaultsToCancel(t *testing.T) {
	fired := false
	m := model{width: 100, height: 30}
	m.confirm = &confirm{
		prompt:      "Delete?",
		cmd:         func() tea.Msg { fired = true; return nil },
		yesLabel:    "Delete",
		destructive: true,
	}
	next, cmd := m.Update(key("enter"))
	nm := next.(model)
	if nm.confirm != nil {
		t.Fatal("dialog still open after enter")
	}
	if cmd != nil {
		cmd() // would set fired if it were the action cmd
	}
	if fired {
		t.Error("enter on default focus fired the destructive action; want cancel")
	}
	if nm.status != "cancelled" {
		t.Errorf("status = %q, want cancelled", nm.status)
	}
}

// Right then Enter moves focus onto the affirmative button and fires it.
func TestConfirmRightEnterFiresAction(t *testing.T) {
	fired := false
	m := model{width: 100, height: 30}
	m.confirm = &confirm{
		prompt:   "Proceed?",
		cmd:      func() tea.Msg { fired = true; return nil },
		yesLabel: "OK",
	}
	next, _ := m.Update(key("right"))
	nm := next.(model)
	if nm.confirm == nil {
		t.Fatal("navigation closed the dialog")
	}
	if nm.confirm.focus != 1 {
		t.Fatalf("focus = %d after right, want 1", nm.confirm.focus)
	}
	next2, cmd := nm.Update(key("enter"))
	if next2.(model).confirm != nil {
		t.Fatal("dialog still open after enter on action")
	}
	if cmd == nil {
		t.Fatal("enter on action button returned no command")
	}
	cmd()
	if !fired {
		t.Error("affirmative action did not fire")
	}
}

// A three-way confirm (noCmd) lays out Cancel / decline / affirmative, and the
// decline button runs noCmd.
func TestConfirmThreeWayButtons(t *testing.T) {
	yes, no := false, false
	c := confirm{
		prompt:   "Also delete snapshots?",
		cmd:      func() tea.Msg { yes = true; return nil },
		noCmd:    func() tea.Msg { no = true; return nil },
		yesLabel: "Delete snapshots",
		noLabel:  "Keep snapshots",
	}
	btns := c.buttons()
	if len(btns) != 3 {
		t.Fatalf("got %d buttons, want 3", len(btns))
	}
	if btns[0].label != "Cancel" || btns[1].label != "Keep snapshots" || btns[2].label != "Delete snapshots" {
		t.Fatalf("button order wrong: %q / %q / %q", btns[0].label, btns[1].label, btns[2].label)
	}
	// focus the middle (decline) button and activate it
	m := model{width: 100, height: 30}
	m.confirm = &c
	next, _ := m.Update(key("right"))
	next2, cmd := next.(model).Update(key("enter"))
	if next2.(model).confirm != nil {
		t.Fatal("dialog still open")
	}
	cmd()
	if !no || yes {
		t.Errorf("decline button: noCmd fired=%v, cmd fired=%v; want no only", no, yes)
	}
}
