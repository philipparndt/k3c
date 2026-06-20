package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// Emit records one measurement for the current engine/iteration.
type Emit func(variant, metric string, value float64, unit string)

// Env holds run-wide knobs and the timing/power helpers benchmarks use.
type Env struct {
	Variants     map[string]bool // cold/warm filter; empty = all
	Power        bool
	PowerWindow  time.Duration
	ReadyTimeout time.Duration
}

func (env *Env) want(v string) bool { return len(env.Variants) == 0 || env.Variants[v] }

// timed runs fn, returning its wall time in ms and (optionally) the mean
// per-engine energy impact sampled across it (sudo-free).
func (env *Env) timed(patterns []string, sampleEnergy bool, fn func() error) (ms, ei float64, hasEI bool, err error) {
	var es *energySampler
	if sampleEnergy && env.Power {
		es = startEnergy(patterns)
	}
	t0 := time.Now()
	err = fn()
	ms = float64(time.Since(t0).Milliseconds())
	if es != nil {
		ei, hasEI = es.stop()
	}
	return
}

// windowEnergy samples per-engine energy for a fixed window (steady-state).
func (env *Env) windowEnergy(patterns []string, d time.Duration) (float64, bool) {
	if !env.Power {
		return 0, false
	}
	es := startEnergy(patterns)
	if es == nil {
		return 0, false
	}
	logf("sampling energy for %s (steady state)…", d)
	time.Sleep(d)
	return es.stop()
}

type Benchmark interface {
	Name() string
	Run(ctx context.Context, env *Env, e Engine, emit Emit) error
}

func newBenchmark(name string) (Benchmark, error) {
	switch name {
	case "empty":
		return emptyBench{}, nil
	case "resume":
		return resumeBench{}, nil
	case "pull":
		return pullBench{}, nil
	case "helm":
		return helmBench{}, nil
	}
	return nil, fmt.Errorf("unknown benchmark %q (empty|resume|pull|helm)", name)
}

// ---- empty: time create -> usable addons Ready (cold & warm) ----------------

type emptyBench struct{}

func (emptyBench) Name() string { return "empty" }
func (emptyBench) Run(ctx context.Context, env *Env, e Engine, emit Emit) error {
	for _, v := range []string{"cold", "warm"} {
		if !env.want(v) {
			continue
		}
		logf("[%s] empty (%s): preparing…", e.Name(), v)
		if v == "cold" {
			_ = e.ColdPrep(ctx)
		} else {
			_ = e.WarmPrep(ctx)
		}
		var k Kube
		ms, ei, hasEI, err := env.timed(e.EnergyPatterns(), true, func() error {
			var e2 error
			if k, e2 = e.Create(ctx); e2 != nil {
				return e2
			}
			return waitAddons(ctx, k, e.Addons(), env.ReadyTimeout)
		})
		if err != nil {
			warnf("[%s] empty (%s): %v", e.Name(), v, err)
			_ = e.Destroy(ctx)
			continue
		}
		emit(v, "time_to_ready", ms, "ms")
		if hasEI {
			emit(v, "energy", ei, "EI")
		}
		okf("[%s] empty (%s): %.0f ms", e.Name(), v, ms)
		_ = e.Destroy(ctx)
	}
	return nil
}

// ---- resume: bring up (untimed) -> release -> time restore ------------------

type resumeBench struct{}

func (resumeBench) Name() string { return "resume" }
func (resumeBench) Run(ctx context.Context, env *Env, e Engine, emit Emit) error {
	logf("[%s] resume: bringing cluster up (untimed)…", e.Name())
	_ = e.WarmPrep(ctx)
	k, err := e.Create(ctx)
	if err != nil {
		return err
	}
	if err := waitAddons(ctx, k, e.Addons(), env.ReadyTimeout); err != nil {
		_ = e.Destroy(ctx)
		return err
	}
	logf("[%s] resume: releasing…", e.Name())
	if err := e.Suspend(ctx); err != nil {
		_ = e.Destroy(ctx)
		return err
	}
	ms, ei, hasEI, err := env.timed(e.EnergyPatterns(), true, func() error {
		if err := e.Resume(ctx); err != nil {
			return err
		}
		return waitAddons(ctx, k, e.Addons(), env.ReadyTimeout)
	})
	if err == nil {
		emit("restore", "resume_time", ms, "ms")
		if hasEI {
			emit("restore", "energy", ei, "EI")
		}
		okf("[%s] resume: %.0f ms", e.Name(), ms)
	} else {
		warnf("[%s] resume: %v", e.Name(), err)
	}
	_ = e.Destroy(ctx)
	return err
}

// ---- pull: pull N images into the cluster, time until Running --------------

type pullBench struct{}

func (pullBench) Name() string { return "pull" }

