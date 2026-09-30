package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type screen int

const (
	scrBrowse screen = iota
	scrSettings
	scrProgress
	scrSummary
	scrHelp
)

// ----------------------------------------------------------------- messages

type scanMsg struct {
	res scanned
}

type probeMsg struct {
	path string
	meta Meta
}

type gatherMsg struct {
	paths []string
	size  int64
}

type dirStatMsg struct {
	path  string
	size  int64
	count int
}

type runEventMsg struct{ ev Event }

type runDoneMsg struct{}

type tickMsg time.Time

type statusMsg struct {
	text  string
	isErr bool
}

type toolsMsg struct {
	ffmpeg, ffprobe, cwebp bool
}

// -------------------------------------------------------------------- model

type Model struct {
	screen   screen
	prevScr  screen
	width    int
	height   int
	ready    bool
	quitting bool

	dir     string
	entries []Entry
	visible []int
	cursor  int
	top     int
	loading bool
	loadErr error

	sel      map[string]bool
	selSizes map[string]int64
	selOrder []string

	sort   sortMode
	filter string

	filterInput textinput.Model
	pathInput   textinput.Model
	editing     int // 0 none, 1 filter, 2 path

	settings   Settings
	setCursor  int
	showHidden bool

	jobs    []*Job
	runner  *Runner
	overall float64
	spin    int
	viewTop int

	status    string
	statusErr bool
	statusTTL int

	// askingReplace is set while the y/n "replace the originals?" question is
	// on screen. It swallows every other key until the question is answered so
	// a stray letter can never start a run the user did not confirm.
	askingReplace bool

	tools     toolsMsg
	probeBusy map[string]bool

	// dirStats caches "how much media is under this folder". Walking a tree
	// is far too slow to do inside View(), so it happens off the UI thread.
	dirStats   map[string]dirStat
	dirStatAge time.Time
	statBusy   map[string]bool
}

type dirStat struct {
	size  int64
	count int
	seen  time.Time
}

func NewModel(start string, args []string) Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter"
	ti.CharLimit = 80

	pi := textinput.New()
	pi.Prompt = "▸ "
	pi.Placeholder = "/path/to/output"
	pi.CharLimit = 200

	if start == "" {
		if wd, err := os.Getwd(); err == nil {
			start = wd
		} else {
			start = homeDir()
		}
	}
	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}
	if st, err := os.Stat(start); err == nil && !st.IsDir() {
		start = filepath.Dir(start)
	}

	m := Model{
		screen:      scrBrowse,
		dir:         start,
		sel:         map[string]bool{},
		selSizes:    map[string]int64{},
		filterInput: ti,
		pathInput:   pi,
		settings:    defaultSettings(),
		probeBusy:   map[string]bool{},
		dirStats:    map[string]dirStat{},
		statBusy:    map[string]bool{},
	}
	skip := m.settings.outputSubdir()
	for _, a := range args {
		files, err := collectMedia(a, skip)
		if err != nil {
			m.status = "skipping " + shortPath(a) + ": " + err.Error()
			m.statusErr = true
			m.statusTTL = 60
			continue
		}
		for _, f := range files {
			m.addSelected(f)
		}
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.scanCmd(m.dir), tickCmd(), checkTools())
}

func checkTools() tea.Cmd {
	return func() tea.Msg {
		return toolsMsg{ffmpeg: lookPath("ffmpeg"), ffprobe: lookPath("ffprobe"), cwebp: lookPath("cwebp")}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) scanCmd(dir string) tea.Cmd {
	cache := metaCache
	return func() tea.Msg {
		list, err := listDir(dir, cache)
		return scanMsg{res: scanned{dir: dir, list: list, err: err}}
	}
}

func (m Model) gatherCmd(roots []string) tea.Cmd {
	return func() tea.Msg {
		seen := map[string]bool{}
		var paths []string
		var size int64
		skip := m.settings.outputSubdir()
		for _, r := range roots {
			files, err := collectMedia(r, skip)
			if err != nil {
				continue
			}
			for _, f := range files {
				if seen[f] {
					continue
				}
				seen[f] = true
				paths = append(paths, f)
				if st, err := os.Stat(f); err == nil {
					size += st.Size()
				}
			}
		}
		sort.Strings(paths)
		return gatherMsg{paths: paths, size: size}
	}
}

func (m Model) probeCmd(path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return probeMsg{path: path, meta: probe(ctx, path)}
	}
}

