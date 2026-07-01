// Package tui is the interactive terminal UI of k3c (k3c ui): a k9s-style
// tree of machines with their snapshots as children, a top header with a
// context info panel and shortcut menu, modal dialogs for input, and a
// session-long command log.
//
// Operations run k3c itself as a subprocess: the CLI commands keep their
// logging and config resolution, the TUI stays responsive and shows the
// captured output.
package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/philipparndt/go-logger"

	"k3c/cluster"
	"k3c/config"
	"k3c/runtime"
)

// rowKind distinguishes a top-level machine row from a nested snapshot row.
type rowKind int

const (
	rowMachine rowKind = iota
	rowSnapshot
)

// treeRow is one visible line of the flattened machine/snapshot tree. A
// snapshot row always carries the index of its parent machine, so
// machine-scoped operations work whether the cursor sits on the machine or on
// one of its snapshots.
type treeRow struct {
	kind        rowKind
	machine     int    // index into model.clusters
	snapName    string // set on a snapshot row
	snapMode    string
	snapWhen    string
	snapSize    string // human-readable on-disk size of a snapshot
	placeholder string // dim filler row ("loading…", "no snapshots …")
}

// confirm is a pending yes/no question and the command an answer of yes
// runs. A non-nil noCmd runs on decline instead of cancelling — used for
// follow-up questions where "no" still performs the base action.
//
// The dialog renders as buttons (cancel on the left, the affirmative action on
// the right; a noCmd adds a middle button). destructive paints the affirmative
// button red. focus is the selected button, navigated with ←/→ and defaulting
// to the safe leftmost (Cancel).
type confirm struct {
	prompt      string
	cmd         tea.Cmd
	noCmd       tea.Cmd
	yesLabel    string // affirmative button label (default "OK")
	noLabel     string // middle button label when noCmd is set (default "No")
	destructive bool   // paint the affirmative button red
	focus       int    // selected button index (0 = Cancel)
}

// confirmButton is one rendered button in a confirm dialog. A nil action is
// the cancel button.
type confirmButton struct {
	label       string
	destructive bool
	action      tea.Cmd
}

// buttons lays out the dialog's buttons left-to-right: Cancel, then (when a
// noCmd decline path exists) the decline button, then the affirmative action.
func (c confirm) buttons() []confirmButton {
	yes := c.yesLabel
	if yes == "" {
		yes = "OK"
	}
	btns := []confirmButton{{label: "Cancel"}}
	if c.noCmd != nil {
		no := c.noLabel
		if no == "" {
			no = "No"
		}
		btns = append(btns, confirmButton{label: no, action: c.noCmd})
	}
	btns = append(btns, confirmButton{label: yes, destructive: c.destructive, action: c.cmd})
	return btns
}

// askMsg opens a (follow-up) confirmation.
type askMsg struct{ c confirm }

// openSnapshotWizardMsg opens the "new snapshot" create wizard. It lets a dialog
// button (the New/Recreate choice) open the wizard, which is not a plain op.
type openSnapshotWizardMsg struct {
	cluster string
	docker  bool
}

// nameInput is the open "new snapshot" wizard.
type nameInput struct {
	input   textinput.Model
	cluster string
	mode    cluster.SnapshotMode
	docker  bool // snapshot the docker sidecar instead of a cluster
}

// modes lists the tiers the wizard cycles through. The docker sidecar
// supports only warm/cold — frozen drops the image store and rehydrates it
// from the cluster pull-cache, which the sidecar does not use.
func (in nameInput) modes() []cluster.SnapshotMode {
	if in.docker {
		return []cluster.SnapshotMode{cluster.ModeWarm, cluster.ModeCold}
	}
	return []cluster.SnapshotMode{cluster.ModeWarm, cluster.ModeCold, cluster.ModeFrozen}
}

// cycleMode advances the wizard to the next tier (wraps around).
func (in *nameInput) cycleMode() {
	modes := in.modes()
	for i, md := range modes {
		if md == in.mode {
			in.mode = modes[(i+1)%len(modes)]
			return
		}
	}
	in.mode = modes[0]
}

// modeDesc is the one-line description shown as the user tabs through tiers.
func modeDesc(mode cluster.SnapshotMode) string {
	switch mode {
	case cluster.ModeCold:
		return "full disk; boots fresh (~30–60s)"
	case cluster.ModeFrozen:
		return "state + volumes only; rehydrates images on thaw (minutes)"
	default:
		return "memory + disk; resumes instantly (largest)"
	}
}

// newSnapshotWizard builds the "new snapshot" create wizard for a cluster, or
// for the docker sidecar when docker is set.
func newSnapshotWizard(clusterName string, docker bool) *nameInput {
	in := textinput.New()
	in.Placeholder = time.Now().Format("2006-01-02-1504")
	in.Focus()
	in.CharLimit = 64
	in.Width = 24
	if docker {
		clusterName = "docker"
	}
	return &nameInput{input: in, cluster: clusterName, docker: docker, mode: cluster.ModeWarm}
}

// renameInput is the open "rename snapshot" dialog.
type renameInput struct {
	input   textinput.Model
	cluster string
	oldName string
	docker  bool // rename a docker sidecar snapshot instead of a cluster's
}

// exportPick is the open frozen-export tier chooser (slim/fat/thin). Only
// frozen snapshots offer it; warm/cold export their disk image directly.
type exportPick struct {
	cluster string
	snap    string
	out     string
	mode    cluster.FrozenExportMode
}

// cycle advances to the next export tier (wraps around). Order puts the small,
// sensible default (slim) first.
func (e *exportPick) cycle() {
	order := []cluster.FrozenExportMode{cluster.FrozenSlim, cluster.FrozenFat, cluster.FrozenThin}
	for i, md := range order {
		if md == e.mode {
			e.mode = order[(i+1)%len(order)]
			return
		}
	}
	e.mode = order[0]
}

// exportModeDesc is the one-line description shown as the user tabs through
// export tiers.
func exportModeDesc(mode cluster.FrozenExportMode) string {
	switch mode {
	case cluster.FrozenFat:
		return "self-contained: bundles all image blobs (largest; imports offline)"
	case cluster.FrozenThin:
		return "no images (smallest; only safe with no local-only images)"
	default: // slim
		return "local-only images bundled; remote images re-pull on import"
	}
}

// commandRun is one executed operation, kept for the whole session and shown
// in the command-log dialog.
type commandRun struct {
	desc   string
	args   []string
	output string
	err    error
	when   time.Time
}

// sortMode is how each machine's snapshots are ordered. The machine order
// itself is always stable (name order, as the cluster package returns it).
type sortMode int

const (
	sortByName sortMode = iota // alphabetical (default)
	sortByDate                 // newest snapshot first
)

func (s sortMode) label() string {
	if s == sortByDate {
		return "by date"
	}
	return "by name"
}

type model struct {
	cfg *config.Config

	sortMode sortMode // snapshot ordering within each machine (name | date)

	clusters       []cluster.ClusterInfo
	snapsByMachine map[string][]cluster.SnapshotInfo // snapshots of every loaded machine
	expanded       map[string]bool                   // machine name → expanded (default true)
	loading        map[string]bool                   // machine name → snapshot load in flight
	rows           []treeRow                         // flattened visible tree
	cur            int                               // cursor into rows
	loaded         bool                              // first refresh has returned

	lastTraffic  map[string]trafficSample
	netLine      string // traffic rates of the selected cluster
	netTotalLine string // cumulative traffic of the selected cluster
	cacheLine    string // pull cache performance (global)

	daemons cluster.DaemonsInfo // host-daemon process and listener state

	width   int
	height  int
	listTop int // index of the first visible list row when scrolling (compact view)

	spin      spinner.Model
	busy      string   // running operation, "" when idle
	busyArgs  []string // args of the running operation (recorded into the log)
	opLine    string   // latest output line of the running operation
	opCh      chan opEventMsg
	status    string // last result line
	failed    bool   // last result was an error
	statusSeq int    // bumped on every status change; gates the auto-dismiss timer

	commands    []commandRun // session-long command history
	logVP       viewport.Model
	helpVP      viewport.Model
	diagramVP   viewport.Model
	showLog     bool // command-log dialog open
	showHelp    bool // keybinding help dialog open
	showDiagram bool // system data-flow diagram open

	confirm    *confirm
	input      *nameInput
	rename     *renameInput
	exportPick *exportPick
}

// New builds the TUI model. cfg supplies state-dir lookups and the color
// theme; every operation re-resolves its own config in the subprocess.
func New(cfg *config.Config) tea.Model {
	applyTheme(cfg.Theme)
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(accent)
	return model{
		cfg:            cfg,
		spin:           sp,
		lastTraffic:    map[string]trafficSample{},
		snapsByMachine: map[string][]cluster.SnapshotInfo{},
		expanded:       map[string]bool{},
		loading:        map[string]bool{},
	}
}

