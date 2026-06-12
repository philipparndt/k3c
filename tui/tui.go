// Package tui is the interactive terminal UI of k3c (k3c ui): clusters and
// their snapshots side by side, with single-key lifecycle operations.
//
// Operations run k3c itself as a subprocess: the CLI commands keep their
// logging and config resolution, the TUI stays responsive and shows the
// captured output.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"k3c/cluster"
	"k3c/config"
)

type pane int

const (
	paneClusters pane = iota
	paneSnapshots
)

// confirm is a pending yes/no question and the command an answer of yes runs.
type confirm struct {
	prompt string
	cmd    tea.Cmd
}

// nameInput is the open "new snapshot" prompt.
type nameInput struct {
	input   textinput.Model
	cluster string
	cold    bool
}

type model struct {
	cfg *config.Config

	clusters  []cluster.ClusterInfo
	snapshots []cluster.SnapshotInfo
	cCur      int
	sCur      int
	focus     pane

	width  int
	height int

	spin    spinner.Model
	busy    string // running operation, "" when idle
	status  string // last result line
	failed  bool   // last result was an error
	output  string // full output of the last operation
	showOut bool

	confirm *confirm
	input   *nameInput
}

// New builds the TUI model. cfg is only used for state-dir lookups; every
// operation re-resolves its own config in the subprocess.
func New(cfg *config.Config) tea.Model {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(accent)
	return model{cfg: cfg, spin: sp}
}

// Run starts the TUI.
func Run(cfg *config.Config) error {
	_, err := tea.NewProgram(New(cfg), tea.WithAltScreen()).Run()
	return err
}

// --- messages ---

type dataMsg struct {
	clusters  []cluster.ClusterInfo
	snapshots []cluster.SnapshotInfo
}

type opDoneMsg struct {
	desc   string
	output string
	err    error
}

type tickMsg struct{}

// --- commands ---

func (m model) selectedCluster() string {
	if m.cCur < len(m.clusters) {
		return m.clusters[m.cCur].Name
	}
	return ""
}

func (m model) selectedSnapshot() string {
	if m.sCur < len(m.snapshots) {
		return m.snapshots[m.sCur].Name
	}
	return ""
}

func (m model) refresh() tea.Cmd {
	cfg, name := m.cfg, m.selectedCluster()
	return func() tea.Msg {
		clusters := cluster.Clusters(cfg)
		// keep the selection on reloads; fall back to the first cluster
		current := name
		if current == "" && len(clusters) > 0 {
			current = clusters[0].Name
		}
		return dataMsg{clusters: clusters, snapshots: cluster.Snapshots(cfg, current)}
	}
}

func tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// runOp executes k3c itself with args and reports the result.
func runOp(desc string, args ...string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return opDoneMsg{desc: desc, err: err}
		}
		out, err := exec.Command(exe, args...).CombinedOutput()
		return opDoneMsg{desc: desc, output: strings.TrimSpace(string(out)), err: err}
	}
}

// --- update ---

