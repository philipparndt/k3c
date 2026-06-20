package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// verbose logs every lifecycle command so a run is fully auditable (the whole
// point of a comparison benchmark: you can see exactly what hit each engine).
var verbose = true

// log lines are stored plain (no ANSI) and routed to the UI, which scrolls them
// in a window and tints by the leading marker. Without a UI they print plainly.
func emitLine(s string) {
	if ui != nil {
		ui.addLog(s)
		return
	}
	fmt.Fprintln(os.Stderr, s)
}
func logf(format string, a ...any)  { emitLine("· " + fmt.Sprintf(format, a...)) }
func okf(format string, a ...any)   { emitLine("✓ " + fmt.Sprintf(format, a...)) }
func warnf(format string, a ...any) { emitLine("! " + fmt.Sprintf(format, a...)) }

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

type execOpts struct {
	log        bool     // log the command line
	env        []string // extra environment (appended to os.Environ)
	stdin      string   // stdin to feed
	stdoutOnly bool     // capture stdout only (stderr logged on error) — for outputs we parse
	dir        string   // working directory
}

func exec1(ctx context.Context, o execOpts, name string, args ...string) (string, error) {
	if o.log {
		logf("exec: %s %s", name, strings.Join(args, " "))
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if o.env != nil {
		cmd.Env = append(os.Environ(), o.env...)
	}
	if o.dir != "" {
		cmd.Dir = o.dir
	}
	if o.stdin != "" {
		cmd.Stdin = strings.NewReader(o.stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	if o.stdoutOnly {
		cmd.Stderr = &errb
	} else {
		cmd.Stderr = &out
	}
	err := cmd.Run()
	if err != nil && o.stdoutOnly && errb.Len() > 0 {
		logf("stderr: %s", tail(errb.String(), 3))
	}
	return out.String(), err
}

// run executes a command, logging it; combined output is returned.
func run(ctx context.Context, name string, args ...string) (string, error) {
	return exec1(ctx, execOpts{log: true}, name, args...)
}

// runQ runs quietly (no log) — for poll loops like kubectl rollout status.
func runQ(ctx context.Context, name string, args ...string) (string, error) {
	return exec1(ctx, execOpts{}, name, args...)
}

// runOut captures stdout only (stderr dropped), for commands whose stdout we
// parse (e.g. `k3c kubeconfig get`, which also logs to stderr).
func runOut(ctx context.Context, env []string, name string, args ...string) (string, error) {
	return exec1(ctx, execOpts{log: true, env: env, stdoutOnly: true}, name, args...)
}

// runEnv runs with extra environment, logged (combined output).
func runEnv(ctx context.Context, env []string, name string, args ...string) (string, error) {
	return exec1(ctx, execOpts{log: true, env: env}, name, args...)
}

// runStdin runs feeding stdin, logged (combined output).
func runStdin(ctx context.Context, stdin, name string, args ...string) (string, error) {
	return exec1(ctx, execOpts{log: true, stdin: stdin}, name, args...)
}

// runDir runs in a working directory with extra env, logged (combined output).
func runDir(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	return exec1(ctx, execOpts{log: true, env: env, dir: dir}, name, args...)
}

func withTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
