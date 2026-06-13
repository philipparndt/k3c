package cluster

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
	"k3c/runtime"
)

// Transparent egress (the gvisor-tap-vsock "gvnet" netstack). When enabled
// (config.TransparentEgress), every k3c VM gets, in addition to its vmnet
// NIC, a second NIC backed by a per-VM host-side userspace netstack. The
// netstack terminates the guest's TCP/IP and re-originates each connection
// from ordinary host sockets, so corporate egress (Zscaler) works
// transparently — no SNI gateway, no CoreDNS override, no per-domain config.
//
// Dual-NIC: the vmnet NIC stays for host<->VM access (published ports, the
// kube API, the docker host) and for reaching k3c's host daemons at the vmnet
// gateway; the gvnet NIC is attached first so it provides the default route
// (egress). The netstack is one-per-VM and exits when its VM disconnects, so
// it is (re)spawned before each `container run`/`container start`.

const (
	// gvnetPlugin is the container network plugin that backs a gvnet network.
	gvnetPlugin = "container-network-gvnet"
	// defaultNetwork is the builtin vmnet network every VM keeps for
	// host<->VM connectivity.
	defaultNetwork = "default"
)

func gvnetDir(cfg *config.Config) string { return filepath.Join(cfg.BaseDir, "gvnet") }

// gvnetNetworkName is the container network name backing a VM's netstack.
func gvnetNetworkName(vm string) string { return "k3c-gv-" + vm }

func gvnetSocketPath(cfg *config.Config, vm string) string {
	return filepath.Join(gvnetDir(cfg), vm+".sock")
}

func gvnetPidFile(cfg *config.Config, vm string) string {
	return filepath.Join(gvnetDir(cfg), vm+".pid")
}

// gvnetNetworks returns the `--network` arguments for a VM in transparent
// egress mode: the vmnet default network FIRST (it stays the primary NIC, so
// the runtime keeps targeting its host-routable IP for published ports and
// containerIP) and the gvnet network second. The guest default route is then
// repointed at the gvnet NIC in the entrypoint (config.GvnetRouteSnippet) so
// egress is transparent. When transparent egress is off it returns nil and the
// VM uses its implicit default network.
func gvnetNetworks(cfg *config.Config, vm string) ([]string, error) {
	if !cfg.TransparentEgress {
		return nil, nil
	}
	net, err := ensureGvnet(cfg, vm)
	if err != nil {
		return nil, err
	}
	return []string{"--network", defaultNetwork, "--network", net}, nil
}

// ensureGvnet makes a per-VM transparent-egress netstack available and returns
// the container network name to attach the VM to. It (re)spawns the netstack
// on a fixed unixgram socket and creates the gvnet network once (on a distinct
// /24). Call before `container run`/`container start` for the VM.
func ensureGvnet(cfg *config.Config, vm string) (string, error) {
	if err := os.MkdirAll(gvnetDir(cfg), 0o755); err != nil {
		return "", err
	}
	net := gvnetNetworkName(vm)
	subnet, gateway := allocateGvnetSubnet(net)
	sock := gvnetSocketPath(cfg, vm)
	// (Re)spawn the netstack if it is not running OR its socket is gone: the
	// per-VM netstack exits when its VM disconnects (leaving a stale pidfile or
	// a missing socket), so a restart needs a fresh one. Checking only the
	// pidfile is not enough — a missing socket would surface later as the
	// runtime failing to connect (ENOENT) when it attaches the VM.
	if _, err := os.Stat(sock); err != nil || !pidAlive(gvnetPidFile(cfg, vm)) {
		stopGvnet(cfg, vm) // clear any half-dead netstack/pidfile/socket first
		if err := spawnGvnet(cfg, vm, sock, subnet, gateway); err != nil {
			return "", err
		}
	}
	if err := ensureGvnetNetwork(net, sock, subnet); err != nil {
		return "", err
	}
	return net, nil
}