// ------------------------------------------------------------------ updates

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil

	case tickMsg:
		m.spin++
		if m.statusTTL > 0 {
			m.statusTTL--
			if m.statusTTL == 0 {
				m.status, m.statusErr = "", false
			}
		}
		if m.screen == scrProgress {
			m.recomputeOverall()
		}
		return m, m.tick()

	case toolsMsg:
		m.tools = msg
		return m, nil

	case statusMsg:
		m.status, m.statusErr, m.statusTTL = msg.text, msg.isErr, 50
		return m, nil

	case scanMsg:
		if msg.res.dir != m.dir {
			return m, nil
		}
		m.loading = false
		m.dirStatAge = time.Now()
		if msg.res.err != nil {
			m.loadErr = msg.res.err
			return m, nil
		}
		m.loadErr = nil
		m.entries = msg.res.list
		m.cursor = 0
		m.top = 0
		m.reindex()
		return m, tea.Batch(m.probeVisible(), m.wantDirStat())

	case probeMsg:
		metaCache.put(msg.path, msg.meta)
		delete(m.probeBusy, msg.path)
		for i := range m.entries {
			if m.entries[i].Path == msg.path {
				m.entries[i].Meta = msg.meta
			}
		}
		return m, m.probeVisible()

	case dirStatMsg:
		delete(m.statBusy, msg.path)
		m.dirStats[msg.path] = dirStat{size: msg.size, count: msg.count, seen: time.Now()}
		return m, nil

	case gatherMsg:
		allSel := true
		for _, p := range msg.paths {
			if !m.sel[p] {
				allSel = false
			}
		}
		for _, p := range msg.paths {
			m.addSelectedSize(p, msg.size, len(msg.paths))
		}
		m.say(fmt.Sprintf("%s %d file(s) · %s", map[bool]string{true: "deselected", false: "selected"}[allSel], len(msg.paths), humanSize(msg.size)), false)
		return m, nil

	case runEventMsg:
		return m.applyEvent(msg.ev)

	case runDoneMsg:
		m.applyRunDone()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) say(text string, isErr bool) {
	m.status, m.statusErr, m.statusTTL = text, isErr, 50
}

func (m Model) tick() tea.Cmd { return tickCmd() }

func (m *Model) recomputeOverall() {
	if len(m.jobs) == 0 {
		m.overall = 0
		return
	}
	var sum float64
	for _, j := range m.jobs {
		switch j.State {
		case JobDone, JobSkipped, JobFailed, JobCanceled:
			sum++
		default:
			sum += j.Percent
		}
	}
	m.overall = sum / float64(len(m.jobs))
}

func (m *Model) applyEvent(ev Event) (tea.Model, tea.Cmd) {
	if ev.Index < 0 || ev.Index >= len(m.jobs) {
		return m, nil
	}
	m.jobs[ev.Index].apply(ev)
	m.recomputeOverall()
	return m, m.drainRunner()
}

func (m *Model) drainRunner() tea.Cmd {
	if m.runner == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := m.runner.Next()
		if !ok {
			return runDoneMsg{}
		}
		return runEventMsg{ev: ev}
	}
}

func (m *Model) applyRunDone() {
	if m.runner != nil {
		for i, j := range m.jobs {
			if j.State == JobQueued || j.State == JobRunning {
				m.jobs[i].State = JobCanceled
			}
		}
	}
	m.recomputeOverall()
	m.screen = scrSummary
	m.viewTop = 0
}

// --------------------------------------------------------------- selection

