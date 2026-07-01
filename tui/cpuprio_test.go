package tui

import (
	"strings"
	"testing"

	"k3c/cluster"
)

// TestCPUPrioLine pins the info-panel CPU line's contract: a running,
// deprioritized VM reads as such; an enabled-but-drifted one warns; a
// non-running VM and a feature-off VM degrade to neutral text. The badge is
// driven by the stable nice-based state, so these are the only cases.
func TestCPUPrioLine(t *testing.T) {
	cases := []struct {
		name    string
		info    cluster.ClusterInfo
		want    string
		notWant string
	}{
		{"deprioritized", cluster.ClusterInfo{Server: "running", CPUPrio: "low"}, "deprioritized", "drifted"},
		{"drifted", cluster.ClusterInfo{Server: "running", CPUPrio: "drifted"}, "drifted", "deprioritized"},
		{"normal", cluster.ClusterInfo{Server: "running", CPUPrio: ""}, "normal", "deprioritized"},
		{"stopped", cluster.ClusterInfo{Server: "stopped", CPUPrio: "low"}, "—", "deprioritized"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cpuPrioLine(tc.info)
			if !strings.Contains(got, tc.want) {
				t.Errorf("cpuPrioLine(%+v) = %q, want it to contain %q", tc.info, got, tc.want)
			}
			if tc.notWant != "" && strings.Contains(got, tc.notWant) {
				t.Errorf("cpuPrioLine(%+v) = %q, should not contain %q", tc.info, got, tc.notWant)
			}
		})
	}
}
