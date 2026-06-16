package cluster

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k3c/config"
	"k3c/runtime"
)

// Doctor walks the chain of known failure modes — host tools, container
// system, host DNS, daemons, egress, cluster health — and reports what is
// broken with a hint how to fix it. All checks are read-only.
func Doctor(cfg *config.Config) error {
	d := &doctor{}

	d.section("host")
	d.checkKubectl()
	d.checkRuntime()
	d.checkGvnetPlugin(cfg)
	d.checkHostDNS(cfg)
	d.checkCorporateNetwork(cfg)
	d.checkCIDRCollisions(cfg)

	d.section("daemons")
	d.checkDaemons(cfg)
	d.checkEgress(cfg)

	d.section("cluster '" + cfg.Cluster + "'")
	d.checkCluster(cfg)

	fmt.Printf("\n%d ok, %d warning(s), %d problem(s)\n", d.ok, d.warns, d.fails)
	if d.fails > 0 {
		return fmt.Errorf("%d problem(s) found", d.fails)
	}
	return nil
}

type doctor struct {
	ok, warns, fails int
}

func (d *doctor) section(name string) { fmt.Println(name) }

func (d *doctor) pass(msg string) {
	d.ok++
	fmt.Println("  ✓ " + msg)
}

func (d *doctor) warn(msg, hint string) {
	d.warns++
	fmt.Println("  ! " + msg)
	if hint != "" {
		fmt.Println("      → " + hint)
	}
}

func (d *doctor) fail(msg, hint string) {
	d.fails++
	fmt.Println("  ✗ " + msg)
	if hint != "" {
		fmt.Println("      → " + hint)
	}
}

func (d *doctor) checkKubectl() {
	if _, err := exec.LookPath("kubectl"); err != nil {
		d.fail("kubectl not found in PATH", "install kubectl (e.g. brew install kubectl)")
		return
	}
	d.pass("kubectl found")
}

func (d *doctor) checkRuntime() {
	out, err := runContainer("--version")
	if err != nil {
		d.fail("container CLI not working: "+firstLine(out), "")
		return
	}
	d.pass("container runtime: " + firstLine(out))

	if _, err := runContainer("system", "status"); err != nil {
		d.fail("container system not running", "k3c cluster start launches it, or: k3c container system start --enable-kernel-install")
		return
	}
	if out, err := runContainer("image", "ls"); err != nil {
		lower := strings.ToLower(out)
		if strings.Contains(lower, "plugin") && strings.Contains(lower, "not found") {
			d.fail("container system has no plugins (an aborted first start)",
				"k3c container system stop && k3c container system start --enable-kernel-install")
		} else {
			d.fail("container image listing fails: "+firstLine(out), "")
		}
		return
	}
	d.pass("container system running (plugins ok)")
}

// checkGvnetPlugin verifies the transparent-egress network plugin is present.
// A k3c whose bundled runtime predates the plugin fails cluster/sidecar create
// the moment transparent egress builds its gvnet network, with "unable to
// locate network plugin container-network-gvnet". Only relevant when
// egress.transparent is set.
func (d *doctor) checkGvnetPlugin(cfg *config.Config) {
	if !cfg.TransparentEgress {
		return
	}
	// The plugin can be present on disk yet absent from the RUNNING system if
	// the runtime was upgraded without restarting it — diagnose that first.
	if runtime.RuntimeRestartNeeded() {
		d.fail("the running container system is on an outdated runtime; new plugins (e.g. transparent-egress container-network-gvnet) are not registered",
			"k3c container system stop && k3c container system start --enable-kernel-install")
		return
	}
	ok, root := runtime.GvnetPluginInstalled()
	if ok {
		d.pass("transparent-egress plugin present (container-network-gvnet)")
		return
	}
	hint := "upgrade k3c to a build whose bundled runtime includes the plugin"
	if root != "" {
		hint += "; or drop container-network-gvnet into " +
			filepath.Join(root, "libexec/container/plugins") +
			" then: k3c container system stop && k3c container system start"
	}
	d.fail("transparent-egress plugin missing: container-network-gvnet (egress.transparent is set)", hint)
}

