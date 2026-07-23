package cluster

import (
	"errors"
	"testing"

	"k3c/config"
)

// The docker sidecar is its own VM with its own gvnet netstack; cluster repair
// must rebuild its forwarding plane too (customer report: repair fixed the
// server VM, the sidecar's path to the pull cache stayed dead).

func TestRepairSidecarForwardingRunsDownThenUp(t *testing.T) {
	var calls []string
	err := repairSidecarForwarding(true,
		func() error { calls = append(calls, "down"); return nil },
		func() error { calls = append(calls, "up"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "down" || calls[1] != "up" {
		t.Fatalf("want [down up], got %v", calls)
	}
}

func TestRepairSidecarForwardingSkipsStoppedSidecar(t *testing.T) {
	err := repairSidecarForwarding(false,
		func() error { t.Fatal("down must not be called"); return nil },
		func() error { t.Fatal("up must not be called"); return nil })
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepairSidecarForwardingPropagatesDownError(t *testing.T) {
	boom := errors.New("boom")
	err := repairSidecarForwarding(true,
		func() error { return boom },
		func() error { t.Fatal("up must not be called after a failed down"); return nil })
	if !errors.Is(err, boom) {
		t.Fatalf("want down error, got %v", err)
	}
}

func TestRepairSidecarForwardingPropagatesUpError(t *testing.T) {
	boom := errors.New("boom")
	err := repairSidecarForwarding(true,
		func() error { return nil },
		func() error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("want up error, got %v", err)
	}
}

// doctor probes the gateway path from INSIDE the sidecar — the path image
// pulls take to the pull cache / local registry. The pull cache is preferred
// (dockerd's registry mirror), the local registry is the fallback.

func TestDockerGatewayProbeURLPrefersPullCache(t *testing.T) {
	cfg := &config.Config{
		VmnetGateway:     "192.168.64.1",
		PullCacheEnabled: true, PullCachePort: "5011",
		RegistryEnabled: true, RegistryPort: "5001",
	}
	if got, want := dockerGatewayProbeURL(cfg), "http://192.168.64.1:5011/v2/"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDockerGatewayProbeURLFallsBackToRegistry(t *testing.T) {
	cfg := &config.Config{
		VmnetGateway:    "192.168.64.1",
		RegistryEnabled: true, RegistryPort: "5001",
	}
	if got, want := dockerGatewayProbeURL(cfg), "http://192.168.64.1:5001/v2/"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDockerGatewayProbeURLEmptyWhenNothingToProbe(t *testing.T) {
	if got := dockerGatewayProbeURL(&config.Config{VmnetGateway: "192.168.64.1"}); got != "" {
		t.Fatalf("no gateway service enabled: want empty, got %q", got)
	}
	if got := dockerGatewayProbeURL(&config.Config{PullCacheEnabled: true, PullCachePort: "5011"}); got != "" {
		t.Fatalf("no vmnet gateway: want empty, got %q", got)
	}
}