func (m *Model) addSelected(p string) {
	if m.sel[p] {
		return
	}
	m.sel[p] = true
	m.selOrder = append(m.selOrder, p)
	if st, err := os.Stat(p); err == nil {
		m.selSizes[p] = st.Size()
	}
}

func (m *Model) addSelectedSize(p string, _ int64, _ int) {
	m.addSelected(p)
}

func (m *Model) removeSelected(p string) {
	if !m.sel[p] {
		return
	}
	delete(m.sel, p)
	delete(m.selSizes, p)
	for i, v := range m.selOrder {
		if v == p {
			m.selOrder = append(m.selOrder[:i], m.selOrder[i+1:]...)
			break
		}
	}
}

func (m *Model) toggle(p string) {
	if m.sel[p] {
		m.removeSelected(p)
	} else {
		m.addSelected(p)
	}
}

func (m *Model) clearSelection() {
	m.sel = map[string]bool{}
	m.selSizes = map[string]int64{}
	m.selOrder = nil
}

func (m *Model) selectedTotal() (int, int64) {
	var total int64
	for _, p := range m.selOrder {
		total += m.selSizes[p]
	}
	return len(m.selOrder), total
}

func (m *Model) selectedPaths() []string {
	out := make([]string, 0, len(m.selOrder))
	out = append(out, m.selOrder...)
	return out
}

func (m *Model) countByKind() (int, int) {
	imgs, vids := 0, 0
	for p := range m.sel {
		switch kindOf(p) {
		case KindImage:
			imgs++
		case KindVideo:
			vids++
		}
	}
	return imgs, vids
}

// ------------------------------------------------------------- list helpers

func (m *Model) reindex() {
	sortEntries(m.entries, m.sort)
	needle := strings.ToLower(strings.TrimSpace(m.filter))
	m.visible = m.visible[:0]
	for i := range m.entries {
		e := m.entries[i]
		if needle != "" && !e.IsUp() && !strings.Contains(strings.ToLower(e.Name), needle) {
			continue
		}
		m.visible = append(m.visible, i)
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
	m.clampScroll()
}

func (m *Model) cur() (Entry, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return Entry{}, false
	}
	return m.entries[m.visible[m.cursor]], true
}

func (m Model) listHeight() int {
	h := m.height - 6
	if h < 3 {
		h = 3
	}
	return h
}

func (m *Model) scrollWindow() int {
	return m.listHeight()
}

