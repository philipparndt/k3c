// Package tui is the interactive terminal UI of k3c (k3c ui): clusters and
// their snapshots side by side, with single-key lifecycle operations.
//
// Operations run k3c itself as a subprocess: the CLI commands keep their
// logging and config resolution, the TUI stays responsive and shows the
// captured output.
package tui

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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

// confirm is a pending yes/no question and the command an answer of yes
// runs. A non-nil noCmd runs on decline instead of cancelling — used for
// follow-up questions where "no" still performs the base action.
type confirm struct {
	prompt string
	cmd    tea.Cmd
	noCmd  tea.Cmd
}

// askMsg opens a (follow-up) confirmation.
type askMsg struct{ c confirm }

// nameInput is the open "new snapshot" prompt.
type nameInput struct {
	input   textinput.Model
	cluster string
	cold    bool
	docker  bool // snapshot the docker sidecar instead of a cluster
}

type model struct {
	cfg *config.Config

	clusters     []cluster.ClusterInfo
	snapshots    []cluster.SnapshotInfo
	snapsLoading bool // snapshots of the newly selected cluster in flight
	cCur         int
	sCur         int
	focus        pane

	lastTraffic map[string]trafficSample
	netLine     string // traffic rates of the selected cluster
	cacheLine   string // pull cache performance

	width  int
	height int

	spin     spinner.Model
	busy     string // running operation, "" when idle
	opLine   string // latest output line of the running operation
	opCh     chan opEventMsg
	status   string // last result line
	failed   bool   // last result was an error
	output   string // full output of the last operation
	showOut  bool
	showHelp bool // full keybinding help (toggled with ?)

	confirm *confirm
	input   *nameInput
}

// New builds the TUI model. cfg is only used for state-dir lookups; every
// operation re-resolves its own config in the subprocess.
func New(cfg *config.Config) tea.Model {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(accent)
	return model{cfg: cfg, spin: sp, lastTraffic: map[string]trafficSample{}}
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
	// the cluster the snapshots were listed for: a reply that raced a
	// newer selection must not overwrite its snapshots
	forCluster string
	traffic    *trafficSample
	cacheStats *cluster.PullStats
}

// trafficSample is a point-in-time reading of a cluster VM's cumulative
// external traffic counters.
type trafficSample struct {
	cluster string
	rx, tx  int64
	at      time.Time
}

// opEventMsg streams a running operation: progress lines while it runs,
// then one final done event with the full output.
type opEventMsg struct {
	line   string
	done   bool
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

// selectedKind returns the kind of the selected clusters-pane row ("" for a
// cluster, "docker" for the sidecar).
func (m model) selectedKind() string {
	if m.cCur < len(m.clusters) {
		return m.clusters[m.cCur].Kind
	}
	return ""
}

// dockerKey maps a clusters-pane key to a docker-sidecar lifecycle operation.
func (m model) dockerKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter", "s":
		return m.startOp("docker sidecar up", "docker", "up")
	case "S":
		return m.startOp("docker sidecar down", "docker", "down")
	case "p":
		return m.startOp("docker sidecar pause", "docker", "pause")
	case "r":
		return m.startOp("docker sidecar resume", "docker", "resume")
	case "z":
		return m.startOp("docker sidecar suspend", "docker", "suspend")
	case "c", "n":
		in := textinput.New()
		in.Placeholder = time.Now().Format("2006-01-02-1504")
		in.Focus()
		in.CharLimit = 64
		in.Width = 24
		m.input = &nameInput{input: in, cluster: "docker", docker: true}
		return m, textinput.Blink
	case "d", "x":
		m.confirm = &confirm{
			prompt: "Remove the docker sidecar? (the image-store volume is kept)",
			cmd:    m.opCmd("docker sidecar removal", "docker", "rm"),
		}
		return m, nil
	}
	return m, nil
}