// registryHosts extracts the registry endpoint hosts from the k3s
// registries configuration — the names image pulls actually depend on.
func registryHosts(cfg *config.Config) []string {
	re := regexp.MustCompile(`https?://([a-zA-Z0-9.-]+)`)
	seen := map[string]bool{}
	var hosts []string
	for _, line := range strings.Split(cfg.Registries, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(line, -1) {
			host := m[1]
			if seen[host] || strings.HasPrefix(host, "192.168.") || host == "localhost" {
				continue
			}
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	if len(hosts) > 3 {
		hosts = hosts[:3]
	}
	return hosts
}

// checkHostDNS resolves the registry hosts through the SYSTEM resolver
// (dscacheutil), the same path the daemons use — a wedged mDNSResponder
// after a VPN flap breaks exactly this while dig still works.
func (d *doctor) checkHostDNS(cfg *config.Config) {
	hosts := registryHosts(cfg)
	if len(hosts) == 0 {
		d.pass("host DNS: no registry mirrors configured, nothing to resolve")
		return
	}
	for _, host := range hosts {
		out, _ := exec.Command("dscacheutil", "-q", "host", "-a", "name", host).CombinedOutput()
		if !strings.Contains(string(out), "ip_address") {
			d.fail("host DNS cannot resolve "+host,
				"system resolver is wedged (VPN flap?): sudo killall -9 mDNSResponder, or reconnect the VPN")
			return
		}
	}
	d.pass(fmt.Sprintf("host DNS resolves the registry mirrors (%s)", strings.Join(hosts, ", ")))
}

// tcpReachable reports whether host:port accepts a TCP connection.
func tcpReachable(host, port string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// checkCorporateNetwork tests the host's DIRECT reachability of a
// corporate registry host (not via the k3c proxy). Host-side tools — the
// `ic`/gradle build pulling from the corporate Maven repo, plain docker —
// depend on this, and a half-up VPN/Zscaler tunnel leaves public internet
// working while corporate hosts time out, surfacing as opaque build
// stack traces. When the corporate host is unreachable, a public probe
// distinguishes "VPN down" from "no network".
func (d *doctor) checkCorporateNetwork(cfg *config.Config) {
	hosts := registryHosts(cfg)
	if len(hosts) == 0 {
		return
	}
	host := hosts[0]
	if tcpReachable(host, "443", 6*time.Second) {
		d.pass("corporate network reachable (" + host + ":443)")
		return
	}
	if tcpReachable("1.1.1.1", "443", 6*time.Second) {
		d.fail("corporate host "+host+" unreachable, but public internet is up",
			"VPN/Zscaler appears down — reconnect it; host builds (ic/gradle) and image pulls will fail until then")
	} else {
		d.fail("no network connectivity ("+host+" and public both unreachable)", "check your network connection")
	}
}

// checkCIDRCollisions warns when another interface (typically the
// corporate VPN) claims routes overlapping the cluster's pod or service
// CIDR: host traffic to those addresses silently disappears into the
// tunnel, and addresses behind the VPN in that range are shadowed for
// pods.
func (d *doctor) checkCIDRCollisions(cfg *config.Config) {
	out, err := exec.Command("netstat", "-rn", "-f", "inet").Output()
	if err != nil {
		return
	}
	cidrs := map[string]*net.IPNet{}
	for name, c := range map[string]string{"cluster": cfg.ClusterCIDR, "service": cfg.ServiceCIDR} {
		if _, n, err := net.ParseCIDR(c); err == nil {
			cidrs[name] = n
		}
	}
	var collisions []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(fields[len(fields)-1], "utun") {
			continue
		}
		rn := parseRouteDest(fields[0])
		if rn == nil {
			continue
		}
		for name, n := range cidrs {
			if n.Contains(rn.IP) || rn.Contains(n.IP) {
				collisions = append(collisions, name+" CIDR "+n.String()+" vs VPN route "+fields[0])
			}
		}
	}
	if len(collisions) > 0 {
		if len(collisions) > 3 {
			collisions = collisions[:3]
		}
		d.warn("cluster CIDRs overlap VPN routes: "+strings.Join(collisions, "; "),
			"pick free ranges in k3c.yaml (clusterCidr/serviceCidr) and recreate the cluster")
		return
	}
	d.pass("cluster CIDRs do not collide with VPN routes")
}

// parseRouteDest parses a netstat destination like "10.53/16" or
// "10.1.67.130" into a network, or nil for anything else.
func parseRouteDest(dest string) *net.IPNet {
	prefix, bits, hasBits := strings.Cut(dest, "/")
	octets := strings.Split(prefix, ".")
	if len(octets) > 4 || len(octets) == 1 {
		return nil
	}
	for len(octets) < 4 {
		octets = append(octets, "0")
	}
	if !hasBits {
		bits = "32"
	}
	_, n, err := net.ParseCIDR(strings.Join(octets, ".") + "/" + bits)
	if err != nil {
		return nil
	}
	return n
}

func (d *doctor) checkDaemons(cfg *config.Config) {
	if !pidAlive(cfg.ProxyPidFile()) {
		d.fail("host daemons not running", "k3c cluster start (or: k3c daemons restart)")
		return
	}
	recorded, _ := os.ReadFile(daemonsVersionFile(cfg))
	if strings.TrimSpace(string(recorded)) != daemonsVersion(cfg) {
		d.warn("daemons were spawned by another k3c build or config", "k3c daemons restart")
	} else {
		d.pass("daemons running (current build and config)")
	}

	type listener struct{ name, port string }
	listeners := []listener{{"proxy", cfg.ProxyPort}, {"sni-gateway", "443"}}
	for _, p := range cfg.EgressPorts {
		if p != 443 {
			listeners = append(listeners, listener{"egress", strconv.Itoa(p)})
		}
	}
	for _, f := range cfg.EgressForwards {
		listeners = append(listeners, listener{"forward -> " + f.Target, f.Port})
	}
	if len(ignoredResources(cfg)) > 0 {
		listeners = append(listeners, listener{"webhook", webhookPort})
	}
	if cfg.RegistryEnabled {
		listeners = append(listeners, listener{"registry", cfg.RegistryPort})
	}
	if cfg.PullCacheEnabled {
		listeners = append(listeners, listener{"pull-cache", cfg.PullCachePort})
	}
	down := []string{}
	for _, l := range listeners {
		if !portOpen(l.port) {
			down = append(down, l.name+" :"+l.port)
		}
	}
	if len(down) > 0 {
		d.fail("listeners down: "+strings.Join(down, ", "), "k3c daemons restart")
		return
	}
	d.pass(fmt.Sprintf("all %d listeners up", len(listeners)))
}

// checkEgress sends a request to a registry mirror through the host
// proxy — the path image pulls take. Any HTTP status counts as reachable.
func (d *doctor) checkEgress(cfg *config.Config) {
	hosts := registryHosts(cfg)
	if len(hosts) == 0 || !portOpen(cfg.ProxyPort) {
		return
	}
	proxyURL, _ := url.Parse("http://127.0.0.1:" + cfg.ProxyPort)
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
	resp, err := client.Head("https://" + hosts[0] + "/v2/")
	if err != nil {
		d.fail("egress to "+hosts[0]+" via the proxy failed: "+firstLine(err.Error()),
			"check VPN connectivity; pulls will fail with Bad Gateway")
		return
	}
	resp.Body.Close()
	d.pass(fmt.Sprintf("egress via proxy: %s (HTTP %d)", hosts[0], resp.StatusCode))
}

func (d *doctor) checkCluster(cfg *config.Config) {
	if !containerExists(cfg.ServerName, false) {
		d.warn("cluster does not exist", "k3c cluster create")
		return
	}
	if isPaused(cfg) {
		d.pass("cluster is paused (resume with: k3c cluster resume)")
		return
	}
	if !containerExists(cfg.ServerName, true) {
		state := "stopped"
		if _, err := containerStateFilePath(cfg.ServerName, vmstateFile); err == nil {
			state = "suspended"
		}
		d.pass("cluster is " + state + " (start with: k3c cluster start)")
		return
	}

	out, err := kubectl(cfg, "get", "nodes", "--request-timeout=8s")
	if err != nil {
		d.fail("API server not reachable via context "+cfg.KubeContext+": "+firstLine(out),
			"k3s may still be starting; check: k3c container logs "+cfg.ServerName)
		return
	}
	if !strings.Contains(out, " Ready") {
		d.fail("node is not Ready", "kubectl --context "+cfg.KubeContext+" describe nodes")
	} else {
		d.pass("server running, node Ready")
	}

	if _, err := runContainer("exec", cfg.ServerName,
		"sh", "-c", "test -r /etc/rancher/k3s/registries.yaml"); err != nil {
		d.fail("virtiofs shares are dead (image pulls cannot read the registry CA)",
			"k3c cluster start repairs them")
	} else {
		d.pass("virtiofs shares healthy")
	}

	if out, err := runContainer("exec", cfg.ServerName, "date", "+%s"); err == nil {
		if guest, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			if skew := time.Now().Unix() - guest; skew > 10 || skew < -10 {
				d.fail(fmt.Sprintf("guest clock is %ds off", skew),
					"TLS and token validation break; a suspend+start cycle resyncs it")
			} else {
				d.pass("guest clock in sync")
			}
		}
	}

	d.checkWebhook(cfg)

	if cfg.CorednsCustom() != "" {
		if _, err := kubectl(cfg, "-n", "kube-system", "get", "configmap", "coredns-custom"); err != nil {
			d.fail("CoreDNS egress override missing (pods cannot reach the egress domains)",
				"k3c cluster start re-applies it")
		} else {
			d.pass("CoreDNS egress override applied")
		}
	}

	if total, used, available, err := guestMemMB(cfg); err == nil {
		fmt.Printf("  i guest memory: %dM used / %dM (%dM available), host footprint %dM\n",
			used, total, available, footprintMB(cfg.Cluster))
	}
}

// checkWebhook posts a synthetic admission review to the webhook and
// verifies it returns a patch — the silent failure mode is a webhook that
// is registered but not mutating (failurePolicy is Ignore).
func (d *doctor) checkWebhook(cfg *config.Config) {
	resources := ignoredResources(cfg)
	if len(resources) == 0 {
		return
	}
	if _, err := kubectl(cfg, "get", "mutatingwebhookconfiguration", "k3c-ignore-cpu-requests"); err != nil {
		d.fail("ignore-requests webhook is not registered (pods keep their full requests)",
			"k3c cluster start registers it; already-Pending pods need a delete to be re-admitted")
		return
	}
	review := map[string]any{
		"apiVersion": "admission.k8s.io/v1",
		"kind":       "AdmissionReview",
		"request": map[string]any{
			"uid": "doctor",
			"object": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "probe",
						"resources": map[string]any{
							"requests": map[string]any{"cpu": "500m", "memory": "512Mi"},
						},
					}},
				},
			},
		},
	}
	body, _ := json.Marshal(review)
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	resp, err := client.Post("https://"+cfg.VmnetGateway+":"+webhookPort+"/mutate-pods",
		"application/json", bytes.NewReader(body))
	if err != nil {
		d.fail("webhook endpoint not reachable: "+firstLine(err.Error()), "k3c daemons restart")
		return
	}
	defer resp.Body.Close()
	var reviewResp struct {
		Response struct {
			Patch string `json:"patch"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reviewResp); err != nil || reviewResp.Response.Patch == "" {
		d.fail("webhook responds but does not mutate", "k3c daemons restart")
		return
	}
	if _, err := base64.StdEncoding.DecodeString(reviewResp.Response.Patch); err != nil {
		d.fail("webhook returned an invalid patch", "k3c daemons restart")
		return
	}
	d.pass("ignore-requests webhook mutating (" + strings.Join(resources, ", ") + ")")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// The in-cluster debug pod: netshoot ships curl, dig, nc, tcpdump, and
// friends for testing DNS, egress, and service routing from a pod's
// perspective. There is no official image with a comparable toolset;
// the image is overridable for environments that require a blessed one.
const debugPodName = "k3c-debug"

// netshoot v0.14, pinned by digest for reproducibility (--image overrides)
const defaultDebugImage = "docker.io/nicolaka/netshoot@sha256:47b907d662d139d1e2f22bfe14f4efca1e3f1feed283572f47c970c780c03b61"

func debugPodManifest(image string) string {
	return `apiVersion: v1
kind: Pod
metadata:
  name: ` + debugPodName + `
  labels:
    app: k3c-debug
spec:
  containers:
    - name: debug
      image: "` + image + `"
      command: ["sleep", "infinity"]
  terminationGracePeriodSeconds: 1
`
}

// DoctorShellRemove deletes the debug pod.
func DoctorShellRemove(cfg *config.Config) error {
	out, err := kubectl(cfg, "delete", "pod", debugPodName, "--ignore-not-found", "--wait=false")
	if err != nil {
		return fmt.Errorf("removing debug pod: %s", out)
	}
	fmt.Println("debug pod removed")
	return nil
}

// DoctorShell starts (or reuses) a debug pod in the cluster and opens an
// interactive shell in it. With remove the pod is deleted when the shell
// exits, otherwise it keeps running for the next invocation.
func DoctorShell(cfg *config.Config, image string, remove bool) error {
	if image == "" {
		image = defaultDebugImage
	}
	// an existing pod with another image cannot be reused
	if current, err := kubectl(cfg, "get", "pod", debugPodName, "--request-timeout=8s",
		"-o", "jsonpath={.spec.containers[0].image}"); err == nil && current != image {
		fmt.Println("replacing debug pod (image " + current + " -> " + image + ")")
		if out, err := kubectl(cfg, "delete", "pod", debugPodName, "--wait=true"); err != nil {
			return fmt.Errorf("removing old debug pod: %s", out)
		}
	}
	if _, err := kubectl(cfg, "get", "pod", debugPodName, "--request-timeout=8s"); err != nil {
		fmt.Println("creating debug pod " + debugPodName + " (" + image + ")")
		apply := kubectlCommand(cfg, "apply", "-f", "-")
		apply.Stdin = strings.NewReader(debugPodManifest(image))
		if out, err := apply.CombinedOutput(); err != nil {
			return fmt.Errorf("creating debug pod: %s", strings.TrimSpace(string(out)))
		}
	}
	if out, err := kubectl(cfg, "wait", "--for=condition=Ready",
		"pod/"+debugPodName, "--timeout=120s"); err != nil {
		return fmt.Errorf("debug pod did not become ready: %s", out)
	}
	if remove {
		fmt.Println("connecting (the pod is removed when the shell exits)")
	} else {
		fmt.Println("connecting (the pod keeps running; remove with: k3c doctor --rm)")
	}
	shell := kubectlCommand(cfg, "exec", "-it", debugPodName, "--", "bash")
	shell.Stdin = os.Stdin
	shell.Stdout = os.Stdout
	shell.Stderr = os.Stderr
	err := shell.Run()
	if remove {
		if rmErr := DoctorShellRemove(cfg); err == nil {
			err = rmErr
		}
	}
	return err
}
