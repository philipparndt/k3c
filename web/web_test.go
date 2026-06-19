package web

import "testing"

// actionArgs is the security-relevant mapping: only known lifecycle actions may
// produce CLI arguments, so the action endpoint cannot run an arbitrary command.
func TestActionArgs(t *testing.T) {
	cases := []struct {
		kind, name, action string
		want               []string
	}{
		{"", "vehub", "start", []string{"cluster", "start", "vehub"}},
		{"", "vehub", "stop", []string{"cluster", "stop", "vehub"}},
		{"", "vehub", "pause", []string{"cluster", "pause", "vehub"}},
		{"", "vehub", "resume", []string{"cluster", "resume", "vehub"}},
		{"docker", "docker", "start", []string{"docker", "up"}},
		{"docker", "docker", "stop", []string{"docker", "down"}},
		{"docker", "docker", "pause", []string{"docker", "pause"}},
		{"docker", "docker", "resume", []string{"docker", "resume"}},
		// rejected: not a lifecycle action
		{"", "vehub", "destroy", nil},
		{"", "vehub", "rm", nil},
		{"docker", "docker", "delete", nil},
		{"", "vehub", "", nil},
	}
	for _, c := range cases {
		got := actionArgs(c.kind, c.name, c.action)
		if len(got) != len(c.want) {
			t.Errorf("actionArgs(%q,%q,%q) = %v, want %v", c.kind, c.name, c.action, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("actionArgs(%q,%q,%q) = %v, want %v", c.kind, c.name, c.action, got, c.want)
				break
			}
		}
	}
}