func (m model) refresh() tea.Cmd {
	cfg, name := m.cfg, m.selectedCluster()
	return func() tea.Msg {
		clusters := cluster.Clusters(cfg)
		// the docker sidecar is another managed VM: list it after the clusters
		// so its lifecycle (pause/resume/suspend/up/down) is reachable here too
		if sidecar, ok := cluster.DockerSidecarInfo(cfg); ok {
			clusters = append(clusters, sidecar)
		}
		// keep the selection on reloads; fall back to the first cluster
		current := name
		if current == "" && len(clusters) > 0 {
			current = clusters[0].Name
		}
		snaps := cluster.Snapshots(cfg, current)
		for _, c := range clusters {
			if c.Name == current && c.Kind == "docker" {
				snaps = cluster.DockerSnapshots(cfg)
			}
		}
		msg := dataMsg{clusters: clusters, snapshots: snaps, forCluster: current}
		for _, c := range clusters {
			if c.Name == current && c.Server == "running" {
				if rx, tx, err := cluster.Traffic(cfg, current); err == nil {
					msg.traffic = &trafficSample{cluster: current, rx: rx, tx: tx, at: time.Now()}
				}
			}
		}
		if cfg.PullCacheEnabled {
			if stats, err := cluster.PullCacheStats(cfg); err == nil {
				msg.cacheStats = stats
			}
		}
		return msg
	}
}

// snapsMsg carries a snapshots-only reload (cluster navigation).
type snapsMsg struct {
	snapshots  []cluster.SnapshotInfo
	forCluster string
}

// refreshSnapshots reloads only the selected cluster's snapshots — a
// directory listing, fast enough for cursor navigation, unlike the full
// cluster state refresh.
func (m model) refreshSnapshots() tea.Cmd {
	cfg, name, docker := m.cfg, m.selectedCluster(), m.selectedKind() == "docker"
	return func() tea.Msg {
		if docker {
			return snapsMsg{snapshots: cluster.DockerSnapshots(cfg), forCluster: name}
		}
		return snapsMsg{snapshots: cluster.Snapshots(cfg, name), forCluster: name}
	}
}

func tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

var (
	ansiRe      = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	logPrefixRe = regexp.MustCompile(`^\[\s*\d+\]\s*`)
)

// cleanLine strips colors and the logger's uptime prefix for display.
func cleanLine(s string) string {
	return logPrefixRe.ReplaceAllString(ansiRe.ReplaceAllString(s, ""), "")
}

// startOpStream executes k3c itself with args, streaming output lines.
func startOpStream(args []string) chan opEventMsg {
	ch := make(chan opEventMsg, 16)
	go func() {
		defer close(ch)
		exe, err := os.Executable()
		if err != nil {
			ch <- opEventMsg{done: true, err: err}
			return
		}
		cmd := exec.Command(exe, args...)
		pr, pw, err := os.Pipe()
		if err != nil {
			ch <- opEventMsg{done: true, err: err}
			return
		}
		cmd.Stdout = pw
		cmd.Stderr = pw
		if err := cmd.Start(); err != nil {
			pw.Close()
			pr.Close()
			ch <- opEventMsg{done: true, err: err}
			return
		}
		pw.Close()
		var output strings.Builder
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			line := cleanLine(sc.Text())
			output.WriteString(line + "\n")
			ch <- opEventMsg{line: line}
		}
		pr.Close()
		ch <- opEventMsg{done: true, output: strings.TrimSpace(output.String()), err: cmd.Wait()}
	}()
	return ch
}