// Default pull images. Docker Hub rate-limits anonymous pulls (~100/6h per IP),
// which repeated cold-pull benchmarking trips — override with BENCH_PULL_IMAGES
// (space-separated), e.g. point at registry.k8s.io / ghcr.io / a mirror, or
// authenticate to Docker Hub, or rely on the pull cache (don't clear it).
var pullImages = func() []string {
	if s := os.Getenv("BENCH_PULL_IMAGES"); strings.TrimSpace(s) != "" {
		return strings.Fields(s)
	}
	return []string{"nginx:1.27", "redis:7.4", "postgres:16", "node:22-bookworm", "python:3.12"}
}()

func pullManifest(ns string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: v1\nkind: Namespace\nmetadata: {name: %s}\n---\n", ns)
	for i, img := range pullImages {
		fmt.Fprintf(&b, `apiVersion: v1
kind: Pod
metadata: {name: pull-%d, namespace: %s, labels: {app: pull}}
spec:
  terminationGracePeriodSeconds: 0
  containers:
  - {name: c, image: %s, imagePullPolicy: Always, command: ["sleep","3600"]}
---
`, i, ns, img)
	}
	return b.String()
}

func (pullBench) Run(ctx context.Context, env *Env, e Engine, emit Emit) error {
	const ns = "bench-pull"
	for _, v := range []string{"cold", "warm"} {
		if !env.want(v) {
			continue
		}
		logf("[%s] pull (%s): %d images", e.Name(), v, len(pullImages))
		if v == "cold" {
			_ = e.ColdPrep(ctx)
		} else {
			_ = e.WarmPrep(ctx)
		}
		k, err := e.Create(ctx)
		if err != nil {
			warnf("[%s] pull (%s): create: %v", e.Name(), v, err)
			continue
		}
		ms, ei, hasEI, err := env.timed(e.EnergyPatterns(), true, func() error {
			// The API can briefly reject applies right after create; retry a few
			// times and surface the real output if it keeps failing.
			var out string
			var aerr error
			for try := 0; try < 5; try++ {
				if out, aerr = runStdin(ctx, pullManifest(ns), "kubectl", k.args("apply", "-f", "-")...); aerr == nil {
					break
				}
				time.Sleep(2 * time.Second)
			}
			if aerr != nil {
				return fmt.Errorf("apply: %s", tail(out, 4))
			}
			return waitPodsReady(ctx, k, ns, len(pullImages), env.ReadyTimeout)
		})
		if err == nil {
			emit(v, "pull_time", ms, "ms")
			if hasEI {
				emit(v, "energy", ei, "EI")
			}
			okf("[%s] pull (%s): %.0f ms", e.Name(), v, ms)
		} else {
			warnf("[%s] pull (%s): %v", e.Name(), v, err)
		}
		_, _ = kc(ctx, k, "delete", "ns", ns, "--wait=false")
		_ = e.Destroy(ctx)
	}
	return nil
}

// ---- helm: Traefik + Grafana install, then steady-state power --------------

type helmBench struct{}

func (helmBench) Name() string { return "helm" }
func helmArgs(k Kube, rest ...string) []string {
	a := []string{"--kubeconfig", k.Path}
	if k.Context != "" {
		a = append(a, "--kube-context", k.Context)
	}
	return append(a, rest...)
}
func (helmBench) Run(ctx context.Context, env *Env, e Engine, emit Emit) error {
	logf("[%s] helm: bringing up cluster…", e.Name())
	_ = e.WarmPrep(ctx)
	k, err := e.Create(ctx)
	if err != nil {
		return err
	}
	_ = waitPodsReady(ctx, k, "kube-system", 1, env.ReadyTimeout)
	_, _ = run(ctx, "helm", "repo", "add", "traefik", "https://traefik.github.io/charts")
	_, _ = run(ctx, "helm", "repo", "add", "grafana", "https://grafana.github.io/helm-charts")
	_, _ = run(ctx, "helm", "repo", "update")

	to := durSecs(env.ReadyTimeout)
	ms, _, _, err := env.timed(nil, false, func() error {
		_, e1 := run(ctx, "helm", helmArgs(k, "upgrade", "--install", "traefik", "traefik/traefik",
			"-n", "traefik", "--create-namespace", "--wait", "--timeout", to)...)
		_, e2 := run(ctx, "helm", helmArgs(k, "upgrade", "--install", "grafana", "grafana/grafana",
			"-n", "grafana", "--create-namespace", "--wait", "--timeout", to)...)
		if e1 != nil {
			return e1
		}
		return e2
	})
	if err != nil {
		warnf("[%s] helm install slow/failed: %v", e.Name(), err)
	} else {
		emit("steady", "install_to_ready", ms, "ms")
		okf("[%s] helm install→ready: %.0f ms", e.Name(), ms)
	}
	if ei, ok := env.windowEnergy(e.EnergyPatterns(), env.PowerWindow); ok {
		emit("steady", "energy", ei, "EI")
	}
	_, _ = run(ctx, "helm", helmArgs(k, "uninstall", "grafana", "-n", "grafana")...)
	_, _ = run(ctx, "helm", helmArgs(k, "uninstall", "traefik", "-n", "traefik")...)
	_ = e.Destroy(ctx)
	return nil
}
