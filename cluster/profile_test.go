package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k3c/config"
)

func TestParsePodLine(t *testing.T) {
	uid, s, ok := parsePodLine("pod1234_5678.slice 1500000 2097152 1048576")
	if !ok {
		t.Fatal("expected line to parse")
	}
	if uid != "1234-5678" {
		t.Errorf("uid = %q, want 1234-5678 (pod prefix/.slice stripped, _ -> -)", uid)
	}
	if s.CPUUsec != 1500000 {
		t.Errorf("CPUUsec = %d, want 1500000", s.CPUUsec)
	}
	if s.MemCurrent != 2097152 {
		t.Errorf("MemCurrent = %d, want 2097152", s.MemCurrent)
	}
	// working set = memory.current - inactive_file
	if s.MemWorkingSet != 2097152-1048576 {
		t.Errorf("MemWorkingSet = %d, want %d", s.MemWorkingSet, 2097152-1048576)
	}
}

func TestParsePodLineWorkingSetFloor(t *testing.T) {
	// inactive_file larger than memory.current must not yield a negative ws.
	_, s, ok := parsePodLine("pod1 100 0 500")
	if !ok {
		t.Fatal("expected line to parse")
	}
	if s.MemWorkingSet != 0 {
		t.Errorf("MemWorkingSet = %d, want 0 (floored)", s.MemWorkingSet)
	}
}

func TestParsePodLineRejectsMalformed(t *testing.T) {
	for _, line := range []string{"", "===", "pod1 1 2", "pod1 a b c"} {
		if _, _, ok := parsePodLine(line); ok {
			t.Errorf("parsePodLine(%q) = ok, want rejected", line)
		}
	}
}

func TestSnapshotJSONShape(t *testing.T) {
	snap := Snapshot{
		TimeMillis: 1700000000000,
		Pods: map[string]PodSample{
			"uid-a": {Name: "ns/pod-a", CPUUsec: 42, MemWorkingSet: 100, MemCurrent: 200},
		},
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(snap); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{`"t_ms":1700000000000`, `"name":"ns/pod-a"`, `"cpu_usec":42`, `"mem_ws":100`, `"mem_current":200`} {
		if !strings.Contains(got, want) {
			t.Errorf("snapshot JSON %s missing %s", got, want)
		}
	}
}

func TestProfileStreamErrorsWhenClusterNotRunning(t *testing.T) {
	// With no container runtime present in the test environment, the target
	// server cannot exist, so the stream must fail fast rather than block.
	cfg := &config.Config{Cluster: "nope", ServerName: "nope-server"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := ProfileStream(ctx, cfg, 500*time.Millisecond, false); err == nil {
		t.Fatal("expected an error for a cluster that is not running")
	}
}
