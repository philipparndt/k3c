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

func (e *orbEngine) Name() string     { return "orbstack" }
func (e *orbEngine) Addons() []string { return []string{"coredns", "local-path-provisioner"} }

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

// ---------------------------------------------------------------- k3d --------

type k3dEngine struct {
	cluster  string
	kubePath string
}

func (e *k3dEngine) Name() string     { return "k3d" }
func (e *k3dEngine) Addons() []string { return []string{"coredns", "local-path-provisioner"} }

func orbSocket(ctx context.Context) string {
	out, err := runOut(ctx, nil, "docker", "context", "inspect", "orbstack", "-f", "{{.Endpoints.docker.Host}}")
	if s := strings.TrimSpace(out); err == nil && s != "" {
		return s
	}
	return "unix://" + homeDir() + "/.orbstack/run/docker.sock"
}
func (e *k3dEngine) dockerEnv(ctx context.Context) []string {
	return []string{"DOCKER_HOST=" + orbSocket(ctx)}
}
func (e *k3dEngine) orbUp(ctx context.Context) {
	if strings.TrimSpace(mustOut(runQ(context.Background(), "orb", "status"))) != "Running" {
		_, _ = run(ctx, "orb", "start")
	}
}
func (e *k3dEngine) k3d(ctx context.Context, args ...string) (string, error) {
	c, cancel := withTimeout(ctx, 300*time.Second)
	defer cancel()
	return runEnv(c, e.dockerEnv(ctx), "k3d", args...)
}
func (e *k3dEngine) del(ctx context.Context) {
	_, _ = runEnv(ctx, e.dockerEnv(ctx), "k3d", "cluster", "delete", e.cluster)
}
func (e *k3dEngine) ColdPrep(ctx context.Context) error { e.orbUp(ctx); e.del(ctx); return nil }
func (e *k3dEngine) WarmPrep(ctx context.Context) error { e.orbUp(ctx); e.del(ctx); return nil }
func (e *k3dEngine) Create(ctx context.Context) (Kube, error) {
	e.orbUp(ctx)
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
func (e *k3dEngine) StopAll(ctx context.Context) error {
	e.del(ctx)
	_, _ = run(ctx, "orb", "stop")
	return nil
}

func mustOut(s string, _ error) string { return s }
func durSecs(d time.Duration) string   { return fmt.Sprintf("%ds", int(d.Seconds())) }