// spawnGvnet starts the gvnet netstack detached on sock for the given
// subnet/gateway and waits for the socket to appear.
func spawnGvnet(cfg *config.Config, vm, sock, subnet, gateway string) error {
	_ = os.Remove(sock) // stale socket from a previous netstack
	logFile, err := os.OpenFile(filepath.Join(gvnetDir(cfg), vm+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	logger.Info(fmt.Sprintf("starting transparent-egress netstack for %s (%s)", vm, subnet))
	cmd := exec.Command(runtime.GvnetBinary(),
		"-socket", "unixgram://"+sock,
		"-subnet", subnet,
		"-gateway", gateway)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	// Detach into its own session so the netstack outlives the k3c process and
	// its shell: otherwise SIGHUP on shell exit kills it, the VM loses its NIC
	// and the runtime stops the container. The netstack must live as long as
	// its VM (it exits on its own when the VM disconnects).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting gvnet netstack: %w", err)
	}
	if err := os.WriteFile(gvnetPidFile(cfg, vm), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sock); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("gvnet netstack for %s did not create %s (see %s)", vm, sock, filepath.Join(gvnetDir(cfg), vm+".log"))
}

// ensureGvnetNetwork creates the gvnet container network if missing. The
// network persists across netstack restarts (the socket path is fixed per VM).
func ensureGvnetNetwork(net, sock, subnet string) error {
	if gvnetNetworkExists(net) {
		return nil
	}
	out, err := runContainer("network", "create",
		"--plugin", gvnetPlugin,
		"--option", "gvnetSocketPath="+sock,
		"--subnet", subnet,
		net)
	if err != nil {
		return fmt.Errorf("creating gvnet network %s: %s", net, out)
	}
	return nil
}

func gvnetNetworkExists(net string) bool {
	return gvnetNetworkSubnet(net) != ""
}

// gvnetNetworkSubnet returns the subnet of an existing network, or "".
func gvnetNetworkSubnet(net string) string {
	out, _ := runContainer("network", "ls")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == net {
			return f[1]
		}
	}
	return ""
}

// allocateGvnetSubnet returns the /24 (and its .1 gateway) for a VM's gvnet
// network. Per-VM networks must not overlap (the apiserver rejects that), so
// each gets a distinct /24 from 192.168.127.0/24 upward; an existing network's
// subnet is reused so the assignment is stable across restarts.
func allocateGvnetSubnet(net string) (subnet, gateway string) {
	used := map[int]bool{}
	out, _ := runContainer("network", "ls")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		oct, ok := thirdOctet192168(f[1])
		if !ok {
			continue
		}
		if f[0] == net {
			return f[1], fmt.Sprintf("192.168.%d.1", oct) // reuse existing
		}
		used[oct] = true
	}
	for oct := 127; oct <= 254; oct++ {
		if !used[oct] {
			return fmt.Sprintf("192.168.%d.0/24", oct), fmt.Sprintf("192.168.%d.1", oct)
		}
	}
	return "192.168.127.0/24", "192.168.127.1" // fallback (should not happen)
}

// thirdOctet192168 parses the third octet of a "192.168.X.0/24" CIDR.
func thirdOctet192168(cidr string) (int, bool) {
	if !strings.HasPrefix(cidr, "192.168.") {
		return 0, false
	}
	rest := strings.TrimPrefix(cidr, "192.168.")
	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:dot])
	if err != nil {
		return 0, false
	}
	return n, true
}

// stopGvnet tears down a VM's netstack: it usually has already exited (it ends
// when its VM disconnects), so this kills any lingering process and removes the
// pidfile and socket. The network object is left in place for a later restart.
func stopGvnet(cfg *config.Config, vm string) {
	pidFile := gvnetPidFile(cfg, vm)
	if data, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
			}
		}
	}
	_ = os.Remove(pidFile)
	_ = os.Remove(gvnetSocketPath(cfg, vm))
}

// removeGvnet fully removes a VM's gvnet network (after the container itself is
// gone) in addition to stopping its netstack.
func removeGvnet(cfg *config.Config, vm string) {
	stopGvnet(cfg, vm)
	_, _ = runContainer("network", "rm", gvnetNetworkName(vm))
}
