package tui

import (
	"strings"
	"testing"

	"k3c/cluster"
)

func TestDiagramScreenRenders(t *testing.T) {
	m := model{
		width:  120,
		height: 40,
		clusters: []cluster.ClusterInfo{
			{Name: "vehub", Server: "running", RAM: "32GB", Context: "k3d-vehub", Active: true},
			{Name: "docker", Server: "running", RAM: "48G", Kind: "docker"},
		},
		cacheLine: "99% hits · cache 7.4 GB · up 20.1 MB",
		daemons: cluster.DaemonsInfo{
			State: "running",
			Pid:   "1234",
			Listeners: []cluster.ListenerState{
				{Name: "proxy", Port: "3128", Up: true},
				{Name: "sni-gateway", Port: "443", Up: true},
				{Name: "forward", Port: "9480", Detail: "-> 127.0.0.1:3128", Up: false},
				{Name: "pull-cache", Port: "5011", Up: true},
			},
		},
	}

	m.openDiagram()
	out := m.diagramScreen()
	for _, want := range []string{"system", "host daemon", "container runtime", "pull-cache", "vehub", "docker", "running", "proxy"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagram missing %q\n%s", want, out)
		}
	}
	t.Logf("\n%s", out)

	// narrow terminal: must fall back, not panic
	m.width = 30
	if got := m.diagramScreen(); !strings.Contains(got, "resize") {
		t.Errorf("narrow diagram should show resize hint, got:\n%s", got)
	}
}

// On a short terminal the diagram drops its frame, fits, and scrolls with the
// cursor keys instead of closing.
func TestDiagramScrollsOnShortTerminal(t *testing.T) {
	const w, h = 70, 16
	m := model{
		width: w, height: h,
		clusters: []cluster.ClusterInfo{
			{Name: "vehub", Server: "running", RAM: "32GB", Context: "k3d-vehub", Active: true},
			{Name: "docker", Server: "running", RAM: "48G", Kind: "docker"},
		},
		daemons: cluster.DaemonsInfo{State: "running", Pid: "1234",
			Listeners: []cluster.ListenerState{
				{Name: "proxy", Port: "3128", Up: true},
				{Name: "pull-cache", Port: "5011", Up: true},
			}},
	}
	m.openDiagram()
	out := m.diagramScreen()
	lines := strings.Split(out, "\n")
	if len(lines) > h {
		t.Errorf("diagram is %d lines, exceeds height %d\n%s", len(lines), h, out)
	}
	if strings.Contains(out, "╭──────────────────────────────────────") {
		t.Errorf("compact diagram should drop its outer frame\n%s", out)
	}

	next, _ := m.Update(key("j"))
	nm := next.(model)
	if !nm.showDiagram {
		t.Fatal("scroll key closed the diagram; want scroll")
	}
	if nm.diagramVP.YOffset == 0 {
		t.Error("diagram did not scroll on a short terminal")
	}
	if closed, _ := nm.Update(key("esc")); closed.(model).showDiagram {
		t.Error("esc did not close the diagram")
	}
}
