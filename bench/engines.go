package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

// ---------------------------------------------------------------- k3c --------

type k3cEngine struct {
	cluster  string
	config   string // optional --config (e.g. to enable the pull cache)
	kubePath string
}

func (e *k3cEngine) Name() string     { return "k3c" }
func (e *k3cEngine) Addons() []string { return []string{"coredns", "local-path-provisioner"} }

// k3c runs each cluster as a generic Apple Virtualization.framework helper
// (com.apple.Virtualization.VirtualMachine), reparented to launchd with no k3c
// marker in its name — so a substring match misses it entirely (the bug that
// made k3c read ~0 EI). Identify THIS cluster's VM by its open container disk
// files via lsof (sudo-free), cached per pid. Other k3c clusters' VMs (e.g. a
// persistent k3c-server) are excluded by the cluster-specific path.
func (e *k3cEngine) EnergyMatcher() Matcher {
	const vmProc = "com.apple.Virtualization.VirtualMachine"
	needle := "containers/" + e.cluster + "-" // e.g. containers/bench-
	cache := map[int]bool{}
	return func(pid int, cmd string) bool {
		if !strings.Contains(cmd, vmProc) {
			return false
		}
		if v, ok := cache[pid]; ok {
			return v
		}
		v := pidHasOpenPath(pid, needle)
		cache[pid] = v
		return v
	}
}
func (e *k3cEngine) DockerContext() string { return "k3c" }
func (e *k3cEngine) DockerUp(ctx context.Context) error {
	_, err := e.k3c(ctx, "docker", "up")
	return err
}

func (e *k3cEngine) base() []string {
	if e.config != "" {
		return []string{"--config", e.config}
	}
	return nil
}
func (e *k3cEngine) k3c(ctx context.Context, args ...string) (string, error) {
	return run(ctx, "k3c", append(e.base(), args...)...)
}
func (e *k3cEngine) del(ctx context.Context) {
	_, _ = runQ(ctx, "k3c", append(e.base(), "cluster", "delete", e.cluster)...)
}

func (e *k3cEngine) ColdPrep(ctx context.Context) error {
	e.del(ctx)
	_, _ = runQ(ctx, "k3c", append(e.base(), "pull-cache", "clear")...)
	return nil
}
func (e *k3cEngine) WarmPrep(ctx context.Context) error {
	e.del(ctx)
	if _, err := e.k3c(ctx, "cluster", "create", e.cluster); err != nil {
		return fmt.Errorf("warm prep create: %w", err)
	}
	e.del(ctx)
	return nil
}
func (e *k3cEngine) writeKubeconfig(ctx context.Context) error {
	out, err := runOut(ctx, nil, "k3c", append(e.base(), "kubeconfig", "get", e.cluster)...)
	if err != nil {
		return fmt.Errorf("kubeconfig get: %w", err)
	}
	return os.WriteFile(e.kubePath, []byte(out), 0o600)
}
func (e *k3cEngine) Create(ctx context.Context) (Kube, error) {
	e.del(ctx)
	if _, err := e.k3c(ctx, "cluster", "create", e.cluster); err != nil {
		return Kube{}, fmt.Errorf("k3c cluster create: %w", err)
	}
	f, err := os.CreateTemp("", "bench-kubeconfig-*")
	if err != nil {
		return Kube{}, err
	}
	e.kubePath = f.Name()
	f.Close()
	if err := e.writeKubeconfig(ctx); err != nil {
		return Kube{}, err
	}
	return Kube{Path: e.kubePath}, nil
}
func (e *k3cEngine) Destroy(ctx context.Context) error {
	e.del(ctx)
	if e.kubePath != "" {
		os.Remove(e.kubePath)
	}
	return nil
}
func (e *k3cEngine) Suspend(ctx context.Context) error {
	_, err := e.k3c(ctx, "cluster", "suspend", e.cluster)
	return err
}
func (e *k3cEngine) Resume(ctx context.Context) error {
	if _, err := e.k3c(ctx, "cluster", "start", e.cluster); err != nil {
		return err
	}
	return e.writeKubeconfig(ctx) // API port may have changed
}
func (e *k3cEngine) StopAll(ctx context.Context) error {
	e.del(ctx)
	_, _ = runQ(ctx, "k3c", "daemons", "stop")
	return nil
}

// ---------------------------------------------------------------- orb --------

type orbEngine struct{}

func (e *orbEngine) Name() string                       { return "orbstack" }
func (e *orbEngine) Addons() []string                   { return []string{"coredns", "local-path-provisioner"} }
func (e *orbEngine) EnergyMatcher() Matcher             { return substringMatcher("OrbStack") }
func (e *orbEngine) DockerContext() string              { return "orbstack" }
func (e *orbEngine) DockerUp(ctx context.Context) error { return e.startVM(ctx) }

