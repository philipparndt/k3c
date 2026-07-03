package tui

import "testing"

// A pending runtime update opens the restart confirm dialog once: declining
// it must not re-open the dialog on the next refresh.
func TestRuntimeUpdateOffersRestartOnce(t *testing.T) {
	m := model{width: 100, height: 30}
	next, _ := m.Update(dataMsg{runtimeUpdate: true})
	nm := next.(model)
	if nm.confirm == nil {
		t.Fatal("pending runtime update did not open the confirm dialog")
	}
	if nm.confirm.yesLabel != "Restart" {
		t.Errorf("yesLabel = %q, want Restart", nm.confirm.yesLabel)
	}
	if !nm.restartOffered {
		t.Error("restartOffered not set with the dialog open")
	}

	// decline (esc), then another refresh reporting the same pending update
	next2, _ := nm.Update(key("esc"))
	nm2 := next2.(model)
	if nm2.confirm != nil {
		t.Fatal("esc did not close the dialog")
	}
	next3, _ := nm2.Update(dataMsg{runtimeUpdate: true})
	if next3.(model).confirm != nil {
		t.Error("declined restart was re-offered on the next refresh")
	}
}

// The restart offer never replaces a dialog that is already open, and stays
// eligible for a later refresh (restartOffered remains unset).
func TestRuntimeUpdateDoesNotStompOpenDialog(t *testing.T) {
	m := model{width: 100, height: 30}
	m.confirm = &confirm{prompt: "Delete something?", yesLabel: "Delete"}
	next, _ := m.Update(dataMsg{runtimeUpdate: true})
	nm := next.(model)
	if nm.confirm == nil || nm.confirm.prompt != "Delete something?" {
		t.Fatal("runtime-update offer replaced an open confirm dialog")
	}
	if nm.restartOffered {
		t.Error("restartOffered set while the offer was suppressed")
	}
}
