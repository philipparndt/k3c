package cluster

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k3c/config"
)

// A real daemons.log tail from a spawn that lost :443 to OrbStack: colour
// codes plus the logger's elapsed-time prefix wrap the line that matters.
const daemonLogSample = "\x1b[37m[   0]\x1b[0m \x1b[0mlistening on 0.0.0.0:3128\x1b[0m\n" +
	"\x1b[37m[   0]\x1b[0m \x1b[0mdocker: engine socket at /Users/x/.config/k3c/docker.sock\x1b[0m\n" +
	"\x1b[37m[   0]\x1b[0m \x1b[31mlisten tcp 0.0.0.0:443: bind: address already in use\x1b[0m\n"

func TestDaemonLogTailQuotesThisSpawnOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{BaseDir: dir}
	stale := "\x1b[37m[ 300]\x1b[0m \x1b[0mpruned 0 stale tags\x1b[0m\n"
	if err := os.WriteFile(cfg.DaemonLogFile(), []byte(stale+daemonLogSample), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reading from this spawn's offset must skip the earlier run's lines and
	// strip both the colour codes and the "[   0]" prefix.
	got := daemonLogTail(cfg, int64(len(stale)))
	want := "listen tcp 0.0.0.0:443: bind: address already in use"
	if got != want {
		t.Fatalf("daemonLogTail = %q; want %q", got, want)
	}
	// An offset past the end (nothing logged) yields no reason, so the caller
	// falls back to the plain "see the log" message.
	if got := daemonLogTail(cfg, int64(len(stale)+len(daemonLogSample))); got != "" {
		t.Fatalf("daemonLogTail past end = %q; want empty", got)
	}
}

func TestDaemonLogTailMissingFile(t *testing.T) {
	cfg := &config.Config{BaseDir: filepath.Join(t.TempDir(), "absent")}
	if got := daemonLogTail(cfg, 0); got != "" {
		t.Fatalf("daemonLogTail = %q; want empty", got)
	}
}

// heldDaemonPorts must spot an occupied listener port, and portConflictError
// must name the port and its role rather than sending the user to the log.
func TestHeldDaemonPortsReportsOccupiedListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	// Only the proxy port is occupied; sni-gateway :443 and the registry port
	// are left unset/free so the test does not depend on the host's state.
	cfg := &config.Config{BaseDir: t.TempDir(), ProxyPort: port}
	var held []listenerSpec
	for _, s := range heldDaemonPorts(cfg) {
		if s.port == port {
			held = append(held, s)
		}
	}
	if len(held) != 1 || held[0].name != "proxy" {
		t.Fatalf("heldDaemonPorts = %+v; want the proxy on :%s", held, port)
	}

	err = portConflictError(held)
	if err == nil {
		t.Fatal("portConflictError = nil; want an error")
	}
	msg := err.Error()
	for _, want := range []string{"port already in use", ":" + port, "proxy"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("portConflictError = %q; want it to mention %q", msg, want)
		}
	}
}

// The daemons bind the registry port whether or not the local registry is
// enabled, so the pre-spawn check has to cover it too.
func TestDaemonListenersAlwaysIncludeRegistry(t *testing.T) {
	cfg := &config.Config{BaseDir: t.TempDir(), ProxyPort: "3128", RegistryPort: "5001"}
	var names []string
	for _, s := range daemonListeners(cfg) {
		names = append(names, s.name+":"+s.port)
	}
	for _, want := range []string{"proxy:3128", "sni-gateway:443", "registry:5001"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("daemonListeners = %v; want it to include %s", names, want)
		}
	}
}
