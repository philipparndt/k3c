package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Kube identifies a cluster's kubeconfig + context. Context "" means use the
// kubeconfig's current-context.
type Kube struct {
	Path    string
	Context string
}

func (k Kube) args(rest ...string) []string {
	a := []string{"--kubeconfig", k.Path}
	if k.Context != "" {
		a = append(a, "--context", k.Context)
	}
	return append(a, rest...)
}

// kc runs kubectl against the cluster, quietly (used in poll loops).
func kc(ctx context.Context, k Kube, args ...string) (string, error) {
	return runQ(ctx, "kubectl", k.args(args...)...)
}

// waitAddons blocks until each named deployment in kube-system is rolled out.
func waitAddons(ctx context.Context, k Kube, addons []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		all := true
		for _, d := range addons {
			if _, err := kc(ctx, k, "-n", "kube-system", "rollout", "status", "deploy/"+d, "--timeout=3s"); err != nil {
				all = false
				break
			}
		}
		if all {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("addons %v not ready within %s", addons, timeout)
		}
		time.Sleep(time.Second)
	}
}

// waitPodsReady blocks until at least want pods in ns are Ready.
func waitPodsReady(ctx context.Context, k Kube, ns string, want int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		out, _ := kc(ctx, k, "-n", ns, "get", "pods", "--no-headers")
		ready := 0
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			rt := strings.SplitN(f[1], "/", 2)
			if len(rt) == 2 && rt[0] == rt[1] && rt[1] != "0" {
				ready++
			}
		}
		if ready >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("only %d/%d pods Ready in %s within %s", ready, want, ns, timeout)
		}
		time.Sleep(time.Second)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