// Run starts the TUI.
func Run(cfg *config.Config) error {
	// The TUI owns the terminal (alt-screen); stray log lines from in-process
	// calls like runtime.EnsureSystem would corrupt the frame. Silence the
	// global logger for the session and restore it on exit.
	logger.LogTo(io.Discard)
	defer logger.LogTo(os.Stderr)
	_, err := tea.NewProgram(New(cfg), tea.WithAltScreen()).Run()
	return err
}

// --- messages ---

type dataMsg struct {
	clusters       []cluster.ClusterInfo
	snapsByMachine map[string][]cluster.SnapshotInfo
	traffic        *trafficSample
	cacheStats     *cluster.PullStats
	daemons        *cluster.DaemonsInfo
}

// snapsMsg carries a single machine's snapshots — an on-expand lazy load.
type snapsMsg struct {
	machine string
	snaps   []cluster.SnapshotInfo
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

// clearStatusMsg fires after a delay to dismiss a success status line. It
// carries the statusSeq it was scheduled for, so a newer status (or a sticky
// failure) is left untouched.
type clearStatusMsg struct{ seq int }

// statusDismiss is how long a success result stays before auto-clearing.
const statusDismiss = 4 * time.Second

func clearStatusAfter(seq int) tea.Cmd {
	return tea.Tick(statusDismiss, func(time.Time) tea.Msg { return clearStatusMsg{seq: seq} })
}

// --- selection helpers ---

func (m model) curRow() (treeRow, bool) {
	if m.cur >= 0 && m.cur < len(m.rows) {
		return m.rows[m.cur], true
	}
	return treeRow{}, false
}

func (m model) curMachine() (cluster.ClusterInfo, bool) {
	r, ok := m.curRow()
	if !ok || r.machine < 0 || r.machine >= len(m.clusters) {
		return cluster.ClusterInfo{}, false
	}
	return m.clusters[r.machine], true
}

func (m model) curName() string {
	if c, ok := m.curMachine(); ok {
		return c.Name
	}
	return ""
}

func (m model) curKind() string {
	if c, ok := m.curMachine(); ok {
		return c.Kind
	}
	return ""
}

// onSnapshot reports whether the cursor sits on a real snapshot row (not a
// placeholder filler).
func (m model) onSnapshot() bool {
	r, ok := m.curRow()
	return ok && r.kind == rowSnapshot && r.snapName != ""
}

func (m model) curSnapshot() string {
	if r, ok := m.curRow(); ok && r.kind == rowSnapshot {
		return r.snapName
	}
	return ""
}

// --- commands ---

// dockerKey maps a key to a docker-sidecar lifecycle operation.
func (m model) dockerKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		return m.startOp("activate docker sidecar", "docker", "activate")
	case "s":
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
		m.input = newSnapshotWizard("docker", true)
		return m, textinput.Blink
	case "d", "x":
		m.confirm = &confirm{
			prompt:      "Remove the docker sidecar? (the image-store volume is kept)",
			cmd:         m.opCmd("docker sidecar removal", "docker", "rm"),
			yesLabel:    "Remove",
			destructive: true,
		}
		return m, nil
	}
	return m, nil
}

func (m model) refresh() tea.Cmd {
	cfg := m.cfg
	curName := m.curName()
	pullCache := cfg.PullCacheEnabled
	expanded := make(map[string]bool, len(m.expanded))
	for k, v := range m.expanded {
		expanded[k] = v
	}
	return func() tea.Msg {
		// Start the container system first, like `cluster list` does. A stopped
		// system (e.g. right after a host restart) makes `container ls` fail, so
		// Clusters would return nothing and the tree would falsely read "no
		// clusters". EnsureSystem runs its work at most once per process, so the
		// periodic refreshes that follow are cheap.
		_ = runtime.EnsureSystem()
		clusters := cluster.Clusters(cfg)
		// the docker sidecar is another managed VM: list it after the clusters
		// so its lifecycle (pause/resume/suspend/up/down) is reachable here too
		if sidecar, ok := cluster.DockerSidecarInfo(cfg); ok {
			clusters = append(clusters, sidecar)
		}
		// load snapshots of every expanded machine (new machines default to
		// expanded), so the tree shows children without an extra keystroke
		snaps := make(map[string][]cluster.SnapshotInfo)
		for _, c := range clusters {
			exp, ok := expanded[c.Name]
			if !ok {
				exp = true
			}
			if !exp {
				continue
			}
			if c.Kind == "docker" {
				snaps[c.Name] = cluster.DockerSnapshots(cfg)
			} else {
				snaps[c.Name] = cluster.Snapshots(cfg, c.Name)
			}
		}
		msg := dataMsg{clusters: clusters, snapsByMachine: snaps}
		for _, c := range clusters {
			if c.Name == curName && c.Server == "running" {
				if rx, tx, err := cluster.Traffic(cfg, curName); err == nil {
					msg.traffic = &trafficSample{cluster: curName, rx: rx, tx: tx, at: time.Now()}
				}
			}
		}
		if pullCache {
			if stats, err := cluster.PullCacheStats(cfg); err == nil {
				msg.cacheStats = stats
			}
		}
		d := cluster.DaemonsState(cfg)
		msg.daemons = &d
		return msg
	}
}

// refreshSnapshots loads one machine's snapshots — a directory listing, fast
// enough to keep an expand snappy instead of waiting for the next full refresh.
func (m model) refreshSnapshots(name, kind string) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		if kind == "docker" {
			return snapsMsg{machine: name, snaps: cluster.DockerSnapshots(cfg)}
		}
		return snapsMsg{machine: name, snaps: cluster.Snapshots(cfg, name)}
	}
}

// sortedSnaps returns a machine's snapshots ordered by the active sort mode: by
// name (alphabetical) or by date (newest first). Created is an RFC3339 string;
// unparseable/empty values sort last so the mode degrades to name order. The
// returned slice is a copy, so the stored order is left untouched.
func (m model) sortedSnaps(machine string) []cluster.SnapshotInfo {
	snaps := append([]cluster.SnapshotInfo(nil), m.snapsByMachine[machine]...)
	if m.sortMode == sortByDate {
		when := func(s cluster.SnapshotInfo) (time.Time, bool) {
			t, err := time.Parse(time.RFC3339, s.Created)
			return t, err == nil
		}
		sort.SliceStable(snaps, func(i, j int) bool {
			ti, oki := when(snaps[i])
			tj, okj := when(snaps[j])
			if oki != okj {
				return oki // parseable dates before unknown
			}
			if oki && !ti.Equal(tj) {
				return ti.After(tj) // newest first
			}
			return snaps[i].Name < snaps[j].Name
		})
		return snaps
	}
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].Name < snaps[j].Name })
	return snaps
}

