package main

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// powerSampler samples whole-system CPU power (mW) via macOS powermetrics.
// Approximates OrbStack's per-process energy metric; document the difference
// when comparing to their published charts. Requires sudo (primed in main).
type powerSampler struct {
	cmd  *exec.Cmd
	path string
}

func powerAvailable() bool {
	if _, err := exec.LookPath("powermetrics"); err != nil {
		return false
	}
	// sudo must be usable non-interactively (main primes the cache).
	return exec.Command("sudo", "-n", "true").Run() == nil
}

// startPower begins sampling in the background; returns nil if unavailable.
func startPower() *powerSampler {
	if !powerAvailable() {
		return nil
	}
	f, err := os.CreateTemp("", "bench-power-*")
	if err != nil {
		return nil
	}
	name := f.Name()
	f.Close()
	out, err := os.OpenFile(name, os.O_WRONLY, 0o644)
	if err != nil {
		os.Remove(name)
		return nil
	}
	cmd := exec.Command("sudo", "powermetrics", "--samplers", "cpu_power", "-i", "1000", "-n", "100000")
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		out.Close()
		os.Remove(name)
		return nil
	}
	out.Close()
	return &powerSampler{cmd: cmd, path: name}
}

// stop ends sampling and returns the average CPU power in mW.
func (p *powerSampler) stop() (float64, bool) {
	if p == nil {
		return 0, false
	}
	// The actual powermetrics runs as root under sudo; kill it directly.
	_ = exec.Command("sudo", "pkill", "-f", "powermetrics --samplers cpu_power").Run()
	_ = p.cmd.Wait()
	defer os.Remove(p.path)

	f, err := os.Open(p.path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	var sum float64
	var n int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "CPU Power:") {
			continue
		}
		// "CPU Power: 1234 mW"
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			if v, err := strconv.ParseFloat(fields[2], 64); err == nil {
				sum += v
				n++
			}
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}
