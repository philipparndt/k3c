package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// progressUI renders a live bottom region: a headline (current step), one
// progress bar, and a fixed-height scrolling log window — redrawn in place with
// ANSI. When stderr is not a TTY (piped/background), it degrades to plain lines
// so logs still stream into captured output.
type progressUI struct {
	mu       sync.Mutex
	tty      bool
	width    int
	logRows  int
	height   int
	headline string
	total    int
	done     int
	logs     []string
	started  bool
}

var ui *progressUI

func newUI(total int) *progressUI {
	u := &progressUI{total: total, logRows: 12, width: 100}
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		u.tty = true
		u.width = termWidth()
	}
	u.height = 3 + u.logRows // headline + bar + blank + log rows
	return u
}

func termWidth() int {
	out, err := exec.Command("sh", "-c", "tput cols 2>/dev/null").Output()
	if err == nil {
		if n, e := strconv.Atoi(strings.TrimSpace(string(out))); e == nil && n > 10 {
			return n
		}
	}
	return 100
}

func (u *progressUI) setStep(headline string) {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.headline = headline
	u.mu.Unlock()
	u.render()
}

func (u *progressUI) advance() {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.done++
	u.mu.Unlock()
	u.render()
}

func (u *progressUI) addLog(line string) {
	if u == nil {
		return
	}
	if !u.tty {
		fmt.Fprintln(os.Stderr, line)
		return
	}
	u.mu.Lock()
	u.logs = append(u.logs, line)
	if len(u.logs) > 500 {
		u.logs = u.logs[len(u.logs)-500:]
	}
	u.mu.Unlock()
	u.render()
}

func (u *progressUI) bar() string {
	const w = 28
	frac := 0.0
	if u.total > 0 {
		frac = float64(u.done) / float64(u.total)
	}
	fill := int(frac * w)
	if fill > w {
		fill = w
	}
	return fmt.Sprintf("[%s%s] %3.0f%%  %d/%d steps",
		strings.Repeat("█", fill), strings.Repeat("·", w-fill), frac*100, u.done, u.total)
}

// render redraws the region in place. Callers must not hold u.mu.
func (u *progressUI) render() {
	if u == nil || !u.tty {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	var b strings.Builder
	if u.started {
		fmt.Fprintf(&b, "\033[%dA", u.height) // cursor up to region top
	}
	u.started = true
	const clr = "\033[2K" // clear line

	fmt.Fprintf(&b, "%s\033[1m▶ %s\033[0m\r\n", clr, trunc(u.headline, u.width-2))
	fmt.Fprintf(&b, "%s\033[38;5;141m%s\033[0m\r\n", clr, u.bar())
	fmt.Fprintf(&b, "%s\r\n", clr)

	start := 0
	if len(u.logs) > u.logRows {
		start = len(u.logs) - u.logRows
	}
	shown := u.logs[start:]
	for i := 0; i < u.logRows; i++ {
		line := ""
		if i < len(shown) {
			line = shown[i]
		}
		fmt.Fprintf(&b, "%s%s\r\n", clr, colorLog(trunc(line, u.width)))
	}
	fmt.Fprint(os.Stderr, b.String())
}

// finish freezes the region and drops below it so the summary prints cleanly.
func (u *progressUI) finish() {
	if u == nil || !u.tty {
		return
	}
	fmt.Fprint(os.Stderr, "\n")
}

// colorLog tints a stored (plain) log line by its leading marker.
func colorLog(s string) string {
	switch {
	case strings.HasPrefix(s, "✓"):
		return "\033[32m" + s + "\033[0m"
	case strings.HasPrefix(s, "!"):
		return "\033[33m" + s + "\033[0m"
	default:
		return "\033[2m" + s + "\033[0m"
	}
}

func trunc(s string, w int) string {
	if w < 1 {
		w = 1
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return string(r[:1])
	}
	return string(r[:w-1]) + "…"
}
