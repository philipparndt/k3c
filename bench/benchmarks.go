package main

import (
	"context"
	"fmt"
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

// timed runs fn, returning its wall time in ms and (optionally) the average CPU
// power sampled across it.
func (env *Env) timed(samplePower bool, fn func() error) (ms, mw float64, hasMW bool, err error) {
	var ps *powerSampler
	if samplePower && env.Power {
		ps = startPower()
	}
	t0 := time.Now()
	err = fn()
	ms = float64(time.Since(t0).Milliseconds())
	if ps != nil {
		mw, hasMW = ps.stop()
	}
	return
}

// windowPower samples CPU power for a fixed window (steady-state workloads).
func (env *Env) windowPower(d time.Duration) (float64, bool) {
	if !env.Power {
		return 0, false
	}
	ps := startPower()
	if ps == nil {
		return 0, false
	}
	logf("sampling power for %s (steady state)…", d)
	time.Sleep(d)
	return ps.stop()
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
		ms, mw, hasMW, err := env.timed(true, func() error {
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
		if hasMW {
			emit(v, "cpu_power", mw, "mW")
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
	ms, mw, hasMW, err := env.timed(true, func() error {
		if err := e.Resume(ctx); err != nil {
			return err
		}
		return waitAddons(ctx, k, e.Addons(), env.ReadyTimeout)
	})
	if err == nil {
		emit("restore", "resume_time", ms, "ms")
		if hasMW {
			emit("restore", "cpu_power", mw, "mW")
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

var pullImages = []string{"nginx:1.27", "redis:7.4", "postgres:16", "node:22-bookworm", "python:3.12"}

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
		ms, mw, hasMW, err := env.timed(true, func() error {
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
			if hasMW {
				emit(v, "cpu_power", mw, "mW")
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
	ms, _, _, err := env.timed(false, func() error {
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
	if mw, ok := env.windowPower(env.PowerWindow); ok {
		emit("steady", "cpu_power", mw, "mW")
	}
	_, _ = run(ctx, "helm", helmArgs(k, "uninstall", "grafana", "-n", "grafana")...)
	_, _ = run(ctx, "helm", helmArgs(k, "uninstall", "traefik", "-n", "traefik")...)
	_ = e.Destroy(ctx)
	return nil
}