func (e *orbEngine) orb(ctx context.Context, args ...string) (string, error) {
	c, cancel := withTimeout(ctx, 240*time.Second)
	defer cancel()
	return run(c, "orb", args...)
}
func (e *orbEngine) status() string {
	out, _ := runQ(context.Background(), "orb", "status")
	return strings.TrimSpace(out)
}
func (e *orbEngine) startVM(ctx context.Context) error {
	for i := 0; i < 3 && e.status() != "Running"; i++ {
		_, _ = e.orb(ctx, "start")
		time.Sleep(2 * time.Second)
	}
	if e.status() != "Running" {
		return fmt.Errorf("orb VM did not start")
	}
	return nil
}
func (e *orbEngine) ColdPrep(ctx context.Context) error {
	_, _ = e.orb(ctx, "stop")
	time.Sleep(2 * time.Second)
	return nil
}
func (e *orbEngine) WarmPrep(ctx context.Context) error {
	_ = e.startVM(ctx)
	_, _ = e.orb(ctx, "start", "k8s")
	_, _ = e.orb(ctx, "stop", "k8s")
	return nil
}
func (e *orbEngine) Create(ctx context.Context) (Kube, error) {
	if err := e.startVM(ctx); err != nil {
		return Kube{}, err
	}
	if _, err := e.orb(ctx, "start", "k8s"); err != nil {
		return Kube{}, fmt.Errorf("orb start k8s: %w", err)
	}
	return Kube{Path: homeDir() + "/.orbstack/k8s/config.yml", Context: "orbstack"}, nil
}
func (e *orbEngine) Destroy(ctx context.Context) error { _, _ = e.orb(ctx, "stop", "k8s"); return nil }
func (e *orbEngine) Suspend(ctx context.Context) error {
	_, err := e.orb(ctx, "stop", "k8s")
	return err
}
func (e *orbEngine) Resume(ctx context.Context) error {
	_, err := e.orb(ctx, "start", "k8s")
	return err
}
func (e *orbEngine) StopAll(ctx context.Context) error { _, _ = e.orb(ctx, "stop"); return nil }

// ----------------------------------------------------- Rancher Desktop -------

type rdEngine struct{}

func (e *rdEngine) Name() string     { return "rancher" }
func (e *rdEngine) Addons() []string { return []string{"coredns", "local-path-provisioner"} }
func (e *rdEngine) EnergyMatcher() Matcher {
	return substringMatcher("Rancher Desktop", "rancher-desktop", "lima", "qemu-system")
}
func (e *rdEngine) DockerContext() string              { return "rancher-desktop" }
func (e *rdEngine) DockerUp(ctx context.Context) error { _, err := e.rd(ctx, "start"); return err }

func rdctlPath() string {
	if p := os.Getenv("RDCTL"); p != "" {
		return p
	}
	return "/Applications/Rancher Desktop.app/Contents/Resources/resources/darwin/bin/rdctl"
}
func (e *rdEngine) rd(ctx context.Context, args ...string) (string, error) {
	c, cancel := withTimeout(ctx, 600*time.Second)
	defer cancel()
	return run(c, rdctlPath(), args...)
}
func (e *rdEngine) ColdPrep(ctx context.Context) error {
	_, _ = e.rd(ctx, "shutdown")
	time.Sleep(2 * time.Second)
	return nil
}
func (e *rdEngine) WarmPrep(ctx context.Context) error { _, _ = e.rd(ctx, "start"); return nil }
func (e *rdEngine) Create(ctx context.Context) (Kube, error) {
	if _, err := e.rd(ctx, "start", "--kubernetes.enabled=true"); err != nil {
		return Kube{}, fmt.Errorf("rdctl start: %w", err)
	}
	return Kube{Path: homeDir() + "/.kube/config", Context: "rancher-desktop"}, nil
}
func (e *rdEngine) Destroy(ctx context.Context) error { _, _ = e.rd(ctx, "shutdown"); return nil }
func (e *rdEngine) Suspend(ctx context.Context) error { _, err := e.rd(ctx, "shutdown"); return err }
func (e *rdEngine) Resume(ctx context.Context) error  { _, err := e.rd(ctx, "start"); return err }
func (e *rdEngine) StopAll(ctx context.Context) error { _, _ = e.rd(ctx, "shutdown"); return nil }

// -------------------------------------------------------------- colima -------

type colimaEngine struct{}

func (e *colimaEngine) Name() string     { return "colima" }
func (e *colimaEngine) Addons() []string { return []string{"coredns", "local-path-provisioner"} }
func (e *colimaEngine) EnergyMatcher() Matcher {
	return substringMatcher("lima-colima", "colima", "limactl")
}
func (e *colimaEngine) DockerContext() string { return "colima" }

