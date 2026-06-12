package cluster

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
	"k3c/version"
)

// Host-side daemons. Apple container VMs have no outbound connectivity
// while a corporate full-tunnel VPN claims all traffic, but they can reach
// the host at the vmnet gateway. Two listeners bridge that gap:
//
//   - CONNECT proxy: used by containerd inside the node (via HTTP(S)_PROXY)
//     for image pulls.
//   - SNI gateway (:443): pod HTTPS egress. CoreDNS answers egress-domain
//     queries with the gateway IP (see config.CorednsCustom); this listener
//     reads the TLS ClientHello SNI and splices to the real host (over the
//     VPN), or to the cluster ingress for the configured ingress domains.
//     TLS stays end-to-end.

const (
	connectTimeout = 10 * time.Second
	bufSize        = 65536
)

func allowedSource(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	// dual-stack listeners hand IPv4 peers over as IPv4-mapped IPv6
	// addresses (::ffff:192.168.64.x) — normalize before matching
	if v4 := ip.To4(); v4 != nil {
		host = v4.String()
	}
	return strings.HasPrefix(host, "192.168.64.") || ip.IsLoopback()
}

func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	pump := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go pump(a, b)
	go pump(b, a)
	<-done // first direction closing tears down both
	a.Close()
	b.Close()
	<-done
}

// handleRegistryConn forwards the public registry port to the active
// cluster's registry.
func handleRegistryConn(conn net.Conn, cfg *config.Config) {
	defer conn.Close()
	if !allowedSource(conn.RemoteAddr()) {
		return
	}
	active := readActive(cfg)
	upstream, err := net.DialTimeout("tcp",
		net.JoinHostPort("127.0.0.1", active.RegistryPort), connectTimeout)
	if err != nil {
		return
	}
	splice(conn, upstream)
}

// --- CONNECT proxy ---