// rebuildRows recomputes the flattened visible tree from clusters, the
// expanded set, and the loaded snapshots.
func (m *model) rebuildRows() {
	rows := make([]treeRow, 0, len(m.clusters))
	for i, c := range m.clusters {
		rows = append(rows, treeRow{kind: rowMachine, machine: i})
		if !m.expanded[c.Name] {
			continue
		}
		switch {
		case m.loading[c.Name]:
			rows = append(rows, treeRow{kind: rowSnapshot, machine: i, placeholder: "loading…"})
		case len(m.snapsByMachine[c.Name]) == 0:
			rows = append(rows, treeRow{kind: rowSnapshot, machine: i, placeholder: "no snapshots — press c to create one"})
		default:
			for _, s := range m.sortedSnaps(c.Name) {
				rows = append(rows, treeRow{
					kind: rowSnapshot, machine: i,
					snapName: s.Name, snapMode: s.Mode, snapWhen: s.Created,
					snapSize: humanBytes(s.Size),
				})
			}
		}
	}
	m.rows = rows
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
		if m.showLog {
			m.sizeLog()
		}
		if m.showHelp {
			m.sizeHelp()
		}
		if m.showDiagram {
			m.sizeDiagram()
		}
		m.clampScroll()
		// Force a full repaint: on resize Bubble Tea's frame diff can leave
		// stale cells from the previous (larger) layout on screen.
		return m, tea.ClearScreen

	case dataMsg:
		m.loaded = true
		m.clusters = msg.clusters
		// new machines default to expanded; user toggles are preserved
		for _, c := range m.clusters {
			if _, ok := m.expanded[c.Name]; !ok {
				m.expanded[c.Name] = true
			}
		}
		for name, s := range msg.snapsByMachine {
			m.snapsByMachine[name] = s
			delete(m.loading, name)
		}
		m.rebuildRows()
		if m.cur >= len(m.rows) {
			m.cur = max(0, len(m.rows)-1)
		}
		m.clampScroll()
		m.netLine = ""
		m.netTotalLine = ""
		if msg.traffic != nil {
			s := *msg.traffic
			m.netTotalLine = fmt.Sprintf("↓ %s  ↑ %s", humanBytes(s.rx), humanBytes(s.tx))
			if prev, ok := m.lastTraffic[s.cluster]; ok {
				elapsed := s.at.Sub(prev.at).Seconds()
				// counters reset on a cluster restart: skip that sample
				if elapsed > 0 && s.rx >= prev.rx && s.tx >= prev.tx {
					m.netLine = fmt.Sprintf("↓ %s/s  ↑ %s/s",
						humanBytes(int64(float64(s.rx-prev.rx)/elapsed)),
						humanBytes(int64(float64(s.tx-prev.tx)/elapsed)))
				}
			}
			m.lastTraffic[s.cluster] = s
		}
		m.cacheLine = ""
		if st := msg.cacheStats; st != nil && st.Hits+st.Misses > 0 {
			m.cacheLine = fmt.Sprintf("%.0f%% hits · cache %s · up %s",
				float64(st.Hits)*100/float64(st.Hits+st.Misses),
				humanBytes(st.HitBytes), humanBytes(st.MissBytes))
		}
		if msg.daemons != nil {
			m.daemons = *msg.daemons
		}
		return m, nil

	case snapsMsg:
		m.snapsByMachine[msg.machine] = msg.snaps
		delete(m.loading, msg.machine)
		m.rebuildRows()
		if m.cur >= len(m.rows) {
			m.cur = max(0, len(m.rows)-1)
		}
		m.clampScroll()
		return m, nil

	case askMsg:
		m.confirm = &msg.c
		return m, nil

	case openSnapshotWizardMsg:
		m.input = newSnapshotWizard(msg.cluster, msg.docker)
		return m, textinput.Blink

	case opStartMsg:
		m.busy = msg.desc
		m.busyArgs = msg.args
		m.status = ""
		m.statusSeq++
		m.opLine = ""
		m.opCh = startOpStream(msg.args)
		return m, tea.Batch(waitOp(m.opCh), m.spin.Tick)

	case opEventMsg:
		if !msg.done {
			m.opLine = msg.line
			return m, waitOp(m.opCh)
		}
		desc, args := m.busy, m.busyArgs
		m.busy = ""
		m.busyArgs = nil
		m.opLine = ""
		m.opCh = nil
		m.commands = append(m.commands, commandRun{
			desc: desc, args: args, output: msg.output, err: msg.err, when: time.Now(),
		})
		m.statusSeq++
		if msg.err != nil {
			m.failed = true
			m.status = desc + " failed: " + lastLine(msg.output, msg.err)
		} else {
			m.failed = false
			m.status = desc + " ✓"
		}
		if m.showLog {
			m.logVP.SetContent(m.logContent())
		}
		// a success line auto-dismisses; failures stay (they point at the log)
		if m.failed {
			return m, m.refresh()
		}
		return m, tea.Batch(m.refresh(), clearStatusAfter(m.statusSeq))

	case clearStatusMsg:
		// only clear if no newer status has replaced this one
		if msg.seq == m.statusSeq {
			m.status = ""
		}
		return m, nil

	case tickMsg:
		if m.busy == "" && m.confirm == nil && m.input == nil && m.exportPick == nil {
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
	// the command-log dialog: scrolling keys drive the viewport, esc/o close
	if m.showLog {
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "l", "esc", "?":
			m.showLog = false
			return m, nil
		}
		var cmd tea.Cmd
		m.logVP, cmd = m.logVP.Update(msg)
		return m, cmd
	}

	// the help dialog: scrolling keys drive the viewport, ? / esc close it
	if m.showHelp {
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?", "esc":
			m.showHelp = false
			return m, nil
		}
		var cmd tea.Cmd
		m.helpVP, cmd = m.helpVP.Update(msg)
		return m, cmd
	}

	// the system diagram: scrolling keys drive the viewport, D / esc close it
	if m.showDiagram {
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "D", "esc":
			m.showDiagram = false
			return m, nil
		}
		// refresh bounds against the current diagram before scrolling.
		m.diagramVP.SetContent(m.diagramContent())
		var cmd tea.Cmd
		m.diagramVP, cmd = m.diagramVP.Update(msg)
		return m, cmd
	}

	// a pending confirmation eats every key: arrows/tab move between buttons,
	// enter activates the focused one; y/n/esc stay as direct shortcuts
	if m.confirm != nil {
		c := *m.confirm
		btns := c.buttons()
		cancel := func() (tea.Model, tea.Cmd) {
			m.confirm = nil
			m.status = "cancelled"
			m.failed = false
			m.statusSeq++
			return m, nil
		}
		switch msg.String() {
		case "left", "h":
			if c.focus > 0 {
				c.focus--
			}
			m.confirm = &c
			return m, nil
		case "right", "l":
			if c.focus < len(btns)-1 {
				c.focus++
			}
			m.confirm = &c
			return m, nil
		case "tab":
			c.focus = (c.focus + 1) % len(btns)
			m.confirm = &c
			return m, nil
		case "shift+tab":
			c.focus = (c.focus - 1 + len(btns)) % len(btns)
			m.confirm = &c
			return m, nil
		case "enter", " ":
			b := btns[c.focus]
			if b.action == nil {
				return cancel()
			}
			m.confirm = nil
			return m, b.action
		case "y", "Y":
			m.confirm = nil
			return m, c.cmd
		case "n", "N":
			m.confirm = nil
			if c.noCmd != nil {
				return m, c.noCmd
			}
			return cancel()
		case "esc", "q":
			return cancel()
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil // ignore any other key
	}

	// the frozen-export tier chooser eats every key
	if m.exportPick != nil {
		switch msg.Type {
		case tea.KeyEscape:
			m.exportPick = nil
			m.status = "cancelled"
			m.failed = false
			m.statusSeq++
			return m, nil
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyTab:
			m.exportPick.cycle()
			return m, nil
		case tea.KeyEnter:
			p := *m.exportPick
			m.exportPick = nil
			args := []string{"snapshot", "export", p.cluster, p.snap, "-o", p.out}
			switch p.mode {
			case cluster.FrozenSlim:
				args = append(args, "--slim")
			case cluster.FrozenThin:
				args = append(args, "--thin")
			}
			return m.startOp(fmt.Sprintf("%s export of %s to %s", p.mode, p.snap, p.out), args...)
		}
		return m, nil
	}

	// the snapshot wizard eats every key
	if m.input != nil {
		switch msg.Type {
		case tea.KeyEscape:
			m.input = nil
			m.status = "cancelled"
			m.failed = false
			m.statusSeq++
			return m, nil
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyTab:
			m.input.cycleMode()
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
			mode := in.mode
			if mode == "" {
				mode = cluster.ModeWarm
			}
			switch mode {
			case cluster.ModeCold:
				args = append(args, "--cold")
			case cluster.ModeFrozen:
				args = append(args, "--frozen")
			}
			return m.startOp(fmt.Sprintf("%s snapshot %q of %s", mode, name, in.cluster), args...)
		}
		var cmd tea.Cmd
		in := *m.input
		in.input, cmd = in.input.Update(msg)
		m.input = &in
		return m, cmd
	}

	// the rename dialog eats every key
	if m.rename != nil {
		switch msg.Type {
		case tea.KeyEscape:
			m.rename = nil
			m.status = "cancelled"
			m.failed = false
			m.statusSeq++
			return m, nil
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyEnter:
			rn := *m.rename
			m.rename = nil
			// snapshot names are directory names and CLI arguments; normalize
			// typed spaces to dashes like the create wizard does
			name := strings.TrimSpace(rn.input.Value())
			name = strings.Join(strings.Fields(name), "-")
			if name == "" || name == rn.oldName {
				m.status = "cancelled"
				m.failed = false
				m.statusSeq++
				return m, nil
			}
			args := []string{"snapshot", "rename", rn.cluster, rn.oldName, name}
			if rn.docker {
				args = []string{"docker", "snapshot", "rename", rn.oldName, name}
			}
			return m.startOp(fmt.Sprintf("rename snapshot %q to %q", rn.oldName, name), args...)
		}
		var cmd tea.Cmd
		rn := *m.rename
		rn.input, cmd = rn.input.Update(msg)
		m.rename = &rn
		return m, cmd
	}

	// global navigation and dialogs
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		return m.move(-1)
	case "down", "j":
		return m.move(1)
	case "right":
		return m.expand()
	case "left":
		return m.collapse()
	case "g", "f5":
		return m, m.refresh()
	case "o":
		return m.cycleSort()
	case "l":
		m.openLog()
		return m, nil
	case "?":
		m.openHelp()
		return m, nil
	case "D":
		m.openDiagram()
		return m, nil
	}

	if m.busy != "" { // one operation at a time
		return m, nil
	}
	name := m.curName()
	if name == "" {
		return m, nil
	}
	kind := m.curKind()

	// the docker sidecar has its own lifecycle verbs; lifecycle keys target the
	// sidecar from any of its rows, machine-only verbs from its machine row
	if kind == "docker" {
		if r, _ := m.curRow(); r.kind == rowMachine {
			return m.dockerKey(msg.String())
		}
		switch msg.String() {
		case "s", "S", "p", "r", "z":
			return m.dockerKey(msg.String())
		case "u", "m", "M":
			return m, nil // not applicable to the docker sidecar
		}
		// enter / c / d / x / e / R fall through to the snapshot logic below
	}

	switch msg.String() {
	case "enter":
		if m.onSnapshot() {
			snap := m.curSnapshot()
			restore := m.opCmd("restore of "+snap+" into "+name, "snapshot", "restore", name, snap)
			if kind == "docker" {
				restore = m.opCmd("restore of "+snap+" into the docker sidecar", "docker", "snapshot", "restore", snap)
			}
			m.confirm = &confirm{
				prompt:      fmt.Sprintf("Restore snapshot %q into %q? Its current state is replaced.", snap, name),
				cmd:         restore,
				yesLabel:    "Restore",
				destructive: true,
			}
			return m, nil
		}
		// machine row (docker machine rows are handled above)
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
		if kind == "docker" {
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
		// On a snapshot row, offer New vs Recreate; on a machine row, go straight
		// to the create wizard.
		if m.onSnapshot() {
			snap := m.curSnapshot()
			r, _ := m.curRow()
			args := []string{"snapshot", "save", name, snap, "--replace"}
			if kind == "docker" {
				args = []string{"docker", "snapshot", "save", snap, "--replace"}
			}
			switch r.snapMode {
			case string(cluster.ModeCold):
				args = append(args, "--cold")
			case string(cluster.ModeFrozen):
				args = append(args, "--frozen")
			}
			docker := kind == "docker"
			m.confirm = &confirm{
				prompt:      fmt.Sprintf("Create a new snapshot, or recreate %q in place?", snap),
				noCmd:       func() tea.Msg { return openSnapshotWizardMsg{cluster: name, docker: docker} },
				noLabel:     "New snapshot",
				cmd:         m.opCmd(fmt.Sprintf("recreate snapshot %q of %s", snap, name), args...),
				yesLabel:    "Recreate",
				destructive: true,
				focus:       1, // default to the non-destructive "New snapshot"
			}
			return m, nil
		}
		m.input = newSnapshotWizard(name, kind == "docker")
		return m, textinput.Blink

	case "e":
		if !m.onSnapshot() || kind == "docker" {
			return m, nil // docker sidecar snapshots are not exportable
		}
		snap := m.curSnapshot()
		out := name + "-" + snap + ".k3csnap"
		// Frozen snapshots have a size/self-containment dial — let the user
		// pick the tier; warm/cold export their disk image directly.
		if r, ok := m.curRow(); ok && r.snapMode == string(cluster.ModeFrozen) {
			m.exportPick = &exportPick{cluster: name, snap: snap, out: out, mode: cluster.FrozenSlim}
			return m, nil
		}
		return m.startOp("export of "+snap+" to "+out,
			"snapshot", "export", name, snap, "-o", out)

	case "R":
		if !m.onSnapshot() {
			return m, nil // rename targets a snapshot, not a machine
		}
		snap := m.curSnapshot()
		in := textinput.New()
		in.SetValue(snap)
		in.Placeholder = snap
		in.Focus()
		in.CharLimit = 64
		in.Width = 24
		m.rename = &renameInput{input: in, cluster: name, oldName: snap, docker: kind == "docker"}
		return m, textinput.Blink

	case "d", "x":
		if !m.onSnapshot() {
			deleteOnly := m.opCmd("delete of cluster "+name, "cluster", "delete", name)
			first := confirm{
				prompt:      fmt.Sprintf("DELETE cluster %q and all its state?", name),
				cmd:         deleteOnly,
				yesLabel:    "Delete",
				destructive: true,
			}
			if n := len(m.snapsByMachine[name]); n > 0 {
				// Cancel here aborts everything; the two action buttons are the
				// keep-snapshots and delete-snapshots paths.
				followUp := confirm{
					prompt:      fmt.Sprintf("Also delete its %d snapshot(s)?", n),
					cmd:         m.opCmd("delete of cluster "+name+" with snapshots", "cluster", "delete", "--snapshots", name),
					noCmd:       deleteOnly,
					yesLabel:    "Delete snapshots",
					noLabel:     "Keep snapshots",
					destructive: true,
				}
				first.cmd = func() tea.Msg { return askMsg{c: followUp} }
			}
			m.confirm = &first
			return m, nil
		}
		snap := m.curSnapshot()
		del := m.opCmd("delete of snapshot "+snap, "snapshot", "delete", name, snap)
		if kind == "docker" {
			del = m.opCmd("delete of docker snapshot "+snap, "docker", "snapshot", "delete", snap)
		}
		m.confirm = &confirm{
			prompt:      fmt.Sprintf("Delete snapshot %q of %q?", snap, name),
			cmd:         del,
			yesLabel:    "Delete",
			destructive: true,
		}
		return m, nil
	}
	return m, nil
}