func (e *colimaEngine) colima(ctx context.Context, args ...string) (string, error) {
	c, cancel := withTimeout(ctx, 600*time.Second)
	defer cancel()
	return run(c, "colima", args...)
}
func (e *colimaEngine) DockerUp(ctx context.Context) error {
	_, err := e.colima(ctx, "start")
	return err
}
func (e *colimaEngine) ColdPrep(ctx context.Context) error {
	_, _ = e.colima(ctx, "stop")
	time.Sleep(2 * time.Second)
	return nil
}
func (e *colimaEngine) WarmPrep(ctx context.Context) error {
	_, _ = e.colima(ctx, "start", "--kubernetes")
	_, _ = e.colima(ctx, "stop")
	return nil
}
func (e *colimaEngine) Create(ctx context.Context) (Kube, error) {
	if _, err := e.colima(ctx, "start", "--kubernetes"); err != nil {
		return Kube{}, fmt.Errorf("colima start: %w", err)
	}
	return Kube{Path: homeDir() + "/.kube/config", Context: "colima"}, nil
}
func (e *colimaEngine) Destroy(ctx context.Context) error { _, _ = e.colima(ctx, "stop"); return nil }
func (e *colimaEngine) Suspend(ctx context.Context) error {
	_, err := e.colima(ctx, "stop")
	return err
}
func (e *colimaEngine) Resume(ctx context.Context) error {
	_, err := e.colima(ctx, "start", "--kubernetes")
	return err
}
func (e *colimaEngine) StopAll(ctx context.Context) error { _, _ = e.colima(ctx, "stop"); return nil }

// ---------------------------------------------------------------- k3d --------
// k3d runs k3s in containers on a docker provider's engine; it is not a runtime
// itself, so each variant is "<provider>-k3d" (orb-k3d / rancher-k3d /
// colima-k3d). Its host energy is the backend VM's (not separable).

type k3dEngine struct {
	cluster  string
	label    string // orb-k3d / rancher-k3d / colima-k3d
	backend  Engine // orb / rd / colima — provides docker, energy, stop
	kubePath string
}

func (e *k3dEngine) Name() string                       { return e.label }
func (e *k3dEngine) Addons() []string                   { return []string{"coredns", "local-path-provisioner"} }
func (e *k3dEngine) EnergyMatcher() Matcher             { return e.backend.EnergyMatcher() }
func (e *k3dEngine) DockerContext() string              { return "" } // uses the backend's docker, not its own
func (e *k3dEngine) DockerUp(ctx context.Context) error { return e.backend.DockerUp(ctx) }

func dockerSocket(ctx context.Context, name string) string {
	if name == "" {
		return ""
	}
	out, err := runOut(ctx, nil, "docker", "context", "inspect", name, "-f", "{{.Endpoints.docker.Host}}")
	if s := strings.TrimSpace(out); err == nil && s != "" {
		return s
	}
	return ""
}
func (e *k3dEngine) dockerEnv(ctx context.Context) []string {
	if s := dockerSocket(ctx, e.backend.DockerContext()); s != "" {
		return []string{"DOCKER_HOST=" + s}
	}
	return nil
}
func (e *k3dEngine) k3d(ctx context.Context, args ...string) (string, error) {
	c, cancel := withTimeout(ctx, 300*time.Second)
	defer cancel()
	return runEnv(c, e.dockerEnv(ctx), "k3d", args...)
}
func (e *k3dEngine) del(ctx context.Context) {
	_, _ = runEnv(ctx, e.dockerEnv(ctx), "k3d", "cluster", "delete", e.cluster)
}
func (e *k3dEngine) ColdPrep(ctx context.Context) error {
	_ = e.backend.DockerUp(ctx)
	e.del(ctx)
	return nil
}
func (e *k3dEngine) WarmPrep(ctx context.Context) error {
	_ = e.backend.DockerUp(ctx)
	e.del(ctx)
	return nil
}
func (e *k3dEngine) Create(ctx context.Context) (Kube, error) {
	if err := e.backend.DockerUp(ctx); err != nil {
		return Kube{}, fmt.Errorf("%s docker up: %w", e.label, err)
	}
	e.del(ctx)
	if _, err := e.k3d(ctx, "cluster", "create", e.cluster, "--wait", "--timeout", durSecs(gReadyTimeout)); err != nil {
		return Kube{}, fmt.Errorf("k3d cluster create: %w", err)
	}
	f, err := os.CreateTemp("", "bench-kubeconfig-*")
	if err != nil {
		return Kube{}, err
	}
	e.kubePath = f.Name()
	f.Close()
	out, err := runOut(ctx, e.dockerEnv(ctx), "k3d", "kubeconfig", "get", e.cluster)
	if err != nil {
		return Kube{}, fmt.Errorf("k3d kubeconfig get: %w", err)
	}
	if err := os.WriteFile(e.kubePath, []byte(out), 0o600); err != nil {
		return Kube{}, err
	}
	return Kube{Path: e.kubePath}, nil
}
func (e *k3dEngine) Destroy(ctx context.Context) error {
	e.del(ctx)
	if e.kubePath != "" {
		os.Remove(e.kubePath)
	}
	return nil
}
func (e *k3dEngine) Suspend(ctx context.Context) error {
	_, err := e.k3d(ctx, "cluster", "stop", e.cluster)
	return err
}
func (e *k3dEngine) Resume(ctx context.Context) error {
	_, err := e.k3d(ctx, "cluster", "start", e.cluster)
	return err
}
func (e *k3dEngine) StopAll(ctx context.Context) error { e.del(ctx); return e.backend.StopAll(ctx) }

func durSecs(d time.Duration) string { return fmt.Sprintf("%ds", int(d.Seconds())) }
