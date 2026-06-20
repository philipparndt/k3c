package main

import (
	"bufio"
	"context"
	"strconv"
	"strings"
	"time"
)

// Per-engine energy sampling, sudo-free. macOS exposes per-process "Energy
// Impact" (what Activity Monitor shows) via `top -stats power` with no root.
// We attribute energy to an engine by summing the impact of its host processes
// (matched by command substrings), so the number is independent of unrelated
// machine load — the same idea as OrbStack's per-process measurement.
//
// Unit is "EI" (energy impact), a relative kernel score, not Watts. It is
// directly comparable across engines on the same machine; we don't fake mW.
//
// Caveat: k3d runs inside OrbStack's VM, so its host energy is the OrbStack VM
// process — k3d and orb are not separable at the host level.

type energySampler struct {
	patterns []string
	stopCh   chan struct{}
	doneCh   chan [2]float64
}

// startEnergy begins polling in the background; nil if no patterns.
func startEnergy(patterns []string) *energySampler {
	if len(patterns) == 0 {
		return nil
	}
	s := &energySampler{patterns: patterns, stopCh: make(chan struct{}), doneCh: make(chan [2]float64, 1)}
	go s.loop()
	return s
}

func (s *energySampler) loop() {
	var sum float64
	var n int
	take := func() {
		if v, ok := energyOnce(s.patterns); ok {
			sum += v
			n++
		}
	}
	take()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			s.doneCh <- [2]float64{sum, float64(n)}
			return
		case <-t.C:
			take()
		}
	}
}

// stop ends sampling and returns the mean energy impact of the engine's procs.
func (s *energySampler) stop() (float64, bool) {
	if s == nil {
		return 0, false
	}
	close(s.stopCh)
	r := <-s.doneCh
	if r[1] == 0 {
		return 0, false
	}
	return r[0] / r[1], true
}

// energyOnce reads one power frame and sums the engine's matching processes.
func energyOnce(patterns []string) (float64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// -l 2: the first frame's POWER is 0 (no interval yet); the second is real.
	out, err := runQ(ctx, "top", "-l", "2", "-s", "1", "-stats", "pid,power")
	if err != nil {
		return 0, false
	}
	frame := lastTopFrame(out)
	if len(frame) == 0 {
		return 0, false
	}
	want := classifyPids(ctx, patterns)
	var sum float64
	matched := false
	for pid, pw := range frame {
		if want[pid] {
			sum += pw
			matched = true
		}
	}
	return sum, matched
}

// lastTopFrame parses the final "PID POWER" frame of `top -l 2` output.
func lastTopFrame(out string) map[int]float64 {
	frame := map[int]float64{}
	inFrame := false
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "PID") {
			frame = map[int]float64{} // new frame begins; keep only the last
			inFrame = true
			continue
		}
		if !inFrame {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		pw, err2 := strconv.ParseFloat(f[1], 64)
		if err1 == nil && err2 == nil {
			frame[pid] = pw
		}
	}
	return frame
}

// classifyPids returns the set of pids whose full command matches any pattern.
func classifyPids(ctx context.Context, patterns []string) map[int]bool {
	out, err := runQ(ctx, "ps", "-Ao", "pid=,command=")
	want := map[int]bool{}
	if err != nil {
		return want
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		sp := strings.IndexByte(line, ' ')
		if sp <= 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:sp])
		if err != nil {
			continue
		}
		cmd := line[sp+1:]
		for _, p := range patterns {
			if strings.Contains(cmd, p) {
				want[pid] = true
				break
			}
		}
	}
	return want
}