func handleProxyConn(conn net.Conn) {
	defer conn.Close()
	if !allowedSource(conn.RemoteAddr()) {
		return
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	parts := strings.Fields(line)
	if len(parts) < 2 || parts[0] != "CONNECT" {
		fmt.Fprint(conn, "HTTP/1.1 501 Not Implemented\r\n\r\n")
		return
	}
	// drain remaining request headers
	for {
		h, err := reader.ReadString('\n')
		if err != nil || h == "\r\n" || h == "\n" {
			break
		}
	}
	upstream, err := net.DialTimeout("tcp", parts[1], connectTimeout)
	if err != nil {
		fmt.Fprint(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	// hand over any bytes the client pipelined behind the headers
	if n := reader.Buffered(); n > 0 {
		buffered, _ := reader.Peek(n)
		if _, err := upstream.Write(buffered); err != nil {
			upstream.Close()
			return
		}
	}
	splice(conn, upstream)
}

// --- SNI gateway ---

// readClientHello reads exactly one TLS record from the connection.
func readClientHello(conn net.Conn) ([]byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	recordLen := int(binary.BigEndian.Uint16(header[3:5]))
	record := make([]byte, recordLen)
	if _, err := io.ReadFull(conn, record); err != nil {
		return nil, err
	}
	return append(header, record...), nil
}

// parseSNI extracts the server_name from a TLS ClientHello, or "".
func parseSNI(data []byte) (name string) {
	defer func() {
		if recover() != nil {
			name = ""
		}
	}()
	if data[0] != 0x16 || data[5] != 0x01 { // handshake record, ClientHello
		return ""
	}
	pos := 5 + 4 + 2 + 32                                    // record header, msg header, client version, random
	pos += 1 + int(data[pos])                                // session id
	pos += 2 + int(binary.BigEndian.Uint16(data[pos:pos+2])) // cipher suites
	pos += 1 + int(data[pos])                                // compression methods
	extEnd := pos + 2 + int(binary.BigEndian.Uint16(data[pos:pos+2]))
	pos += 2
	for pos+4 <= extEnd {
		extType := binary.BigEndian.Uint16(data[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		pos += 4
		if extType == 0 { // server_name
			nameLen := int(binary.BigEndian.Uint16(data[pos+3 : pos+5]))
			return string(data[pos+5 : pos+5+nameLen])
		}
		pos += extLen
	}
	return ""
}

func matchesDomain(name string, domains []string) bool {
	for _, d := range domains {
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

func handleSNIConn(conn net.Conn, cfg *config.Config) {
	defer conn.Close()
	if !allowedSource(conn.RemoteAddr()) {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(connectTimeout))
	hello, err := readClientHello(conn)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	active := readActive(cfg)
	target := net.JoinHostPort("127.0.0.1", active.IngressPort)
	if name := parseSNI(hello); name != "" && !matchesDomain(name, active.IngressDomains) {
		target = net.JoinHostPort(name, "443")
	}
	upstream, err := net.DialTimeout("tcp", target, connectTimeout)
	if err != nil {
		return
	}
	if _, err := upstream.Write(hello); err != nil {
		upstream.Close()
		return
	}
	splice(conn, upstream)
}

// handleEgressPortConn serves an additional egress gateway port: the TLS
// ClientHello's SNI names the real host, which is dialed on the same port
// the client connected to. Connections without an SNI cannot be routed.
func handleEgressPortConn(conn net.Conn, port string) {
	defer conn.Close()
	if !allowedSource(conn.RemoteAddr()) {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(connectTimeout))
	hello, err := readClientHello(conn)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	name := parseSNI(hello)
	if name == "" {
		return
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(name, port), connectTimeout)
	if err != nil {
		return
	}
	if _, err := upstream.Write(hello); err != nil {
		upstream.Close()
		return
	}
	splice(conn, upstream)
}

// handleForwardConn serves a static egress forward: every connection is
// spliced to the fixed target through the host network — no TLS parsing,
// so non-TLS protocols work (e.g. HTTP CONNECT to a corporate proxy).
func handleForwardConn(conn net.Conn, target string) {
	defer conn.Close()
	if !allowedSource(conn.RemoteAddr()) {
		return
	}
	upstream, err := net.DialTimeout("tcp", target, connectTimeout)
	if err != nil {
		return
	}
	splice(conn, upstream)
}

// --- daemon lifecycle ---

func serve(addr string, handler func(net.Conn)) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	logger.Info("listening on " + addr)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go handler(conn)
	}
}

// RunDaemons runs both listeners in the foreground (the hidden `daemons`
// subcommand, spawned detached by create/start).
// daemonsVersionFile records which k3c build the running daemons belong
// to, so a newer binary respawns them instead of leaving stale daemons.
func daemonsVersionFile(cfg *config.Config) string {
	return filepath.Join(cfg.BaseDir, "daemons.version")
}

// daemonsVersion identifies the running daemons: the k3c build plus the
// spawn-time listener config (ports and forwards), so changing either
// respawns them.
func daemonsVersion(cfg *config.Config) string {
	v := version.Get()
	return fmt.Sprintf("%s %s %s ports=%v forwards=%v pullcache=%v:%s",
		v.Version, v.GitCommit, v.BuildDate, cfg.EgressPorts, cfg.EgressForwards,
		cfg.PullCacheEnabled, cfg.PullCachePort)
}

func RunDaemons(cfg *config.Config) error {
	_ = os.WriteFile(daemonsVersionFile(cfg), []byte(daemonsVersion(cfg)+"\n"), 0o644)
	startAutoReclaim(cfg)
	errCh := make(chan error, 3)
	go func() { errCh <- serve("0.0.0.0:"+cfg.ProxyPort, handleProxyConn) }()
	go func() {
		errCh <- serve("0.0.0.0:443", func(c net.Conn) { handleSNIConn(c, cfg) })
	}()
	for _, p := range cfg.EgressPorts {
		if p == 443 {
			continue
		}
		port := strconv.Itoa(p)
		go func() {
			errCh <- serve("0.0.0.0:"+port, func(c net.Conn) { handleEgressPortConn(c, port) })
		}()
	}
	for _, f := range cfg.EgressForwards {
		fw := f
		go func() {
			errCh <- serve("0.0.0.0:"+fw.Port, func(c net.Conn) { handleForwardConn(c, fw.Target) })
		}()
	}
	if cfg.PullCacheEnabled {
		go func() { errCh <- servePullCache(cfg) }()
		startPullCachePrune(cfg)
	}
	if len(ignoredResources(cfg)) > 0 {
		go func() { errCh <- serveWebhook(cfg) }()
	}
	go func() {
		errCh <- serve("0.0.0.0:"+cfg.RegistryPort, func(c net.Conn) { handleRegistryConn(c, cfg) })
	}()
	return <-errCh
}

// egressPortMissing reports whether a configured egress gateway port or
// forward is not served by the running daemons.
func egressPortMissing(cfg *config.Config) bool {
	for _, p := range cfg.EgressPorts {
		if p != 443 && !portOpen(strconv.Itoa(p)) {
			return true
		}
	}
	for _, f := range cfg.EgressForwards {
		if !portOpen(f.Port) {
			return true
		}
	}
	return false
}

// portOpen reports whether a local TCP port accepts connections.
func portOpen(port string) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func pidAlive(pidFile string) bool {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// DaemonsStatus prints the host daemons' process and listener state.
func DaemonsStatus(cfg *config.Config) error {
	pid := "-"
	if data, err := os.ReadFile(cfg.ProxyPidFile()); err == nil {
		pid = strings.TrimSpace(string(data))
	}
	state := "stopped"
	if pidAlive(cfg.ProxyPidFile()) {
		state = "running"
	}
	fmt.Printf("daemons: %s (pid %s)\n", state, pid)
	if recorded, err := os.ReadFile(daemonsVersionFile(cfg)); err == nil {
		fmt.Printf("spawned: %s\n", strings.TrimSpace(string(recorded)))
	}
	listener := func(name, port, detail string) {
		st := "down"
		if portOpen(port) {
			st = "up"
		}
		fmt.Printf("%-12s :%-6s %-5s %s\n", name, port, st, detail)
	}
	listener("proxy", cfg.ProxyPort, "")
	listener("sni-gateway", "443", "")
	for _, p := range cfg.EgressPorts {
		if p != 443 {
			listener("egress", strconv.Itoa(p), "")
		}
	}
	for _, f := range cfg.EgressForwards {
		listener("forward", f.Port, "-> "+f.Target)
	}
	if len(ignoredResources(cfg)) > 0 {
		listener("webhook", webhookPort, "")
	}
	if cfg.RegistryEnabled {
		listener("registry", cfg.RegistryPort, "")
	}
	if cfg.PullCacheEnabled {
		listener("pull-cache", cfg.PullCachePort, "")
	}
	return nil
}

// RestartDaemons stops the host daemons and spawns them fresh.
func RestartDaemons(cfg *config.Config) error {
	StopDaemons(cfg)
	return SpawnDaemons(cfg)
}

// SpawnDaemons starts this binary's `daemons` subcommand detached, unless
// already running. Both pidfiles point at the same process.
func SpawnDaemons(cfg *config.Config) error {
	if pidAlive(cfg.ProxyPidFile()) {
		recorded, _ := os.ReadFile(daemonsVersionFile(cfg))
		switch {
		case strings.TrimSpace(string(recorded)) != daemonsVersion(cfg):
			logger.Info("restarting host daemons (k3c version or listener config changed)")
			StopDaemons(cfg)
		case len(ignoredResources(cfg)) > 0 && !portOpen(webhookPort):
			logger.Info("restarting host daemons (webhook newly enabled)")
			StopDaemons(cfg)
		case egressPortMissing(cfg):
			logger.Info("restarting host daemons (egress ports changed)")
			StopDaemons(cfg)
		default:
			logger.Info("host daemons already running")
			return nil
		}
	}
	logger.Info("starting host daemons (proxy :" + cfg.ProxyPort + ", sni-gateway :443)")
	if err := os.MkdirAll(cfg.BaseDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(cfg.DaemonLogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"daemons"}
	if cfg.ConfigFile != "" {
		args = append(args, "--config", cfg.ConfigFile)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	for _, f := range []string{cfg.ProxyPidFile(), cfg.SNIPidFile()} {
		if err := os.WriteFile(f, []byte(pid), 0o644); err != nil {
			return err
		}
	}
	_ = cmd.Process.Release()
	for i := 0; i < 20; i++ {
		if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+cfg.ProxyPort, time.Second); err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("daemons did not come up; see %s", cfg.DaemonLogFile())
}

// StopDaemons stops the host daemons and removes their pidfiles.
func StopDaemons(cfg *config.Config) {
	if pidAlive(cfg.ProxyPidFile()) {
		logger.Info("stopping host daemons")
		if data, err := os.ReadFile(cfg.ProxyPidFile()); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				if proc, err := os.FindProcess(pid); err == nil {
					_ = proc.Kill()
				}
			}
		}
	}
	_ = os.Remove(cfg.ProxyPidFile())
	_ = os.Remove(cfg.SNIPidFile())
}