// cycleSort switches the snapshot ordering (name ↔ date) and rebuilds the tree.
// Machine order is untouched; the cursor follows the previously selected row —
// the same snapshot when one was selected, otherwise its machine.
func (m model) cycleSort() (tea.Model, tea.Cmd) {
	selMachine := m.curName()
	selSnap := ""
	if r, ok := m.curRow(); ok && r.kind == rowSnapshot {
		selSnap = r.snapName
	}
	if m.sortMode == sortByName {
		m.sortMode = sortByDate
	} else {
		m.sortMode = sortByName
	}
	m.rebuildRows()
	// reposition the cursor on the previously selected snapshot, else its machine
	for i, r := range m.rows {
		if r.machine < 0 || r.machine >= len(m.clusters) || m.clusters[r.machine].Name != selMachine {
			continue
		}
		if selSnap == "" && r.kind == rowMachine {
			m.cur = i
			break
		}
		if selSnap != "" && r.kind == rowSnapshot && r.snapName == selSnap {
			m.cur = i
			break
		}
	}
	if m.cur >= len(m.rows) {
		m.cur = max(0, len(m.rows)-1)
	}
	m.clampScroll()
	return m, nil
}

func (m model) move(delta int) (tea.Model, tea.Cmd) {
	next := m.cur + delta
	if next < 0 || next >= len(m.rows) {
		return m, nil
	}
	prev := m.curName()
	m.cur = next
	// the info panel's net line belongs to the previously selected cluster;
	// blank it until the next refresh recomputes it for the new selection
	if m.curName() != prev {
		m.netLine = ""
		m.netTotalLine = ""
	}
	m.clampScroll()
	return m, nil
}

// expand opens a collapsed machine row, lazy-loading its snapshots if needed.
func (m model) expand() (tea.Model, tea.Cmd) {
	r, ok := m.curRow()
	if !ok || r.kind != rowMachine {
		return m, nil
	}
	c := m.clusters[r.machine]
	if m.expanded[c.Name] {
		return m, nil
	}
	m.expanded[c.Name] = true
	var cmd tea.Cmd
	if _, loaded := m.snapsByMachine[c.Name]; !loaded {
		m.loading[c.Name] = true
		cmd = m.refreshSnapshots(c.Name, c.Kind)
	}
	m.rebuildRows()
	m.clampScroll()
	return m, cmd
}