func (m *Model) clampScroll() {
	win := m.scrollWindow()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+win {
		m.top = m.cursor - win + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

func (m *Model) move(delta int) {
	if len(m.visible) == 0 {
		return
	}
	m.cursor = clampInt(m.cursor+delta, 0, len(m.visible)-1)
	m.clampScroll()
}

func (m *Model) jump(delta int) {
	m.move(delta * m.scrollWindow())
}

func (m *Model) probeVisible() tea.Cmd {
	var cmds []tea.Cmd
	pending := 0
	for _, idx := range m.visible {
		if pending >= 8 {
			break
		}
		e := m.entries[idx]
		if e.Dir || !isMedia(e.Kind) || e.Meta.Probed {
			continue
		}
		if m.probeBusy[e.Path] {
			continue
		}
		m.probeBusy[e.Path] = true
		pending++
		cmds = append(cmds, m.probeCmd(e.Path))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// ------------------------------------------------------------------- input

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		if m.screen == scrProgress {
			if m.runner != nil {
				m.runner.Cancel()
				m.say("cancelling…", false)
			}
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	}

	if m.askingReplace {
		return m.resolveReplacePrompt(key)
	}

	switch m.editing {
	case 1:
		return m.updateFilterInput(msg)
	case 2:
		return m.updatePathInput(msg)
	}

	switch m.screen {
	case scrBrowse:
		return m.updateBrowse(key)
	case scrSettings:
		return m.updateSettings(key, msg)
	case scrProgress:
		return m.updateProgress(key)
	case scrSummary:
		return m.updateSummary(key)
	case scrHelp:
		m.screen = scrBrowse
		return m, nil
	}
	return m, nil
}

func (m Model) updateFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = 0
		m.filter = ""
		m.filterInput.Blur()
		m.reindex()
		return m, m.probeVisible()
	case "enter":
		m.editing = 0
		m.filterInput.Blur()
		m.filter = m.filterInput.Value()
		m.reindex()
		return m, m.probeVisible()
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	m.filter = m.filterInput.Value()
	m.reindex()
	return m, tea.Batch(cmd, m.probeVisible())
}

func (m Model) updatePathInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = 0
		m.pathInput.Blur()
		return m, nil
	case "enter":
		m.settings.OutPath = strings.TrimSpace(m.pathInput.Value())
		m.settings.Output = OutputCustom
		m.editing = 0
		m.pathInput.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.pathInput, cmd = m.pathInput.Update(msg)
	return m, cmd
}

func (m Model) updateBrowse(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		if len(m.sel) > 0 {
			m.say("selection kept · press "+keycap("shift+q")+" to quit with a selection", false)
			m.statusTTL = 30
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit

	case "Q":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.filterInput.SetValue("")
			m.reindex()
			return m, m.probeVisible()
		}
		if len(m.sel) > 0 {
			m.clearSelection()
			return m, nil
		}
		m.say("press "+keycap("q")+" to quit", false)
		return m, nil

	case "up", "k":
		m.move(-1)
		return m, m.wantDirStat()
	case "down", "j":
		m.move(1)
		return m, m.wantDirStat()
	case "pgup":
		m.jump(-1)
		return m, m.wantDirStat()
	case "pgdown":
		m.jump(1)
		return m, m.wantDirStat()
	case "home", "g":
		m.cursor = 0
		m.clampScroll()
		return m, m.wantDirStat()
	case "end", "G":
		m.cursor = max(0, len(m.visible)-1)
		m.clampScroll()
		return m, m.wantDirStat()

	case "right", "l", "enter":
		e, ok := m.cur()
		if !ok {
			return m, nil
		}
		if e.Dir {
			return m.enterDir(e.Path)
		}
		m.toggle(e.Path)
		return m, nil

	case "left", "h", "backspace":
		return m.enterDir(filepath.Dir(m.dir))

	case " ":
		e, ok := m.cur()
		if !ok {
			return m, nil
		}
		if e.Dir {
			return m, m.gatherCmd([]string{e.Path})
		}
		m.toggle(e.Path)
		return m, nil

	case "a":
		return m, m.gatherCmd([]string{m.dir})

	case "n":
		m.clearSelection()
		m.say("selection cleared", false)
		return m, nil

	case "x":
		for _, idx := range m.visible {
			if m.sel[m.entries[idx].Path] {
				m.removeSelected(m.entries[idx].Path)
			}
		}
		m.say("deselected everything in view", false)
		return m, nil

	case "/":
		m.editing = 1
		m.filterInput.SetValue(m.filter)
		m.filterInput.CursorEnd()
		return m, m.filterInput.Focus()

	case "c":
		return m.askReplace()

	case "S":
		return m.startRun()

	case "s":
		if len(m.sel) == 0 {
			m.say("select something first (press "+keycap("space")+")", true)
			return m, nil
		}
		m.prevScr = scrBrowse
		m.screen = scrSettings
		m.setCursor = 0
		return m, nil

	case "ctrl+r":
		m.loading = true
		return m, m.scanCmd(m.dir)

	case "o":
		openInFileManager(m.dir)
		return m, nil

	case "?":
		m.prevScr = m.screen
		m.screen = scrHelp
		return m, nil

	case "H":
		m.showHidden = !m.showHidden
		m.loading = true
		return m, m.scanCmd(m.dir)
	}
	return m, nil
}