// waitOp delivers the next event of the running operation.
func waitOp(ch chan opEventMsg) tea.Cmd {
	return func() tea.Msg {
		if ev, ok := <-ch; ok {
			return ev
		}
		return nil
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
		if m.cCur >= len(m.clusters) {
			m.cCur = max(0, len(m.clusters)-1)
		}
		// only accept snapshots fetched for the current selection
		if msg.forCluster == m.selectedCluster() {
			m.snapshots = msg.snapshots
			m.snapsLoading = false
			if m.sCur >= len(m.snapshots) {
				m.sCur = max(0, len(m.snapshots)-1)
			}
		}
		m.netLine = ""
		if msg.traffic != nil {
			s := *msg.traffic
			if prev, ok := m.lastTraffic[s.cluster]; ok {
				elapsed := s.at.Sub(prev.at).Seconds()
				// counters reset on a cluster restart: skip that sample
				if elapsed > 0 && s.rx >= prev.rx && s.tx >= prev.tx {
					m.netLine = fmt.Sprintf("net  ↓ %s/s  ↑ %s/s   (total ↓ %s  ↑ %s)",
						humanBytes(int64(float64(s.rx-prev.rx)/elapsed)),
						humanBytes(int64(float64(s.tx-prev.tx)/elapsed)),
						humanBytes(s.rx), humanBytes(s.tx))
				}
			}
			m.lastTraffic[s.cluster] = s
		}
		m.cacheLine = ""
		if st := msg.cacheStats; st != nil && st.Hits+st.Misses > 0 {
			m.cacheLine = fmt.Sprintf("pull cache  %.0f%% hits   from cache %s · upstream %s",
				float64(st.Hits)*100/float64(st.Hits+st.Misses),
				humanBytes(st.HitBytes), humanBytes(st.MissBytes))
		}
		return m, nil

	case snapsMsg:
		if msg.forCluster == m.selectedCluster() {
			m.snapshots = msg.snapshots
			m.snapsLoading = false
			if m.sCur >= len(m.snapshots) {
				m.sCur = max(0, len(m.snapshots)-1)
			}
		}
		return m, nil

	case askMsg:
		m.confirm = &msg.c
		return m, nil

	case opStartMsg:
		m.busy = msg.desc
		m.status = ""
		m.showOut = false
		m.opLine = ""
		m.opCh = startOpStream(msg.args)
		return m, tea.Batch(waitOp(m.opCh), m.spin.Tick)

	case opEventMsg:
		if !msg.done {
			m.opLine = msg.line
			return m, waitOp(m.opCh)
		}
		desc := m.busy
		m.busy = ""
		m.opLine = ""
		m.opCh = nil
		m.output = msg.output
		if msg.err != nil {
			m.failed = true
			m.status = desc + " failed: " + lastLine(msg.output, msg.err)
		} else {
			m.failed = false
			m.status = desc + " ✓"
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
	// the full-screen help eats every key: any key closes it (q/ctrl+c quits)
	if m.showHelp {
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		default:
			m.showHelp = false
			return m, nil
		}
	}

	// a pending confirmation eats every key
	if m.confirm != nil {
		c := *m.confirm
		m.confirm = nil
		switch msg.String() {
		case "y", "Y":
			return m, c.cmd
		case "n", "N":
			if c.noCmd != nil {
				return m, c.noCmd
			}
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
			// snapshot names are directory names and CLI arguments; typing
			// spaces is fine, saving them is not — normalize to dashes
			name := strings.TrimSpace(in.input.Value())
			name = strings.Join(strings.Fields(name), "-")
			if name == "" {
				name = in.input.Placeholder
			}
			args := []string{"snapshot", "save", in.cluster, name}
			if in.docker {
				args = []string{"docker", "snapshot", "save", name}
			}
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

	case "?":
		m.showHelp = !m.showHelp
		return m, nil
	}

	if m.busy != "" { // one operation at a time
		return m, nil
	}
	name := m.selectedCluster()
	if name == "" {
		return m, nil
	}

	// the docker sidecar (clusters pane) has its own lifecycle verbs
	if m.focus == paneClusters && m.selectedKind() == "docker" {
		return m.dockerKey(msg.String())
	}

	switch msg.String() {
	case "enter":
		if m.focus == paneSnapshots {
			snap := m.selectedSnapshot()
			if snap == "" {
				return m, nil
			}
			restore := m.opCmd("restore of "+snap+" into "+name, "snapshot", "restore", name, snap)
			if m.selectedKind() == "docker" {
				restore = m.opCmd("restore of "+snap+" into the docker sidecar", "docker", "snapshot", "restore", snap)
			}
			m.confirm = &confirm{
				prompt: fmt.Sprintf("Restore snapshot %q into %q? Its current state is replaced.", snap, name),
				cmd:    restore,
			}
			return m, nil
		}
		if m.selectedKind() == "docker" {
			return m.dockerKey("enter")
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

	case "u":
		if m.selectedKind() == "docker" {
			return m, nil // the docker sidecar has no kube context
		}
		// re-merge ~/.kube/config and switch context to this cluster, fixing a
		// stale endpoint (e.g. after a restart changed the API port, or a
		// same-named context was clobbered) without a full restart
		return m.startOp("use context "+name, "kubeconfig", "merge", name)

	case "m":
		return m.startOp("memory reclaim of "+name, "cluster", "reclaim", name)
	case "M":
		return m.startOp("memory release of "+name, "cluster", "reclaim", name, "--release")

	case "c", "n":
		in := textinput.New()
		in.Placeholder = time.Now().Format("2006-01-02-1504")
		in.Focus()
		in.CharLimit = 64
		in.Width = 24
		m.input = &nameInput{input: in, cluster: name}
		return m, textinput.Blink

	case "e":
		if m.focus != paneSnapshots || m.selectedKind() == "docker" {
			return m, nil // docker sidecar snapshots are not exportable
		}
		if snap := m.selectedSnapshot(); snap != "" {
			out := name + "-" + snap + ".k3csnap"
			return m.startOp("export of "+snap+" to "+out,
				"snapshot", "export", name, snap, "-o", out)
		}
		return m, nil

	case "d", "x":
		if m.focus == paneClusters {
			deleteOnly := m.opCmd("delete of cluster "+name, "cluster", "delete", name)
			first := confirm{
				prompt: fmt.Sprintf("DELETE cluster %q and all its state?", name),
				cmd:    deleteOnly,
			}
			if n := len(m.snapshots); n > 0 {
				followUp := confirm{
					prompt: fmt.Sprintf("Also delete its %d snapshot(s)? (y deletes them, n keeps them, esc cancels everything)", n),
					cmd:    m.opCmd("delete of cluster "+name+" with snapshots", "cluster", "delete", "--snapshots", name),
					noCmd:  deleteOnly,
				}
				first.cmd = func() tea.Msg { return askMsg{c: followUp} }
			}
			m.confirm = &first
			return m, nil
		}
		snap := m.selectedSnapshot()
		if snap == "" {
			return m, nil
		}
		del := m.opCmd("delete of snapshot "+snap, "snapshot", "delete", name, snap)
		if m.selectedKind() == "docker" {
			del = m.opCmd("delete of docker snapshot "+snap, "docker", "snapshot", "delete", snap)
		}
		m.confirm = &confirm{
			prompt: fmt.Sprintf("Delete snapshot %q of %q?", snap, name),
			cmd:    del,
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
		// never show the previous cluster's snapshots while loading
		m.snapshots = nil
		m.snapsLoading = true
		return m, m.refreshSnapshots()
	}
	next := m.sCur + delta
	if next < 0 || next >= len(m.snapshots) {
		return m, nil
	}
	m.sCur = next
	return m, nil
}

// opCmd defers an operation start, so confirmations can carry it.
func (m *model) opCmd(desc string, args ...string) tea.Cmd {
	return func() tea.Msg { return opStartMsg{desc: desc, args: args} }
}

type opStartMsg struct {
	desc string
	args []string
}

func (m model) startOp(desc string, args ...string) (tea.Model, tea.Cmd) {
	return m, m.opCmd(desc, args...)
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
	keySt     = lipgloss.NewStyle().Bold(true).Foreground(cool)
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
	if m.showHelp {
		return m.helpScreen()
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

	header := titleSt.Render(" k3c ") + dimSt.Render("· machines & snapshots")

	parts := []string{header, body}
	if info := m.selectionInfoView(); info != "" {
		parts = append(parts, info)
	}
	parts = append(parts, m.statusView())
	if m.showOut && m.output != "" {
		parts = append(parts, blurBox.Width(m.width-4).Render(tail(m.output, 8)))
	}
	parts = append(parts, m.helpView())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m model) clustersView(width int) string {
	var b strings.Builder
	b.WriteString(titleSt.Render("Machines") + "\n")
	if len(m.clusters) == 0 {
		b.WriteString(dimSt.Render("no clusters — k3c cluster create"))
		return b.String()
	}
	for i, c := range m.clusters {
		active := " "
		if c.Active {
			active = titleSt.Render("★")
		}
		line := fmt.Sprintf("%s %s %-12s %-7s %-9s %6s",
			active, stateDot(c.Server), c.Name, typeLabel(c.Kind), c.Server, c.RAM)
		if i == m.cCur && m.focus == paneClusters {
			line = selectSt.Render(padRight(stripExtra(c, true), width-2))
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// typeLabel is the short machine-type tag shown in the list: clusters are k3s
// VMs; the sidecar is the docker VM.
func typeLabel(kind string) string {
	if kind == "docker" {
		return "docker"
	}
	return "k3s"
}

// stripExtra renders a machine row without color codes for the selection bar.
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
	return fmt.Sprintf("%s %s %-12s %-7s %-9s %6s",
		active, dot, c.Name, typeLabel(c.Kind), c.Server, c.RAM)
}

// selectionInfoView shows details for the highlighted machine — its type and,
// for a cluster, its kube context — plus its network and pull-cache lines,
// below the boxes rather than inside the machines list.
func (m model) selectionInfoView() string {
	if m.cCur >= len(m.clusters) {
		return ""
	}
	c := m.clusters[m.cCur]
	detail := ""
	if c.Kind != "docker" && c.Context != "" {
		detail = " · " + c.Context
	}
	var b strings.Builder
	b.WriteString(dimSt.Render(fmt.Sprintf(" %s · %s%s · %s",
		c.Name, typeLabel(c.Kind), detail, c.Server)))
	if m.netLine != "" {
		b.WriteString("\n" + dimSt.Render(" "+m.netLine))
	}
	if m.cacheLine != "" {
		b.WriteString("\n" + dimSt.Render(" "+m.cacheLine))
	}
	return b.String()
}

func (m model) snapshotsView(width int) string {
	var b strings.Builder
	name := m.selectedCluster()
	b.WriteString(titleSt.Render("Snapshots") + dimSt.Render(" of "+name) + "\n")
	if m.snapsLoading {
		b.WriteString(m.spin.View() + dimSt.Render(" loading…"))
		return b.String()
	}
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
			dimSt.Render("(enter save · tab warm/cold · esc cancel · spaces become dashes)"))
	case m.busy != "":
		line := ""
		if m.opLine != "" {
			line = dimSt.Render(" · " + m.opLine)
		}
		return " " + m.spin.View() + " " + m.busy + dimSt.Render(" …") + line
	case m.status != "" && m.failed:
		return statusBad.Render(" ✗ " + m.status + " (o shows output)")
	case m.status != "":
		return statusOk.Render(" " + m.status)
	default:
		return " "
	}
}

// helpView is the condensed bottom bar (always visible); the full keymap is
// the ?-toggled full-screen helpScreen.
func (m model) helpView() string {
	return dimSt.Render(" ↑↓ move · ⇥ switch · ↵ select · c snapshot · ? help · q quit")
}

// helpBind is one key/description row in the full-screen help.
type helpBind struct{ key, desc string }

// helpScreen renders the full-screen keybinding reference (k9s-style), shown
// while showHelp is set; any key closes it (see handleKey).
func (m model) helpScreen() string {
	col := func(title string, binds []helpBind) string {
		w := 0
		for _, b := range binds {
			if k := lipgloss.Width(b.key); k > w {
				w = k
			}
		}
		var sb strings.Builder
		sb.WriteString(titleSt.Render(title) + "\n")
		for _, b := range binds {
			sb.WriteString(" " + keySt.Render(padRight(b.key, w)) + "  " + dimSt.Render(b.desc) + "\n")
		}
		return strings.TrimRight(sb.String(), "\n")
	}

	general := col("GENERAL", []helpBind{
		{"↑ ↓ / j k", "move"},
		{"⇥", "switch pane"},
		{"g / F5", "refresh"},
		{"o", "toggle output"},
		{"? / esc", "close help"},
		{"q / ^C", "quit"},
	})
	machines := col("MACHINES", []helpBind{
		{"↵", "activate cluster"},
		{"s", "start"},
		{"S", "stop"},
		{"p", "pause"},
		{"r", "resume"},
		{"z", "suspend"},
		{"u", "use-context (kubeconfig)"},
		{"m", "reclaim memory"},
		{"M", "release memory"},
		{"c", "new snapshot"},
		{"d / x", "delete cluster"},
	})
	snapshots := col("SNAPSHOTS", []helpBind{
		{"↵", "restore"},
		{"c", "create"},
		{"d / x", "delete"},
		{"e", "export"},
	})
	docker := col("DOCKER SIDECAR", []helpBind{
		{"↵ / s", "up"},
		{"S", "down"},
		{"p / r", "pause / resume"},
		{"z", "suspend"},
		{"c", "snapshot"},
		{"d / x", "remove"},
	})

	gap := "    "
	topRow := lipgloss.JoinHorizontal(lipgloss.Top, general, gap, machines, gap, snapshots)
	title := titleSt.Render(" k3c ") + dimSt.Render("· keybindings")
	footer := dimSt.Render(" ? or esc to close")
	content := lipgloss.JoinVertical(lipgloss.Left, title, "", topRow, "", docker, "", footer)
	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Top, content)
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

func humanBytes(b int64) string {
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(b)/1e6)
	case b >= 1e3:
		return fmt.Sprintf("%.1f kB", float64(b)/1e3)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