// collapse closes an expanded machine row, or jumps a snapshot row up to its
// parent machine.
func (m model) collapse() (tea.Model, tea.Cmd) {
	r, ok := m.curRow()
	if !ok {
		return m, nil
	}
	if r.kind == rowSnapshot {
		for i := m.cur - 1; i >= 0; i-- {
			if m.rows[i].kind == rowMachine {
				m.cur = i
				break
			}
		}
		m.clampScroll()
		return m, nil
	}
	c := m.clusters[r.machine]
	if m.expanded[c.Name] {
		m.expanded[c.Name] = false
		m.rebuildRows()
		if m.cur >= len(m.rows) {
			m.cur = max(0, len(m.rows)-1)
		}
	}
	m.clampScroll()
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

// --- command log ---

func (m *model) openLog() {
	m.showLog = true
	m.sizeLog()
}

// sizeLog (re)builds the log viewport for the current terminal size.
func (m *model) sizeLog() {
	w := m.width - 10
	if w > 110 {
		w = 110
	}
	if w < 20 {
		w = 20
	}
	h := m.height - 8
	if h < 5 {
		h = 5
	}
	vp := viewport.New(w, h)
	vp.SetContent(m.logContent())
	m.logVP = vp
}

// logContent renders the whole session's command history, newest first.
func (m model) logContent() string {
	if len(m.commands) == 0 {
		return dimSt.Render("no commands run yet")
	}
	var b strings.Builder
	for i := len(m.commands) - 1; i >= 0; i-- {
		c := m.commands[i]
		status := statusOk.Render("✓")
		if c.err != nil {
			status = statusBad.Render("✗")
		}
		head := "$ k3c " + strings.Join(c.args, " ")
		b.WriteString(keySt.Render(head) + "  " + status + " " + dimSt.Render(c.when.Format("15:04:05")) + "\n")
		if c.output != "" {
			b.WriteString(c.output + "\n")
		}
		if i > 0 {
			b.WriteString(dimSt.Render(strings.Repeat("─", 48)) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- view ---

// Color roles. The defaults are the built-in light-blue theme; applyTheme
// overrides any role from the resolved config. The derived styles below are
// (re)built by rebuildStyles whenever the colors change.
var (
	accent = lipgloss.AdaptiveColor{Light: "#0E86C7", Dark: "#89D7FB"}
	dim    = lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#5C5C5C"}
	good   = lipgloss.AdaptiveColor{Light: "#0F8A4C", Dark: "#42C883"}
	warn   = lipgloss.AdaptiveColor{Light: "#B58A00", Dark: "#E2C04A"}
	cool   = lipgloss.AdaptiveColor{Light: "#0072C6", Dark: "#56B2F2"}
	bad    = lipgloss.AdaptiveColor{Light: "#C5283D", Dark: "#F2637E"}

	// derived styles, (re)built by rebuildStyles from the colors above
	titleSt   lipgloss.Style
	keySt     lipgloss.Style
	dimSt     lipgloss.Style
	selectSt  lipgloss.Style
	statusOk  lipgloss.Style
	statusBad lipgloss.Style
	paneBox   lipgloss.Style
	dialogBox lipgloss.Style
)

func init() { rebuildStyles() }

// rebuildStyles recomputes the derived lipgloss styles from the current color
// roles. Called at init and after applyTheme changes the palette.
func rebuildStyles() {
	titleSt = lipgloss.NewStyle().Bold(true).Foreground(accent)
	keySt = lipgloss.NewStyle().Bold(true).Foreground(cool)
	dimSt = lipgloss.NewStyle().Foreground(dim)
	selectSt = lipgloss.NewStyle().Bold(true).Background(accent).Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1A1A1A"})
	statusOk = lipgloss.NewStyle().Foreground(good)
	statusBad = lipgloss.NewStyle().Foreground(bad)
	paneBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent)
	dialogBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(1, 3)
}

// applyTheme overrides the color roles from the resolved config (an empty
// field keeps the built-in default) and rebuilds the derived styles. The TUI
// is a single instance per process, so a package-level palette is safe.
func applyTheme(t config.UITheme) {
	set := func(dst *lipgloss.AdaptiveColor, v string) {
		if v != "" {
			// a single configured color applies to both light and dark terminals
			*dst = lipgloss.AdaptiveColor{Light: v, Dark: v}
		}
	}
	set(&accent, t.Accent)
	set(&dim, t.Dim)
	set(&good, t.Good)
	set(&warn, t.Warn)
	set(&cool, t.Cool)
	set(&bad, t.Bad)
	rebuildStyles()
}

// dotChar is the uncolored state glyph (used in the selection bar, which can't
// carry per-segment color).
func dotChar(state string) string {
	switch state {
	case "running":
		return "●"
	case "paused":
		return "◐"
	case "suspended":
		return "◌"
	case "stopped":
		return "○"
	default:
		return "·"
	}
}

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

// compactWidth/compactHeight are the smallest terminal the normal side-by-side
// layout renders cleanly in; below either, View switches to the compact,
// scrollable presentation. compactWidth tracks the wide header's natural width
// (info panel beside the menu columns) plus a little slack.
const (
	compactWidth  = 80
	compactHeight = 18
)

// compact reports whether the terminal is too small for the normal layout and
// the stacked, non-wrapping, scrollable presentation should be used instead.
func (m model) compact() bool {
	return m.width < compactWidth || m.height < compactHeight
}

func (m model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	switch {
	case m.showLog:
		return m.logScreen()
	case m.showHelp:
		return m.helpScreen()
	case m.showDiagram:
		return m.diagramScreen()
	case m.confirm != nil:
		return m.confirmScreen()
	case m.input != nil:
		return m.inputScreen()
	case m.rename != nil:
		return m.renameScreen()
	case m.exportPick != nil:
		return m.exportScreen()
	}
	if m.compact() {
		return m.compactView()
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.headerView(), m.treeView(), m.statusView())
}

// compactView is the small-terminal layout: the header fields stacked
// vertically (never side-by-side), a scrolled machine/snapshot list, and the
// status line — every row truncated to the terminal width so nothing wraps.
func (m model) compactView() string {
	header := m.compactHeaderView()
	title := truncate(" "+m.machinesTitle(), m.width)
	list := m.compactListView()
	status := truncate(m.statusView(), m.width)
	// a blank line separates the header from the content, mirroring the gap the
	// wide layout's bordered boxes create.
	return lipgloss.JoinVertical(lipgloss.Left, header, "", title, list, status)
}

// compactHeaderView stacks the info-panel rows and a one-line key hint, each
// truncated to the terminal width. No box border — every column is precious on
// a small terminal.
func (m model) compactHeaderView() string {
	var b strings.Builder
	for _, l := range strings.Split(m.infoPanelView(), "\n") {
		b.WriteString(truncate(l, m.width) + "\n")
	}
	b.WriteString(truncate(m.compactKeyHint(), m.width))
	return b.String()
}

// compactKeyHint condenses the navigation keys and the contextual verbs into a
// single dotted line; it is truncated to width, with the full set still in the
// ? help dialog.
func (m model) compactKeyHint() string {
	binds := append([]helpBind{
		{"↑↓", "move"}, {"←→", "expand"}, {"o", "sort"}, {"l", "logs"}, {"?", "help"}, {"q", "quit"},
	}, m.menuBinds()...)
	parts := make([]string, 0, len(binds))
	for _, b := range binds {
		parts = append(parts, keySt.Render(b.key)+" "+dimSt.Render(b.desc))
	}
	return strings.Join(parts, dimSt.Render(" · "))
}

// listVisible is how many list rows fit below the compact header and above the
// status line. When the list is taller than that, one line is reserved for the
// scroll indicator.
func (m model) listVisible() int {
	// blank separator + title line + status line sit around the list, plus the
	// header block.
	area := m.height - lipgloss.Height(m.compactHeaderView()) - 3
	if area < 1 {
		area = 1
	}
	if len(m.rows) > area && area >= 2 {
		return area - 1
	}
	return area
}

// scrollTop resolves the first visible row for a window of vis rows, biased by
// the persisted listTop but always clamped so the selected row is on screen.
func (m model) scrollTop(vis int) int {
	top := m.listTop
	if top > m.cur {
		top = m.cur
	}
	if m.cur >= top+vis {
		top = m.cur - vis + 1
	}
	if maxTop := len(m.rows) - vis; top > maxTop {
		top = maxTop
	}
	if top < 0 {
		top = 0
	}
	return top
}

// compactListView renders the scrolled, truncated machine/snapshot list.
func (m model) compactListView() string {
	w := m.width
	if len(m.rows) == 0 {
		msg := "no clusters — k3c cluster create"
		if !m.loaded {
			msg = "starting container system…"
		}
		return truncate(" "+dimSt.Render(msg), w)
	}
	vis := m.listVisible()
	top := m.scrollTop(vis)
	end := top + vis
	if end > len(m.rows) {
		end = len(m.rows)
	}
	nameW := m.snapNameWidth()
	var b strings.Builder
	for i := top; i < end; i++ {
		b.WriteString(truncate(m.renderRow(m.rows[i], w, nameW, i == m.cur), w))
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	if ind := scrollIndicator(top, end, len(m.rows)); ind != "" {
		b.WriteString("\n" + truncate(ind, w))
	}
	return b.String()
}

// scrollIndicator notes how many rows are hidden above/below the window, or ""
// when the whole list is visible.
func scrollIndicator(top, end, total int) string {
	var parts []string
	if top > 0 {
		parts = append(parts, fmt.Sprintf("↑ %d more", top))
	}
	if end < total {
		parts = append(parts, fmt.Sprintf("↓ %d more", total-end))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + dimSt.Render(strings.Join(parts, " · "))
}

// clampScroll persists the scroll offset so list movement is sticky between
// frames; the render path re-clamps regardless, so this only affects smoothness.
func (m *model) clampScroll() {
	if !m.compact() {
		m.listTop = 0
		return
	}
	m.listTop = m.scrollTop(m.listVisible())
}

// headerView is the k9s-style top bar: the context info panel beside the
// shortcut menu. The panel is frameless so the header stays compact; the two
// columns top-align directly.
func (m model) headerView() string {
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.infoPanelView(),
		"   ",
		m.keyMenuView(),
	)
}

func panelLabel(s string) string { return dimSt.Render(fmt.Sprintf("%-8s", s)) }

// infoPanelView shows the selected machine plus the contextual net line and the
// global pull-cache line.
func (m model) infoPanelView() string {
	rows := []string{titleSt.Render("k3c") + dimSt.Render(" · machines")}
	if mc, ok := m.curMachine(); ok {
		ctx := mc.Context
		if mc.Kind == "docker" || ctx == "" {
			ctx = "—"
		}
		rows = append(rows,
			panelLabel("machine")+mc.Name+dimSt.Render("  ("+typeLabel(mc.Kind)+" · "+mc.Server+")"),
			panelLabel("context")+ctx,
		)
	}
	net := m.netLine
	if net == "" {
		net = dimSt.Render("—")
	}
	total := m.netTotalLine
	if total == "" {
		total = dimSt.Render("—")
	}
	cache := m.cacheLine
	if cache == "" {
		cache = dimSt.Render("—")
	}
	rows = append(rows, panelLabel("net")+net, panelLabel("total")+total, panelLabel("cache")+cache)
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// keyMenuView renders the always-on navigation column beside the verbs that
// apply to the row under the cursor (machine / snapshot / docker sidecar).
func (m model) keyMenuView() string {
	parts := []string{renderMenuCol([]helpBind{
		{"↑↓", "move"},
		{"←→", "expand"},
		{"o", "sort"},
		{"l", "logs"},
		{"?", "help"},
		{"q", "quit"},
	})}
	ctx := m.menuBinds()
	const per = 6
	for i := 0; i < len(ctx); i += per {
		end := i + per
		if end > len(ctx) {
			end = len(ctx)
		}
		parts = append(parts, "    ", renderMenuCol(ctx[i:end]))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// menuBinds picks the contextual verb set for the current row.
func (m model) menuBinds() []helpBind {
	r, ok := m.curRow()
	if !ok {
		return machineBinds()
	}
	kind := m.clusters[r.machine].Kind
	if kind == "docker" && r.kind == rowMachine {
		return dockerBinds()
	}
	if r.kind == rowSnapshot && r.snapName != "" {
		return snapshotBinds()
	}
	return machineBinds()
}

func renderMenuCol(binds []helpBind) string {
	w := 0
	for _, b := range binds {
		if k := lipgloss.Width(b.key); k > w {
			w = k
		}
	}
	var sb strings.Builder
	for _, b := range binds {
		sb.WriteString(keySt.Render(padRight(b.key, w)) + " " + dimSt.Render(b.desc) + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// typeLabel is the short machine-type tag: clusters are k3s VMs; the sidecar is
// the docker VM.
func typeLabel(kind string) string {
	if kind == "docker" {
		return "docker"
	}
	return "k3s"
}

// machinesTitle is the list pane heading with the current snapshot sort mode.
func (m model) machinesTitle() string {
	return titleSt.Render("Machines") + dimSt.Render("   snapshots "+m.sortMode.label())
}

func (m model) treeView() string {
	w := m.width - 2
	var b strings.Builder
	b.WriteString(" " + m.machinesTitle() + "\n")
	if len(m.rows) == 0 {
		// Until the first refresh returns, the container system may still be
		// starting — don't claim there are no clusters when we haven't looked yet.
		msg := "no clusters — k3c cluster create"
		if !m.loaded {
			msg = "starting container system…"
		}
		b.WriteString(" " + dimSt.Render(msg))
		return paneBox.Width(w).Render(b.String())
	}
	nameW := m.snapNameWidth()
	for i, r := range m.rows {
		b.WriteString(m.renderRow(r, w, nameW, i == m.cur) + "\n")
	}
	return paneBox.Width(w).Render(strings.TrimRight(b.String(), "\n"))
}

// snapNameWidth is the width of the snapshot-name column: the longest visible
// snapshot name, but never below a sensible minimum. Padding every snapshot row
// to this keeps the mode/size/date columns aligned even when one name is longer
// than the default field width.
func (m model) snapNameWidth() int {
	w := 24
	for _, r := range m.rows {
		if r.kind == rowSnapshot && r.snapName != "" {
			if n := lipgloss.Width(r.snapName); n > w {
				w = n
			}
		}
	}
	return w
}

// renderRow draws one tree line; the selected row is a solid bar (uncolored
// glyphs, so the highlight background reads cleanly).
func (m model) renderRow(r treeRow, w, nameW int, selected bool) string {
	if r.kind == rowMachine {
		c := m.clusters[r.machine]
		caret := "▸"
		if m.expanded[c.Name] {
			caret = "▾"
		}
		// the active cluster (the current kube context) is flagged with a
		// trailing ★ on its name — a leading column read as indentation and
		// misaligned the names with the nested snapshot rows
		if selected {
			name := c.Name
			if c.Active {
				name += " ★"
			}
			plain := fmt.Sprintf(" %s %s %-14s %-7s %-9s %6s",
				caret, dotChar(c.Server), name, typeLabel(c.Kind), c.Server, c.RAM)
			return selectSt.Render(padRight(plain, w))
		}
		name := c.Name
		if c.Active {
			name += titleSt.Render(" ★")
		}
		return fmt.Sprintf(" %s %s %s %-7s %-9s %6s",
			dimSt.Render(caret), stateDot(c.Server),
			padRight(name, 14), typeLabel(c.Kind), c.Server, c.RAM)
	}

	const indent = "     "
	if r.placeholder != "" {
		if selected {
			return selectSt.Render(padRight(indent+r.placeholder, w))
		}
		return dimSt.Render(indent + r.placeholder)
	}
	if selected {
		plain := fmt.Sprintf("%s%-*s %-6s %9s  %s", indent, nameW, r.snapName, r.snapMode, r.snapSize, r.snapWhen)
		return selectSt.Render(padRight(plain, w))
	}
	label := fmt.Sprintf("%-6s", r.snapMode)
	mode := dimSt.Render(label)
	switch r.snapMode {
	case "warm":
		mode = lipgloss.NewStyle().Foreground(warn).Render(label)
	case "frozen":
		mode = lipgloss.NewStyle().Foreground(cool).Render(label)
	}
	return fmt.Sprintf("%s%-*s %s %s  %s", indent, nameW, r.snapName, mode,
		dimSt.Render(fmt.Sprintf("%9s", r.snapSize)), dimSt.Render(r.snapWhen))
}

func (m model) statusView() string {
	switch {
	case m.busy != "":
		line := ""
		if m.opLine != "" {
			line = dimSt.Render(" · " + m.opLine)
		}
		return " " + m.spin.View() + " " + m.busy + dimSt.Render(" …") + line
	case m.status != "" && m.failed:
		return statusBad.Render(" ✗ " + m.status + dimSt.Render(" (l shows the command log)"))
	case m.status != "":
		return statusOk.Render(" " + m.status)
	default:
		return dimSt.Render(" ready")
	}
}

// --- dialogs ---

func (m model) center(box string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// fitDialog centers the dialog content in a box sized to preferred content
// width, but never wider than the terminal — on a small screen the box shrinks
// and lipgloss wraps long lines (e.g. a confirm prompt) instead of overflowing.
// preferred == 0 sizes the box to its content. The width passed to the box
// already includes its horizontal padding, so +6 reserves room for it.
func (m model) fitDialog(content string, preferred int) string {
	w := preferred
	if w == 0 {
		w = lipgloss.Width(content) + 6
	}
	if maxW := m.width - 2; w > maxW {
		w = maxW
	}
	return m.center(dialogBox.Width(w).Render(content))
}

// renderButton draws one confirm-dialog button. The focused button is filled
// (accent, or red when it is the destructive affirmative action); an unfocused
// destructive action keeps red text so the consequence reads even before it is
// selected.
func renderButton(label string, focused, destructive bool) string {
	st := lipgloss.NewStyle().Padding(0, 2).Border(lipgloss.RoundedBorder()).Bold(true)
	switch {
	case focused && destructive:
		return st.BorderForeground(bad).Background(bad).
			Foreground(lipgloss.Color("#FFFFFF")).Render(label)
	case focused:
		return st.BorderForeground(accent).Background(accent).
			Foreground(lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1A1A1A"}).Render(label)
	case destructive:
		return st.BorderForeground(bad).Foreground(bad).Render(label)
	default:
		return st.BorderForeground(dim).Foreground(dim).Render(label)
	}
}

func (m model) confirmScreen() string {
	c := m.confirm
	btns := c.buttons()
	rendered := make([]string, 0, len(btns)*2-1)
	for i, b := range btns {
		if i > 0 {
			rendered = append(rendered, "  ")
		}
		rendered = append(rendered, renderButton(b.label, i == c.focus, b.destructive))
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleSt.Render("Confirm"), "",
		c.prompt, "",
		row, "",
		dimSt.Render("← → select · enter confirm · esc cancel"))
	return m.fitDialog(content, 0)
}

func (m model) inputScreen() string {
	in := m.input
	sel := lipgloss.NewStyle().Bold(true).Foreground(accent)
	segs := make([]string, 0, 3)
	for _, md := range in.modes() {
		if md == in.mode {
			segs = append(segs, sel.Render(string(md)))
		} else {
			segs = append(segs, string(md))
		}
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleSt.Render("New snapshot of "+in.cluster), "",
		dimSt.Render("name  ")+in.input.View(),
		dimSt.Render("mode  ")+strings.Join(segs, dimSt.Render(" / ")),
		dimSt.Render("      "+modeDesc(in.mode)), "",
		dimSt.Render("enter save · tab cycle mode · esc cancel · spaces → dashes"))
	return m.fitDialog(content, 64)
}

func (m model) renameScreen() string {
	rn := m.rename
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleSt.Render("Rename snapshot "+rn.oldName), "",
		dimSt.Render("name  ")+rn.input.View(), "",
		dimSt.Render("enter rename · esc cancel · spaces → dashes"))
	return m.fitDialog(content, 64)
}

func (m model) exportScreen() string {
	p := m.exportPick
	sel := lipgloss.NewStyle().Bold(true).Foreground(accent)
	segs := make([]string, 0, 3)
	for _, md := range []cluster.FrozenExportMode{cluster.FrozenSlim, cluster.FrozenFat, cluster.FrozenThin} {
		if md == p.mode {
			segs = append(segs, sel.Render(string(md)))
		} else {
			segs = append(segs, string(md))
		}
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleSt.Render("Export frozen snapshot "+p.snap), "",
		dimSt.Render("file  ")+p.out,
		dimSt.Render("mode  ")+strings.Join(segs, dimSt.Render(" / ")),
		dimSt.Render("      "+exportModeDesc(p.mode)), "",
		dimSt.Render("enter export · tab cycle mode · esc cancel"))
	return m.fitDialog(content, 72)
}

func (m model) logScreen() string {
	title := titleSt.Render(" k3c ") + dimSt.Render("· command log")
	footer := dimSt.Render(" ↑↓ scroll · esc / o close · q quit")
	content := lipgloss.JoinVertical(lipgloss.Left, title, m.logVP.View(), footer)
	return m.center(dialogBox.Render(content))
}

// helpBind is one key/description row in the menu and the help dialog.
type helpBind struct{ key, desc string }

// machineBinds, snapshotBinds and dockerBinds are the single source of truth
// for both the header menu and the help dialog, so the two can't drift.
func machineBinds() []helpBind {
	return []helpBind{
		{"↵", "activate"},
		{"s", "start"},
		{"S", "stop"},
		{"p", "pause"},
		{"r", "resume"},
		{"z", "suspend"},
		{"u", "use-context"},
		{"m", "reclaim mem"},
		{"M", "release mem"},
		{"c", "snapshot"},
		{"d/x", "delete"},
	}
}

func snapshotBinds() []helpBind {
	return []helpBind{
		{"↵", "restore"},
		{"c", "create"},
		{"R", "rename"},
		{"e", "export"},
		{"d/x", "delete"},
	}
}

func dockerBinds() []helpBind {
	return []helpBind{
		{"↵", "activate"},
		{"s", "up"},
		{"S", "down"},
		{"p", "pause"},
		{"r", "resume"},
		{"z", "suspend"},
		{"c", "snapshot"},
		{"d/x", "remove"},
	}
}

// --- system diagram ---

// blockBox is the bordered block used for each component in the diagram.
var blockBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1)

// listenerDot colors a host-daemon listener: up is green, down is red — a
// down listener is the thing the diagram exists to make obvious.
func listenerDot(up bool) string {
	if up {
		return lipgloss.NewStyle().Foreground(good).Render("●")
	}
	return lipgloss.NewStyle().Foreground(bad).Render("○")
}

// stateColor maps a machine state to the color of its status dot, so a block's
// title can echo its symbol: running green, paused yellow, suspended blue,
// stopped/unknown gray.
func stateColor(state string) lipgloss.AdaptiveColor {
	switch state {
	case "running":
		return good
	case "paused":
		return warn
	case "suspended":
		return cool
	default:
		return dim
	}
}

// stateTitle renders a block title bold in its state's color.
func stateTitle(state, text string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(stateColor(state)).Render(text)
}

// colGlyph places a single glyph at column col on an otherwise blank line.
func colGlyph(col int, glyph string) string {
	if col < 0 {
		col = 0
	}
	return strings.Repeat(" ", col) + glyph
}

// diagramScreen renders the k3c system as a data-flow diagram: the host daemon
// and its listeners bridge the guest VMs (the k3s clusters and the docker
// sidecar) that run on the container runtime; image pulls flow through the
// pull-cache. Toggled with D, refreshed on the same tick as the main view.
const diagramMinWidth = 46

func (m model) diagramScreen() string {
	title := titleSt.Render(" k3c ") + dimSt.Render("· system")
	footer := dimSt.Render(" ↑↓ ←→ scroll · D / esc close")

	if m.width < diagramMinWidth {
		body := lipgloss.JoinVertical(lipgloss.Left,
			title, "", dimSt.Render(" resize wider to view the diagram"), "", footer)
		if m.compact() {
			return body
		}
		return m.center(dialogBox.Render(body))
	}

	// rebuild the body from current state every frame so the diagram stays live;
	// the viewport keeps only the scroll offset (set in Update) and dimensions.
	vp := m.diagramVP
	vp.SetContent(m.diagramContent())
	content := lipgloss.JoinVertical(lipgloss.Left, title, "", vp.View(), "", footer)
	if m.compact() {
		// drop the frame on a small screen to reclaim space.
		return content
	}
	return m.center(dialogBox.Render(content))
}

// diagramContent builds the scrollable region of the system diagram: the
// daemon/runtime/VM spine with its connectors, plus the centered legend.
func (m model) diagramContent() string {
	legend := dimSt.Render(" ") + stateDot("running") + dimSt.Render(" running  ") +
		stateDot("paused") + dimSt.Render(" paused  ") +
		stateDot("suspended") + dimSt.Render(" suspended  ") +
		stateDot("stopped") + dimSt.Render(" stopped")

	daemon := m.daemonBlock()
	runtime := m.runtimeBlock()
	vmrow, vmCenters := m.vmRow()

	// The spine — daemon, runtime, VM row — is centered on a single vertical
	// axis J; connectors are drawn at absolute columns so they line up with the
	// box centers. The pull-cache hangs off the runtime to the right and does
	// not move the axis.
	spineW := max(max(lipgloss.Width(daemon), lipgloss.Width(runtime)), lipgloss.Width(vmrow))
	J := spineW / 2
	indent := func(block string) string {
		return lipgloss.NewStyle().PaddingLeft(J - lipgloss.Width(block)/2).Render(block)
	}
	vline := dimSt.Render(colGlyph(J, "│"))

	runtimeRow := indent(runtime)
	if pc := m.pullCacheBlock(); pc != "" {
		runtimeRow = lipgloss.JoinHorizontal(lipgloss.Center, indent(runtime), dimSt.Render(" ──▶ "), pc)
	}

	egressLabel := "⇅ egress · pulls"
	egress := dimSt.Render(colGlyph(J-lipgloss.Width(egressLabel)/2, egressLabel))

	lpVm := J - lipgloss.Width(vmrow)/2
	parts := []string{indent(daemon), vline, egress, vline, runtimeRow}
	parts = append(parts, vmBranch(J, lpVm, vmCenters)...)
	parts = append(parts, indent(vmrow))
	body := lipgloss.JoinVertical(lipgloss.Left, parts...)

	bw := lipgloss.Width(body)
	ctr := func(s string) string { return lipgloss.PlaceHorizontal(bw, lipgloss.Center, s) }
	return lipgloss.JoinVertical(lipgloss.Left, body, "", ctr(legend))
}

func (m *model) openDiagram() {
	m.showDiagram = true
	m.sizeDiagram()
}

// sizeDiagram (re)builds the diagram viewport for the current terminal size so
// the diagram scrolls when it is taller than the screen.
func (m *model) sizeDiagram() {
	content := m.diagramContent()
	w := lipgloss.Width(content)
	avail := m.width
	if !m.compact() {
		avail = m.width - 8 // border (2) + horizontal padding (6)
	}
	if w > avail {
		w = avail
	}
	if w < 10 {
		w = 10
	}
	// title + 2 blank separators + footer sit around the body (4 lines); the
	// framed layout also spends the box border + vertical padding (4 more).
	h := m.height - 4
	if !m.compact() {
		h -= 4
	}
	if h < 3 {
		h = 3
	}
	vp := viewport.New(w, h)
	// the diagram is fixed-width art that can be wider than a small terminal, so
	// allow scrolling left/right as well as up/down.
	vp.SetHorizontalStep(6)
	vp.SetContent(content)
	m.diagramVP = vp
}

// vmBranch draws the connector from the runtime down to the VM boxes: a single
// drop when there is one box, or a tee that splits to each box center. centers
// are columns within the VM row; lpVm is the row's left offset on the spine.
func vmBranch(J, lpVm int, centers []int) []string {
	abs := make([]int, len(centers))
	for i, c := range centers {
		abs[i] = lpVm + c
	}
	if len(abs) <= 1 {
		return []string{dimSt.Render(colGlyph(J, "│")), dimSt.Render(colGlyph(J, "▼"))}
	}
	lo, hi := abs[0], abs[len(abs)-1]
	row := []rune(strings.Repeat(" ", hi+1))
	for x := lo; x <= hi; x++ {
		row[x] = '─'
	}
	for _, c := range abs {
		row[c] = '┬'
	}
	row[lo], row[hi] = '┌', '┐'
	if J >= lo && J <= hi {
		if row[J] == '┬' {
			row[J] = '┼'
		} else {
			row[J] = '┴'
		}
	}
	arrow := []rune(strings.Repeat(" ", hi+1))
	for _, c := range abs {
		arrow[c] = '▼'
	}
	return []string{
		dimSt.Render(colGlyph(J, "│")),
		dimSt.Render(string(row)),
		dimSt.Render(string(arrow)),
	}
}

// daemonBlock renders the host daemon process and its listeners.
func (m model) daemonBlock() string {
	d := m.daemons
	lines := []string{
		stateDot(d.State) + " " + stateTitle(d.State, "host daemon"),
		dimSt.Render(fmt.Sprintf("%s · pid %s", d.State, d.Pid)),
	}
	if len(d.Listeners) == 0 {
		lines = append(lines, dimSt.Render("no listeners"))
	}
	for _, l := range d.Listeners {
		row := listenerDot(l.Up) + " " + dimSt.Render(fmt.Sprintf("%-11s :%-5s", l.Name, l.Port))
		if l.Detail != "" {
			row += " " + dimSt.Render(l.Detail)
		}
		lines = append(lines, row)
	}
	return blockBox.BorderForeground(stateColor(d.State)).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

// runtimeBlock renders the Apple container runtime layer. It has no direct
// health probe, so its state reflects whether it is actively hosting VMs.
func (m model) runtimeBlock() string {
	state := "unknown"
	switch {
	case m.anyRunning():
		state = "running"
	case len(m.clusters) > 0:
		state = "stopped"
	}
	return blockBox.BorderForeground(stateColor(state)).Render(lipgloss.JoinVertical(lipgloss.Left,
		stateDot(state)+" "+stateTitle(state, "container runtime"),
		dimSt.Render("apple Virtualization.framework")))
}

// pullCacheBlock renders the host pull-through cache, or "" when it is not a
// configured listener.
func (m model) pullCacheBlock() string {
	up, enabled := false, false
	for _, l := range m.daemons.Listeners {
		if l.Name == "pull-cache" {
			enabled, up = true, l.Up
		}
	}
	if !enabled {
		return ""
	}
	state := "stopped"
	if up {
		state = "running"
	}
	lines := []string{listenerDot(up) + " " + stateTitle(state, "pull-cache")}
	if m.cacheLine != "" {
		lines = append(lines, dimSt.Render(m.cacheLine))
	} else {
		lines = append(lines, dimSt.Render("no pulls yet"))
	}
	border := lipgloss.AdaptiveColor(good)
	if !up {
		border = bad
	}
	return blockBox.BorderForeground(border).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

// vmRow renders one block per guest VM (clusters and the docker sidecar), side
// by side when wide and stacked when narrow, and returns the column of each
// box's center within the row so the branch connector can line up with them.
func (m model) vmRow() (string, []int) {
	blocks := make([]string, 0, len(m.clusters))
	for _, c := range m.clusters {
		blocks = append(blocks, m.vmBlock(c))
	}
	if len(blocks) == 0 {
		s := dimSt.Render("no machines")
		return s, []int{lipgloss.Width(s) / 2}
	}
	if m.width < 80 {
		col := lipgloss.JoinVertical(lipgloss.Center, blocks...)
		return col, []int{lipgloss.Width(col) / 2}
	}
	const gap = 2
	row := make([]string, 0, len(blocks)*2-1)
	centers := make([]int, 0, len(blocks))
	x := 0
	for i, b := range blocks {
		if i > 0 {
			row = append(row, strings.Repeat(" ", gap))
			x += gap
		}
		w := lipgloss.Width(b)
		centers = append(centers, x+w/2)
		row = append(row, b)
		x += w
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, row...), centers
}

func (m model) vmBlock(c cluster.ClusterInfo) string {
	kind := "k3s"
	if c.Kind == "docker" {
		kind = "docker sidecar"
	}
	lines := []string{
		stateDot(c.Server) + " " + stateTitle(c.Server, c.Name),
		dimSt.Render(kind + " · " + c.Server),
	}
	if c.RAM != "" {
		lines = append(lines, dimSt.Render("mem "+c.RAM))
	}
	if c.Kind != "docker" && c.Context != "" {
		lines = append(lines, dimSt.Render("ctx "+c.Context))
	}
	return blockBox.BorderForeground(stateColor(c.Server)).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

// anyRunning reports whether any managed machine is running.
func (m model) anyRunning() bool {
	for _, c := range m.clusters {
		if c.Server == "running" {
			return true
		}
	}
	return false
}

// helpScreen renders the full keybinding reference dialog (toggled with ?).
func (m model) helpScreen() string {
	title := titleSt.Render(" k3c ") + dimSt.Render("· keybindings")
	footer := dimSt.Render(" ↑↓ scroll · ? / esc close")
	content := lipgloss.JoinVertical(lipgloss.Left, title, "", m.helpVP.View(), "", footer)
	if m.compact() {
		// drop the frame on a small screen to reclaim every column.
		return content
	}
	return m.center(dialogBox.Render(content))
}

// helpBody is the scrollable keybinding reference. The sections are packed into
// as many columns as fit width — a side-by-side grid on a wide terminal,
// collapsing to fewer columns (down to one) as it narrows.
func (m model) helpBody(width int) string {
	sections := []string{
		helpCol("GENERAL", []helpBind{
			{"↑↓ / jk", "move"},
			{"←→", "expand / collapse"},
			{"o", "sort snapshots (name / date)"},
			{"g / F5", "refresh"},
			{"l", "logs / output"},
			{"D", "system diagram"},
			{"? / esc", "close help"},
			{"q / ^C", "quit"},
		}),
		helpCol("MACHINE", machineBinds()),
		helpCol("SNAPSHOT", snapshotBinds()),
		helpCol("DOCKER SIDECAR", dockerBinds()),
	}
	return packColumns(sections, width)
}

// packColumns lays out blocks left-to-right in rows of as many uniform-width
// columns as fit within width, separated by a gap, with a blank line between
// rows.
func packColumns(cols []string, width int) string {
	const gap = "    "
	gapW := lipgloss.Width(gap)
	colW := 0
	for _, c := range cols {
		if w := lipgloss.Width(c); w > colW {
			colW = w
		}
	}
	n := 1
	if colW > 0 {
		n = (width + gapW) / (colW + gapW)
	}
	if n < 1 {
		n = 1
	}
	if n > len(cols) {
		n = len(cols)
	}
	var rows []string
	for i := 0; i < len(cols); i += n {
		end := i + n
		if end > len(cols) {
			end = len(cols)
		}
		parts := make([]string, 0, (end-i)*2-1)
		for j := i; j < end; j++ {
			if j > i {
				parts = append(parts, gap)
			}
			parts = append(parts, cols[j])
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, parts...))
	}
	return strings.Join(rows, "\n\n")
}

func (m *model) openHelp() {
	m.showHelp = true
	m.sizeHelp()
}

// helpContentWidth is the width available to the help body: the full terminal
// when frameless (compact), otherwise the space inside the box border+padding.
func (m model) helpContentWidth() int {
	if m.compact() {
		return m.width
	}
	return m.width - 8
}

// sizeHelp (re)builds the help viewport for the current terminal size so the
// keybinding reference scrolls when it is taller than the screen and re-packs
// its columns to the available width.
func (m *model) sizeHelp() {
	avail := m.helpContentWidth()
	body := m.helpBody(avail)
	w := lipgloss.Width(body)
	if w > avail {
		w = avail
	}
	if w < 10 {
		w = 10
	}
	// title + 2 blank separators + footer sit around the body (4 lines); the
	// framed layout also spends the box border + vertical padding (4 more).
	h := m.height - 4
	if !m.compact() {
		h -= 4
	}
	if h < 3 {
		h = 3
	}
	vp := viewport.New(w, h)
	vp.SetContent(body)
	m.helpVP = vp
}

func helpCol(title string, binds []helpBind) string {
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

func padRight(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// truncate clips s to at most w display columns, appending an ellipsis when it
// has to cut. It is ANSI/width aware, so styled rows are measured by their
// visible width rather than byte length and never wrap onto a second line.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
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