func (m Model) enterDir(dir string) (tea.Model, tea.Cmd) {
	if dir == m.dir {
		return m, nil
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return m, nil
	}
	m.dir = dir
	m.cursor = 0
	m.top = 0
	m.loading = true
	m.filter = ""
	m.filterInput.SetValue("")
	return m, m.scanCmd(dir)
}

// --------------------------------------------------------------- run / jobs

// askReplace puts the y/n question on screen. It is what "c" triggers: rather
// than silently picking an output mode, the user is asked whether the run
// should overwrite the original videos.
func (m Model) askReplace() (tea.Model, tea.Cmd) {
	if len(m.sel) == 0 {
		m.say("nothing selected — press "+keycap("space")+" on a file or folder", true)
		return m, nil
	}
	m.askingReplace = true
	m.status, m.statusErr, m.statusTTL = "", false, 0
	return m, nil
}

// resolveReplacePrompt handles the answer to askReplace. "y" runs with
// OutputReplaceVideo, "n" keeps the current output mode, and esc/q backs out
// without running anything.
func (m Model) resolveReplacePrompt(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y":
		m.askingReplace = false
		m.settings.Output = OutputReplaceVideo
		return m.startRun()
	case "n", "N", "enter":
		m.askingReplace = false
		return m.startRun()
	case "esc", "q", "backspace":
		m.askingReplace = false
		m.say("cancelled — nothing was compressed", false)
		return m, nil
	}
	return m, nil
}

func (m Model) startRun() (tea.Model, tea.Cmd) {
	if len(m.sel) == 0 {
		m.say("nothing selected — press "+keycap("space")+" on a file or folder", true)
		return m, nil
	}
	if !m.tools.ffmpeg {
		m.say("ffmpeg not found on PATH — install it first", true)
		return m, nil
	}
	if m.settings.ImageFormat().Name == "webp" && !m.tools.cwebp {
		m.say("cwebp not found on PATH — pick a different image format", true)
		return m, nil
	}

	imgs, vids := m.countByKind()
	if !m.settings.AlsoImages {
		imgs = 0
	}
	if !m.settings.AlsoVideos {
		vids = 0
	}
	if imgs+vids == 0 {
		m.say("nothing to process with the current "+keycap("Process")+" setting", true)
		return m, nil
	}

	paths := m.selectedPaths()
	jobs := make([]*Job, 0, len(paths))
	for _, p := range paths {
		k := kindOf(p)
		if k == KindImage && !m.settings.AlsoImages {
			continue
		}
		if k == KindVideo && !m.settings.AlsoVideos {
			continue
		}
		jobs = append(jobs, &Job{
			Source: p,
			Kind:   k,
			Before: m.selSizes[p],
			State:  JobQueued,
		})
	}
	if len(jobs) == 0 {
		m.say("nothing to process", true)
		return m, nil
	}

	// Remember metadata already gathered while browsing.
	for i := range m.entries {
		if !m.entries[i].Meta.Probed {
			continue
		}
		if metaCache.get2(m.entries[i].Path) {
			continue
		}
		metaCache.put(m.entries[i].Path, m.entries[i].Meta)
	}

	m.status, m.statusErr, m.statusTTL = "", false, 0
	m.jobs = jobs
	m.runner = NewRunner(jobs, m.settings)
	m.overall = 0
	m.viewTop = 0
	m.screen = scrProgress
	m.spin = 0

	runner := m.runner
	startCmd := func() tea.Msg {
		runner.Run(context.Background())
		return nil
	}
	return m, tea.Batch(startCmd, m.drainRunner())
}

// ----------------------------------------------------------------- settings

