package cluster

import (
	"testing"
	"time"
)

func TestAutoReclaimInterval(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"10m", 10 * time.Minute, true},
		{"1h", time.Hour, true},
		{"30s", time.Minute, true}, // clamped to the 1m floor
		{"off", 0, false},
		{"false", 0, false},
		{"0", 0, false},
		{"garbage", 0, false},
	}
	for _, c := range cases {
		got, ok := autoReclaimInterval(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("autoReclaimInterval(%q) = %v,%v want %v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestReclaimHeadroom(t *testing.T) {
	if got := reclaimHeadroomMB(1000); got != 2048 {
		t.Errorf("small workload headroom = %d, want 2048", got)
	}
	if got := reclaimHeadroomMB(20000); got != 5000 {
		t.Errorf("large workload headroom = %d, want 5000", got)
	}
}

func TestAutoReclaimDrift(t *testing.T) {
	if got := autoReclaimDriftMB(8000); got != 4096 {
		t.Errorf("small target drift = %d, want 4096", got)
	}
	if got := autoReclaimDriftMB(24000); got != 6000 {
		t.Errorf("large target drift = %d, want 6000", got)
	}
}