func (m model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), m.spin.Tick, tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case dataMsg:
		m.clusters = msg.clusters
		m.snapshots = msg.snapshots
		if m.cCur >= len(m.clusters) {
			m.cCur = max(0, len(m.clusters)-1)
		}
		if m.sCur >= len(m.snapshots) {
			m.sCur = max(0, len(m.snapshots)-1)
		}
		return m, nil

	case opStartMsg:
		m.busy = msg.desc
		m.status = ""
		m.showOut = false
		return m, tea.Batch(msg.run, m.spin.Tick)

	case opDoneMsg:
		m.busy = ""
		m.output = msg.output
		if msg.err != nil {
			m.failed = true
			m.status = msg.desc + " failed: " + lastLine(msg.output, msg.err)
		} else {
			m.failed = false
			m.status = msg.desc + " ✓"
		}
		return m, m.refresh()

	case tickMsg:
		if m.busy == "" && m.confirm == nil && m.input == nil {
			return m, tea.Batch(m.refresh(), tick())
		}
		return m, tick()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// a pending confirmation eats every key
	if m.confirm != nil {
		c := *m.confirm
		m.confirm = nil
		if msg.String() == "y" || msg.String() == "Y" {
			return m, c.cmd
		}
		m.status = "cancelled"
		m.failed = false
		return m, nil
	}

	// the snapshot-name prompt eats every key
	if m.input != nil {
		switch msg.Type {
		case tea.KeyEscape:
			m.input = nil
			m.status = "cancelled"
			m.failed = false
			return m, nil
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyTab:
			m.input.cold = !m.input.cold
			return m, nil
		case tea.KeyEnter:
			in := *m.input
			m.input = nil
			name := strings.TrimSpace(in.input.Value())
			if name == "" {
				name = in.input.Placeholder
			}
			args := []string{"snapshot", "save", in.cluster, name}
			mode := "warm"
			if in.cold {
				args = append(args, "--cold")
				mode = "cold"
			}
			return m.startOp(fmt.Sprintf("%s snapshot %q of %s", mode, name, in.cluster), args...)
		}
		var cmd tea.Cmd
		in := *m.input
		in.input, cmd = in.input.Update(msg)
		m.input = &in
		return m, cmd
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "tab", "left", "right", "h", "l":
		if m.focus == paneClusters {
			m.focus = paneSnapshots
		} else {
			m.focus = paneClusters
		}
		return m, nil

	case "up", "k":
		return m.move(-1)

	case "down", "j":
		return m.move(1)

	case "g", "f5":
		return m, m.refresh()

	case "o":
		m.showOut = !m.showOut
		return m, nil
	}

	if m.busy != "" { // one operation at a time
		return m, nil
	}
	name := m.selectedCluster()
	if name == "" {
		return m, nil
	}

	switch msg.String() {
	case "enter":
		if m.focus == paneSnapshots {
			snap := m.selectedSnapshot()
			if snap == "" {
				return m, nil
			}
			m.confirm = &confirm{
				prompt: fmt.Sprintf("Restore snapshot %q into %q? The cluster's current state is replaced.", snap, name),
				cmd:    m.opCmd("restore of "+snap+" into "+name, "snapshot", "restore", name, snap),
			}
			return m, nil
		}
		return m.startOp("activate "+name, "cluster", "activate", name)

	case "s":
		return m.startOp("start "+name, "cluster", "start", name)
	case "S":
		return m.startOp("stop "+name, "cluster", "stop", name)
	case "p":
		return m.startOp("pause "+name, "cluster", "pause", name)
	case "r":
		return m.startOp("resume "+name, "cluster", "resume", name)
	case "z":
		return m.startOp("suspend "+name, "cluster", "suspend", name)

	case "c", "n":
		in := textinput.New()
		in.Placeholder = time.Now().Format("2006-01-02-1504")
		in.Focus()
		in.CharLimit = 64
		in.Width = 24
		m.input = &nameInput{input: in, cluster: name}
		return m, textinput.Blink

	case "d", "x":
		if m.focus != paneSnapshots {
			return m, nil
		}
		snap := m.selectedSnapshot()
		if snap == "" {
			return m, nil
		}
		m.confirm = &confirm{
			prompt: fmt.Sprintf("Delete snapshot %q of %q?", snap, name),
			cmd:    m.opCmd("delete of snapshot "+snap, "snapshot", "delete", name, snap),
		}
		return m, nil
	}
	return m, nil
}

func (m model) move(delta int) (tea.Model, tea.Cmd) {
	if m.focus == paneClusters {
		next := m.cCur + delta
		if next < 0 || next >= len(m.clusters) {
			return m, nil
		}
		m.cCur = next
		m.sCur = 0
		return m, m.refresh()
	}
	next := m.sCur + delta
	if next < 0 || next >= len(m.snapshots) {
		return m, nil
	}
	m.sCur = next
	return m, nil
}

// opCmd wraps runOp so the busy state is set by the caller that fires it.
func (m *model) opCmd(desc string, args ...string) tea.Cmd {
	run := runOp(desc, args...)
	return func() tea.Msg { return opStartMsg{desc: desc, run: run} }
}

type opStartMsg struct {
	desc string
	run  tea.Cmd
}

func (m model) startOp(desc string, args ...string) (tea.Model, tea.Cmd) {
	m.busy = desc
	m.status = ""
	m.showOut = false
	return m, tea.Batch(runOp(desc, args...), m.spin.Tick)
}

// --- view ---

var (
	accent    = lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#7D79F6"}
	dim       = lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#5C5C5C"}
	good      = lipgloss.AdaptiveColor{Light: "#0F8A4C", Dark: "#42C883"}
	warn      = lipgloss.AdaptiveColor{Light: "#B58A00", Dark: "#E2C04A"}
	cool      = lipgloss.AdaptiveColor{Light: "#0072C6", Dark: "#56B2F2"}
	bad       = lipgloss.AdaptiveColor{Light: "#C5283D", Dark: "#F2637E"}
	titleSt   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dimSt     = lipgloss.NewStyle().Foreground(dim)
	selectSt  = lipgloss.NewStyle().Bold(true).Background(accent).Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1A1A1A"})
	statusOk  = lipgloss.NewStyle().Foreground(good)
	statusBad = lipgloss.NewStyle().Foreground(bad)
	focusBox  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1)
	blurBox   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(dim).Padding(0, 1)
)