func (m Model) updateSettings(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	row := setRows[clampInt(m.setCursor, 0, len(setRows)-1)]

	switch key {
	case "esc", "q", "s":
		m.screen = scrBrowse
		return m, nil
	case "up", "k":
		m.setCursor = wrap(m.setCursor-1, len(setRows))
		return m, nil
	case "down", "j":
		m.setCursor = wrap(m.setCursor+1, len(setRows))
		return m, nil
	case "left", "h", "-":
		m.settings.adjust(row.field, -1)
		return m, nil
	case "right", "l", "+", "=":
		m.settings.adjust(row.field, 1)
		return m, nil
	case "home":
		m.setCursor = 0
		return m, nil
	case "end":
		m.setCursor = len(setRows) - 1
		return m, nil
	case "r":
		m.settings = defaultSettings()
		m.say("settings reset", false)
		return m, nil
	case "e":
		if row.field == fOutPath {
			m.editing = 2
			m.pathInput.SetValue(m.settings.OutPath)
			m.pathInput.CursorEnd()
			return m, m.pathInput.Focus()
		}
		m.say("only "+keycap("Custom path")+" accepts typed text", false)
		return m, nil
	case "enter", "S":
		return m.startRun()
	case "c":
		return m.askReplace()
	case "?":
		m.prevScr = scrSettings
		m.screen = scrHelp
		return m, nil
	}
	return m, nil
}

// ----------------------------------------------------------------- progress

func (m Model) updateProgress(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.viewTop = max(0, m.viewTop-1)
		return m, nil
	case "down", "j":
		if m.viewTop < max(0, len(m.jobs)-m.progressHeight()) {
			m.viewTop++
		}
		return m, nil
	case "esc", "q":
		if m.runner != nil && !m.runner.IsFinished() {
			m.runner.Cancel()
			m.say("cancelling…", false)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) progressHeight() int {
	h := m.height - 8
	if h < 3 {
		h = 3
	}
	return h
}

// ------------------------------------------------------------------ summary

func (m Model) updateSummary(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "enter", "q":
		m.screen = scrBrowse
		m.clearSelection()
		m.loading = true
		return m, m.scanCmd(m.dir)
	case "o":
		if dir := m.summaryDir(); dir != "" {
			openInFileManager(dir)
		}
		return m, nil
	}
	return m, nil
}

// summaryDir returns the folder to offer in Finder. When the jobs wrote into
// more than one folder, the deepest common parent is used so the user lands
// somewhere that actually contains the results.
func (m Model) summaryDir() string {
	common := ""
	for _, j := range m.jobs {
		switch j.State {
		case JobDone, JobSkipped:
		default:
			continue
		}
		d := filepath.Dir(j.Target)
		if common == "" {
			common = d
			continue
		}
		common = commonAncestor(common, d)
	}
	if common != "" {
		return common
	}
	if len(m.jobs) > 0 {
		return m.settings.outputDirFor(m.jobs[0].Source, m.jobs[0].Kind)
	}
	return ""
}

// commonAncestor returns the deepest directory that contains both paths, or
// the filesystem root when they share nothing.
func commonAncestor(a, b string) string {
	as, bs := splitPath(a), splitPath(b)
	n := min(len(as), len(bs))
	i := 0
	for i < n && as[i] == bs[i] {
		i++
	}
	if i == 0 {
		return string(filepath.Separator)
	}
	return string(filepath.Separator) + strings.Join(as[:i], string(filepath.Separator))
}

// splitPath breaks an absolute path into its components, dropping the root.
func splitPath(p string) []string {
	p = filepath.Clean(p)
	rest := strings.TrimPrefix(p, string(filepath.Separator))
	if rest == "" || rest == "." {
		return nil
	}
	return strings.Split(rest, string(filepath.Separator))
}

// wantDirStat kicks off a background media count for the folder under the
// cursor, so the info panel never has to block on a directory walk.
func (m *Model) wantDirStat() tea.Cmd {
	e, ok := m.cur()
	if !ok || !e.Dir {
		return nil
	}
	if st, seen := m.dirStats[e.Path]; seen && time.Since(st.seen) < 30*time.Second {
		return nil
	}
	if m.statBusy[e.Path] {
		return nil
	}
	m.statBusy[e.Path] = true
	path := e.Path
	return func() tea.Msg {
		size, count := dirSizeOf(path)
		return dirStatMsg{path: path, size: size, count: count}
	}
}