func stateDot(state string) string {
	st := lipgloss.NewStyle()
	switch state {
	case "running":
		return st.Foreground(good).Render("●")
	case "paused":
		return st.Foreground(warn).Render("◐")
	case "suspended":
		return st.Foreground(cool).Render("◌")
	case "stopped":
		return st.Foreground(dim).Render("○")
	default:
		return st.Foreground(dim).Render("·")
	}
}

func lastLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return last
	}
	return err.Error()
}

func (m model) View() string {
	if m.width == 0 {
		return "loading…"
	}

	leftW := m.width * 2 / 5
	if leftW < 34 {
		leftW = 34
	}
	rightW := m.width - leftW - 6

	left := m.clustersView(leftW)
	right := m.snapshotsView(rightW)

	lBox, rBox := blurBox, blurBox
	if m.focus == paneClusters {
		lBox = focusBox
	} else {
		rBox = focusBox
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top,
		lBox.Width(leftW).Render(left),
		" ",
		rBox.Width(rightW).Render(right),
	)

	header := titleSt.Render(" k3c ") + dimSt.Render("· clusters & snapshots")

	parts := []string{header, body, m.statusView()}
	if m.showOut && m.output != "" {
		parts = append(parts, blurBox.Width(m.width-4).Render(tail(m.output, 8)))
	}
	parts = append(parts, m.helpView())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m model) clustersView(width int) string {
	var b strings.Builder
	b.WriteString(titleSt.Render("Clusters") + "\n")
	if len(m.clusters) == 0 {
		b.WriteString(dimSt.Render("no clusters — k3c cluster create"))
		return b.String()
	}
	for i, c := range m.clusters {
		active := " "
		if c.Active {
			active = titleSt.Render("★")
		}
		line := fmt.Sprintf("%s %s %-14s %-9s %6s", active, stateDot(c.Server), c.Name, c.Server, c.RAM)
		if i == m.cCur && m.focus == paneClusters {
			line = selectSt.Render(padRight(stripExtra(c, true), width-2))
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// stripExtra renders a cluster row without color codes for the selection bar.
func stripExtra(c cluster.ClusterInfo, selected bool) string {
	active := " "
	if c.Active {
		active = "★"
	}
	dot := "●"
	switch c.Server {
	case "paused":
		dot = "◐"
	case "suspended":
		dot = "◌"
	case "stopped":
		dot = "○"
	}
	return fmt.Sprintf("%s %s %-14s %-9s %6s", active, dot, c.Name, c.Server, c.RAM)
}

func (m model) snapshotsView(width int) string {
	var b strings.Builder
	name := m.selectedCluster()
	b.WriteString(titleSt.Render("Snapshots") + dimSt.Render(" of "+name) + "\n")
	if len(m.snapshots) == 0 {
		b.WriteString(dimSt.Render("no snapshots — press c to create one"))
		return b.String()
	}
	for i, s := range m.snapshots {
		mode := dimSt.Render(s.Mode)
		if s.Mode == "warm" {
			mode = lipgloss.NewStyle().Foreground(warn).Render(s.Mode)
		}
		line := fmt.Sprintf("  %-24s %s %s", s.Name, mode, dimSt.Render(s.Created))
		if i == m.sCur && m.focus == paneSnapshots {
			line = selectSt.Render(padRight(fmt.Sprintf("  %-24s %-5s %s", s.Name, s.Mode, s.Created), width-2))
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m model) statusView() string {
	switch {
	case m.confirm != nil:
		return lipgloss.NewStyle().Foreground(warn).Render(" " + m.confirm.prompt + " (y/N)")
	case m.input != nil:
		mode := "warm"
		if m.input.cold {
			mode = "cold"
		}
		return fmt.Sprintf(" new %s snapshot of %s: %s %s",
			mode, m.input.cluster, m.input.input.View(),
			dimSt.Render("(enter save · tab warm/cold · esc cancel)"))
	case m.busy != "":
		return " " + m.spin.View() + " " + m.busy + dimSt.Render(" …")
	case m.status != "" && m.failed:
		return statusBad.Render(" ✗ " + m.status + " (o shows output)")
	case m.status != "":
		return statusOk.Render(" " + m.status)
	default:
		return " "
	}
}

func (m model) helpView() string {
	keys := []string{
		"↑↓ move", "⇥ pane", "↵ activate/restore",
		"s start", "S stop", "p pause", "r resume", "z suspend",
		"c snapshot", "d delete", "o output", "q quit",
	}
	return dimSt.Render(" " + strings.Join(keys, " · "))
}

func padRight(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
