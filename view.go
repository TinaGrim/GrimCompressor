package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func lookPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func openInFileManager(dir string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	_ = cmd.Start()
}

func (m Model) View() string {
	if m.quitting || !m.ready {
		return ""
	}
	switch m.screen {
	case scrSettings:
		return m.viewSettings()
	case scrProgress:
		return m.viewProgress()
	case scrSummary:
		return m.viewSummary()
	case scrHelp:
		return m.viewHelp()
	default:
		return m.viewBrowse()
	}
}

// ------------------------------------------------------------------ chrome

func (m Model) header(title string, right string) string {
	w := m.width
	if w < 20 {
		w = 20
	}
	left := " " + accent("▐▛ ") + title + " "
	r := styDim.Render(" " + right + " ")
	gap := w - lipgloss.Width(left) - lipgloss.Width(r)
	if gap < 1 {
		gap = 1
	}
	return styHeader.Render(pad(left, lipgloss.Width(left))) +
		strings.Repeat(" ", gap) + r
}

func rule(w int) string {
	if w < 1 {
		return ""
	}
	return styBorder.Render(strings.Repeat("─", w))
}

// replacePrompt is the y/n question "c" raises. Videos are replaced in place,
// images go to the compressed subfolder, so the wording says which is which.
func (m Model) replacePrompt() string {
	return " " + styWarn.Render("Replace the original videos with the compressed ones after encoding?  ") +
		keycap("y") + styDim.Render(" yes   ") + keycap("n") + styDim.Render(" no (images → compressed/)   ") +
		keycap("esc") + styDim.Render(" cancel")
}

func (m Model) statusLine() string {
	if m.status == "" {
		return ""
	}
	if m.statusErr {
		return " " + styBad.Render("✗ ") + m.status
	}
	return " " + styGood.Render("✓ ") + m.status
}

func (m Model) footer(keys [][2]string) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, keycap(k[0])+" "+styDim.Render(k[1]))
	}
	line := " " + strings.Join(parts, styDim.Render(" · "))
	return line
}

func (m Model) panel(title string, body []string) string {
	w := m.detailWidth()
	if w < 24 {
		return strings.Join(append([]string{styPanelTi.Render(" " + title)}, body...), "\n")
	}
	head := styPanelTi.Render(" " + title + " ")
	content := append([]string{head}, body...)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBorder).
		Padding(0, 1).
		Width(w).
		Render(strings.Join(content, "\n"))
}

// minDetail is the narrowest the info panel is allowed to get. Below that the
// list takes the whole terminal and the panel is dropped.
const minDetail = 32

// twoPane reports whether there is room for a list plus an info panel.
func (m Model) twoPane() bool { return m.width >= 72 }

// listWidth is how many columns the file list gets. It is the single source of
// truth for the split; detailWidth derives from it.
func (m Model) listWidth() int {
	if !m.twoPane() {
		return max(20, m.width)
	}
	w := m.width * 45 / 100
	w = clampInt(w, 38, 64)
	if room := m.width - minDetail - 1; w > room {
		w = room
	}
	return max(20, w)
}

// detailWidth is the info panel's *content* width, or 0 when it is not shown.
// panelFrame is what the rounded border and padding add on top of that.
func (m Model) detailWidth() int {
	if !m.twoPane() {
		return 0
	}
	w := m.width - m.listWidth() - 1 - panelFrame
	if w < minDetail {
		return 0
	}
	return w
}

// panelFrame is the border + padding overhead of a rounded panel.
const panelFrame = 4

// ------------------------------------------------------------------- browse

func (m Model) viewBrowse() string {
	var b strings.Builder

	right := ""
	if imgs, vids := m.countByKind(); imgs+vids > 0 {
		right = fmt.Sprintf("%d image(s) · %d video(s)", imgs, vids)
	} else {
		right = "nothing selected"
	}
	b.WriteString(m.header("Compress TUI", right))
	b.WriteString("\n")
	b.WriteString(m.pathBar())
	b.WriteString("\n")

	listW := m.listWidth()
	rows := m.renderList(listW)

	if m.twoPane() {
		detail := m.renderDetail()
		rows = joinColumns(rows, listW, detail)
	}
	for _, r := range rows {
		b.WriteString(r)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.selBar())
	if m.askingReplace {
		b.WriteString("\n")
		b.WriteString(m.replacePrompt())
	} else if s := m.statusLine(); s != "" {
		b.WriteString("\n")
		b.WriteString(s)
	}
	b.WriteString("\n")
	if m.askingReplace {
		b.WriteString(m.footer([][2]string{{"y", "replace videos"}, {"n", "keep originals"}, {"esc", "cancel"}}))
	} else {
		b.WriteString(m.footer(m.browseKeyHelp()))
	}
	return b.String()
}

var browseKeys = [][2]string{
	{"↑↓", "move"}, {"⏎", "open/select"}, {"space", "select"}, {"a", "all here"},
	{"/", "filter"}, {"s", "settings"}, {"c", "compress"}, {"S", "start as-is"}, {"?", "help"}, {"q", "quit"},
}

// On a narrow terminal we only keep the keys people cannot guess.
var browseKeysNarrow = [][2]string{
	{"↑↓", "move"}, {"⏎", "open"}, {"␣", "select"}, {"a", "all"},
	{"/", "find"}, {"c", "compress"}, {"?", "?"}, {"q", "quit"},
}

func (m Model) browseKeyHelp() [][2]string {
	if m.width < 78 {
		return browseKeysNarrow
	}
	return browseKeys
}

func (m Model) pathBar() string {
	var line string
	room := m.width - 24
	if m.editing == 1 {
		line = " " + accent(shortPath(m.dir)) + "  " + m.filterInput.View()
	} else {
		line = " " + accent(elidePath(shortPath(m.dir), max(24, room)))
		if m.filter != "" {
			line += "  " + styWarn.Render("filter: "+clip(m.filter, 20))
		}
	}
	if m.loading {
		line += "  " + styDim.Render(spinner(m.spin)+" scanning…")
	}
	if m.loadErr != nil {
		line += "  " + styBad.Render(m.loadErr.Error())
	}
	if len(m.visible) == 0 && !m.loading {
		line += "  " + styDim.Render("(nothing matches — H shows dotfiles)")
	}
	return line
}

func (m Model) renderList(w int) []string {
	if len(m.visible) == 0 {
		return []string{styDim.Render("  nothing here")}
	}
	win := m.listHeight()
	rows := make([]string, 0, win)
	end := min(m.top+win, len(m.visible))

	markerW := 3
	sizeW := 9
	iconW := 1
	// " " + marker + " " + icon + " " is 7 columns, then name, size and meta
	// are separated by single spaces.
	const gutters = 4
	metaW := 0
	if w >= 52 {
		metaW = clampInt(w/3, 10, 24)
	}
	nameW := w - (1 + markerW + 1 + iconW + 1) - gutters - sizeW - metaW
	if nameW < 8 {
		metaW = 0
		nameW = max(8, w-(1+markerW+1+iconW+1)-gutters-sizeW)
	}

	for i := m.top; i < end; i++ {
		e := m.entries[m.visible[i]]
		cur := i == m.cursor

		marker := "   "
		switch {
		case m.sel[e.Path]:
			marker = styGood.Render("[x]")
		case e.Dir:
			marker = " " + styDim.Render("[+]")
		case isMedia(e.Kind):
			marker = styDim.Render("[ ]")
		}

		icon := iconFor(e.Kind)
		var iconStyled string
		switch {
		case e.Dir:
			iconStyled = styAccentDir(icon)
		case e.Kind == KindVideo:
			iconStyled = styWarn.Render(icon)
		case e.Kind == KindImage:
			iconStyled = styAccentIcon(icon)
		default:
			iconStyled = styDim.Render(icon)
		}

		name := clip(e.Name, nameW)
		if cur {
			name = styBold.Render(name)
		}
		size := ""
		if !e.Dir {
			size = padLeft(humanSize(e.Size), sizeW)
		}
		meta := ""
		if metaW > 0 && !e.Dir {
			meta = clip(e.metaSummary(), metaW)
			meta = padLeft(styDim.Render(meta), metaW)
		}

		line := " " + marker + " " + iconStyled + " " + pad(name, nameW) + " " + padLeft(size, sizeW)
		if metaW > 0 {
			line += " " + padLeft(styDim.Render(meta), metaW)
		}
		if cur {
			line = styRowCur.Width(w).Render(line)
		}
		rows = append(rows, pad(line, w))
	}

	// scroll indicators
	if m.top > 0 && len(rows) > 0 {
		rows[0] = " " + styDim.Render("▲ more")
	}
	if end < len(m.visible) && len(rows) > 0 {
		rows[len(rows)-1] = " " + styDim.Render("▼ more")
	}
	return rows
}

func (e Entry) metaSummary() string {
	switch e.Kind {
	case KindImage:
		if e.Meta.Resolution() != "" {
			return e.Meta.Resolution()
		}
		return "image"
	case KindVideo:
		var parts []string
		if e.Meta.Resolution() != "" {
			parts = append(parts, e.Meta.Resolution())
		}
		if d := e.Meta.DurationString(); d != "" {
			parts = append(parts, d)
		}
		if len(parts) == 0 {
			return "video"
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

// joinColumns lays the file list and the info panel side by side. Every row is
// padded to the same width so the panel border stays a clean vertical line.
func joinColumns(left []string, leftW int, right string) []string {
	rightLines := strings.Split(right, "\n")
	rightW := 0
	for _, l := range rightLines {
		if w := lipgloss.Width(l); w > rightW {
			rightW = w
		}
	}
	n := max(len(left), len(rightLines))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		var b strings.Builder
		b.WriteString(pad(l, leftW))
		if r != "" {
			b.WriteString(" ")
			b.WriteString(pad(r, rightW))
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}

func (m Model) renderDetail() string {
	e, ok := m.cur()
	if !ok {
		return m.panel("details", []string{styDim.Render("nothing selected")})
	}

	kv := func(k, v string) string {
		return styDim.Render(pad(k, 14)) + clip(v, m.detailWidth()-16)
	}

	var body []string
	body = append(body, kv("name", e.Name))
	if e.Dir {
		body = append(body, kv("type", "folder"), kv("path", e.Path))
		if st, ok := m.dirStats[e.Path]; ok {
			if st.count > 0 {
				body = append(body, kv("contains", fmt.Sprintf("%d media · %s", st.count, humanSize(st.size))))
			} else {
				body = append(body, kv("contains", "no images or videos"))
			}
		} else {
			body = append(body, kv("contains", spinner(m.spin)+" "+styDim.Render("counting…")))
		}
		body = append(body, "", styDim.Render("press "+keycap("space")+" to add every image and video inside"))
		return m.panel("folder", body)
	}

	body = append(body, kv("type", e.Kind.String()))
	body = append(body, kv("size", humanSize(e.Size)+"  ("+itoa(int(e.Size))+" bytes)"))
	if !e.Meta.Failed {
		if r := e.Meta.Resolution(); r != "" {
			body = append(body, kv("resolution", r))
		}
		if d := e.Meta.DurationString(); d != "" {
			body = append(body, kv("duration", d))
		}
		if e.Meta.Codec != "" {
			body = append(body, kv("video codec", e.Meta.Codec))
		}
		if f := e.Meta.FPSString(); f != "" {
			body = append(body, kv("frame rate", f))
		}
		if e.Meta.HasAudio {
			body = append(body, kv("audio", "yes"))
		} else if e.Kind == KindVideo {
			body = append(body, kv("audio", "no"))
		}
		if e.Meta.Bitrate > 0 {
			body = append(body, kv("bitrate", humanSize(e.Meta.Bitrate)+"/s"))
		}
	} else {
		body = append(body, kv("metadata", styWarn.Render("could not read")))
	}
	body = append(body, kv("modified", e.ModAt.Format("2006-01-02 15:04")))

	if isMedia(e.Kind) {
		preview := m.previewFor(e)
		if preview != "" {
			body = append(body, "", styDim.Render("will become"))
			for _, l := range strings.Split(preview, "\n") {
				body = append(body, "  "+clip(l, m.detailWidth()-4))
			}
		}
	}
	if m.sel[e.Path] {
		body = append(body, "", styGood.Render("✓ in the batch"))
	}
	return m.panel("details", body)
}

func (m Model) previewFor(e Entry) string {
	s := m.settings
	_, longEdge := s.ResolutionOpt()
	if e.Kind == KindImage {
		orig := "original size"
		if longEdge > 0 {
			if e.Meta.LongEdge() > longEdge {
				orig = fmt.Sprintf("long edge → %dpx", longEdge)
			} else {
				orig = "unchanged (already small)"
			}
		}
		return fmt.Sprintf("%s · quality %d · %s", s.ImageFormat().Label, s.Quality, orig)
	}
	orig := "original size"
	if longEdge > 0 && e.Meta.LongEdge() > longEdge {
		orig = fmt.Sprintf("long edge → %dpx", longEdge)
	}
	return fmt.Sprintf("%s · CRF %d · %s preset · %s",
		s.VideoFormat().Label, crfFromQuality(s.Quality), s.PresetName(), orig)
}

func (m Model) selBar() string {
	count, size := m.selectedTotal()
	if count == 0 {
		hint := "press " + keycap("space") + " on a file, or " + keycap("a") + " for every image/video here"
		if m.width < 78 {
			hint = keycap("space") + " select · " + keycap("a") + " all in folder"
		}
		return " " + styDim.Render(hint)
	}
	imgs, vids := m.countByKind()
	parts := []string{}
	if imgs > 0 {
		parts = append(parts, fmt.Sprintf("%d image(s)", imgs))
	}
	if vids > 0 {
		parts = append(parts, fmt.Sprintf("%d video(s)", vids))
	}
	s := styBold.Render(fmt.Sprintf("%d selected", count)) + styDim.Render(" · "+strings.Join(parts, " · ")+" · "+humanSize(size))
	if m.width < 78 {
		return " " + s + "  " + keycap("c") + styDim.Render(" compress   ") + keycap("s") + styDim.Render(" settings")
	}
	return " " + s + "   " + keycap("c") + styDim.Render(" compress   ") + keycap("S") + styDim.Render(" start as-is   ") + keycap("s") + styDim.Render(" settings")
}

// ----------------------------------------------------------------- settings

var settingsKeys = [][2]string{
	{"↑↓", "row"}, {"←→", "change"}, {"e", "edit path"}, {"r", "reset"},
	{"enter", "start"}, {"c", "compress"}, {"esc", "back"}, {"q", "quit"},
}

func (m Model) viewSettings() string {
	var b strings.Builder
	count, size := m.selectedTotal()
	imgs, vids := m.countByKind()

	right := fmt.Sprintf("%d selected · %s", count, humanSize(size))
	b.WriteString(m.header("Settings", right))
	b.WriteString("\n\n")

	listW := 46
	if m.width < listW+34 {
		listW = max(30, m.width/2)
	}
	leftW := m.width - listW - 4
	if leftW < 24 {
		leftW = 24
	}

	var rows []string
	for i, r := range setRows {
		cur := i == m.setCursor
		label := pad(r.label, 18)
		var val string
		if cur && m.editing == 2 && r.field == fOutPath {
			val = m.pathInput.View()
		} else {
			val = m.settings.valueOf(r.field)
		}
		arrow := " "
		if cur {
			arrow = accent("▌")
		}
		line := arrow + label
		switch {
		case cur && !(m.editing == 2 && r.field == fOutPath):
			line += accent("‹ ") + bold(val) + accent(" ›")
		case cur:
			line += val
		default:
			line += styDim.Render(clip(val, listW-24))
		}
		if cur {
			line = styRowCur.Width(listW).Render(line)
		} else {
			line = pad(line, listW)
		}
		rows = append(rows, line)
	}

	// quality bar
	qbar := m.qualityBar(listW - 22)
	rows = append(rows, "  "+qbar)

	rightPane := m.settingsInfo(imgs, vids, leftW)
	for _, r := range joinColumns(rows, listW, rightPane) {
		b.WriteString(r)
		b.WriteString("\n")
	}
	if m.askingReplace {
		b.WriteString(m.replacePrompt())
	} else if s := m.statusLine(); s != "" {
		b.WriteString(s)
	}
	b.WriteString("\n")
	if m.askingReplace {
		b.WriteString(m.footer([][2]string{{"y", "replace videos"}, {"n", "keep originals"}, {"esc", "cancel"}}))
	} else {
		b.WriteString(m.footer(settingsKeys))
	}
	return b.String()
}

func (m Model) qualityBar(w int) string {
	if w < 10 {
		return ""
	}
	inner := w - 2
	filled := int(float64(clampInt(m.settings.Quality, 0, 100)) / 100 * float64(inner))
	full := strings.Repeat("█", filled)
	rest := strings.Repeat("░", max(0, inner-filled))
	return "[" + styGood.Render(full) + styDim.Render(rest) + "]"
}

func (m Model) settingsInfo(imgs, vids, w int) string {
	s := m.settings
	var b []string
	b = append(b, styPanelTi.Render(" This batch"))
	b = append(b, "")
	row := func(k, v string) {
		b = append(b, styDim.Render(pad(k, 15))+clip(v, max(10, w-17)))
	}
	row("images", fmt.Sprintf("%d", imgs))
	row("videos", fmt.Sprintf("%d", vids))
	if s.Output == OutputAlongside {
		row("output", "same folder as the original")
	} else if s.Output == OutputReplace {
		row("output", "replaces the original")
	} else if s.Output == OutputReplaceVideo {
		row("output", "replaces videos, images → compressed/")
	} else if s.Output == OutputCustom {
		p := s.OutPath
		if p == "" {
			p = "(not set — falls back to source folder)"
		}
		row("output", expandHome(p))
	} else {
		row("output", "<source folder>/compressed/")
	}
	row("existing", s.OverwriteLabel())
	row("parallel", fmt.Sprintf("%d at a time", s.parallel()))
	b = append(b, "")

	b = append(b, styPanelTi.Render(" Example command"))
	b = append(b, "")
	for _, l := range strings.Split(m.exampleCommand(imgs > 0, vids > 0), "\n") {
		for _, wrapped := range wrapWords(l, max(20, w-4)) {
			b = append(b, "  "+styDim.Render(wrapped))
		}
	}
	b = append(b, "")

	b = append(b, styPanelTi.Render(" Needs"))
	b = append(b, "")
	need := func(name string, ok bool) {
		mark := styGood.Render("✓")
		if !ok {
			mark = styBad.Render("✗")
		}
		b = append(b, "  "+mark+" "+name)
	}
	need("ffmpeg", m.tools.ffmpeg)
	need("ffprobe", m.tools.ffprobe)
	if s.ImageFormat().Name == "webp" {
		need("cwebp (for WebP images)", m.tools.cwebp)
	}
	return strings.Join(b, "\n")
}

func (m Model) exampleCommand(withImage, withVideo bool) string {
	s := m.settings
	_, longEdge := s.ResolutionOpt()
	if withVideo || !withImage {
		src := "/path/to/clip.mov"
		r := &Runner{settings: s}
		return "ffmpeg " + strings.Join(r.ffmpegArgs(src, "out"+s.VideoFormat().Ext, KindVideo, Meta{Width: 3840, Height: 2160, Duration: 60e9, HasAudio: true}), " ")
	}
	src := "/path/to/photo.jpg"
	if s.ImageFormat().Name == "webp" {
		r := &Runner{settings: s}
		return "cwebp " + strings.Join(r.cwebpArgs(src, "out.webp", Meta{Width: 4032, Height: 3024}), " ")
	}
	r := &Runner{settings: s}
	_ = longEdge
	return "ffmpeg " + strings.Join(r.ffmpegArgs(src, "out"+s.ImageFormat().Ext, KindImage, Meta{Width: 4032, Height: 3024}), " ")
}

// ----------------------------------------------------------------- progress

var progressKeys = [][2]string{
	{"ctrl+c", "cancel"}, {"↑↓", "scroll"},
}

func (m Model) viewProgress() string {
	var b strings.Builder
	done, failed := 0, 0
	for _, j := range m.jobs {
		switch j.State {
		case JobDone, JobSkipped:
			done++
		case JobFailed, JobCanceled:
			failed++
		}
	}
	right := fmt.Sprintf("%d/%d finished", done, len(m.jobs))
	if failed > 0 {
		right += fmt.Sprintf(" · %d failed", failed)
	}
	b.WriteString(m.header("Compressing", right))
	b.WriteString("\n")
	b.WriteString(" " + bar(m.overall, min(50, m.width-24)) + " " +
		styBold.Render(fmt.Sprintf("%3.0f%%", m.overall*100)) + "  " + m.elapsedLine())
	b.WriteString("\n\n")

	win := m.progressHeight()
	if m.viewTop > max(0, len(m.jobs)-win) {
		m.viewTop = max(0, len(m.jobs)-win)
	}
	end := min(m.viewTop+win, len(m.jobs))

	nameW := clampInt(m.width-52, 14, 48)
	barW := clampInt(m.width/6, 8, 20)

	for i := m.viewTop; i < end; i++ {
		j := m.jobs[i]
		state := styDim.Render("·")
		pct := 0.0
		switch j.State {
		case JobRunning:
			state = accent(spinner(m.spin))
			pct = j.Percent
		case JobDone:
			state = styGood.Render("✓")
			pct = 1
		case JobSkipped:
			state = styWarn.Render("≡")
			pct = 1
		case JobFailed:
			state = styBad.Render("✗")
		case JobCanceled:
			state = styDim.Render("⊘")
		}
		detail := j.Log
		switch j.State {
		case JobDone:
			detail = fmt.Sprintf("%s → %s  (%s smaller)",
				humanSize(j.Before), humanSize(j.After),
				trimPercent(j.SavedPercent()))
		case JobSkipped:
			detail = "already exists, skipped"
		case JobFailed:
			detail = j.Err
		case JobCanceled:
			detail = "canceled"
		}
		if lipgloss.Width(detail) > m.width-nameW-barW-10 {
			detail = clip(detail, max(8, m.width-nameW-barW-10))
		}
		b.WriteString(" " + state + " " + pad(clip(filepath_Base(j.Source), nameW), nameW) +
			" " + bar(pct, barW) + " " + styDim.Render(detail) + "\n")
	}

	if s := m.statusLine(); s != "" {
		b.WriteString(s)
		b.WriteString("\n")
	}
	b.WriteString(m.footer(progressKeys))
	return b.String()
}

func filepath_Base(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func trimPercent(f float64) string {
	if f < 0 {
		f = 0
	}
	return fmt.Sprintf("%.0f%%", f)
}

func (m Model) elapsedLine() string {
	if len(m.jobs) == 0 {
		return ""
	}
	var first, last time.Time
	for _, j := range m.jobs {
		if j.started.IsZero() {
			continue
		}
		if first.IsZero() || j.started.Before(first) {
			first = j.started
		}
		if j.ended.After(last) {
			last = j.ended
		}
	}
	if first.IsZero() {
		return styDim.Render("starting…")
	}
	if last.IsZero() {
		last = time.Now()
	}
	el := last.Sub(first)
	eta := ""
	if m.overall > 0.02 && m.overall < 1 {
		eta = styDim.Render(fmt.Sprintf("  ·  ~%s left", humanDuration(time.Duration(float64(el)/m.overall)-el)))
	}
	return styDim.Render("elapsed "+humanDuration(el)) + eta
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Round(time.Second).Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
}

// ------------------------------------------------------------------ summary

func (m Model) viewSummary() string {
	var b strings.Builder

	// Only completed work may appear in the savings maths; a canceled or
	// failed job has no meaningful output size.
	var inBytes, outBytes int64
	okCount, skipped, failed, canceled := 0, 0, 0, 0
	var problems []*Job
	for _, j := range m.jobs {
		switch j.State {
		case JobDone:
			okCount++
			inBytes += j.Before
			outBytes += j.After
		case JobSkipped:
			skipped++
			inBytes += j.Before
			outBytes += j.After
		case JobFailed:
			failed++
			problems = append(problems, j)
		case JobCanceled:
			canceled++
			problems = append(problems, j)
		}
	}

	title := "Done"
	if failed > 0 || canceled > 0 {
		title = "Finished with problems"
	}
	right := fmt.Sprintf("%d of %d written", okCount, len(m.jobs))
	if skipped > 0 {
		right += fmt.Sprintf(" · %d skipped", skipped)
	}
	b.WriteString(m.header(title, right))
	b.WriteString("\n\n")

	if inBytes > 0 {
		line := " " + styBold.Render(humanSize(inBytes)) + styDim.Render(" → ") + styBold.Render(humanSize(outBytes))
		if outBytes < inBytes {
			saved := inBytes - outBytes
			pct := float64(saved) / float64(inBytes) * 100
			line += "   " + styGood.Render("saved "+humanSize(saved)+"  ("+trimPercent(pct)+")")
		} else {
			line += "   " + styWarn.Render("no size saved")
		}
		b.WriteString(line + "\n")
	}

	if failed+canceled > 0 {
		b.WriteString(" " + styDim.Render(fmt.Sprintf("%d failed · %d canceled · %d written",
			failed, canceled, okCount)) + "\n")
	}
	b.WriteString("\n")

	const maxNotes = 8
	if len(problems) > 0 {
		b.WriteString(" " + styPanelTi.Render("Notes") + "\n")
		for i, j := range problems {
			if i == maxNotes {
				b.WriteString("   " + styDim.Render(fmt.Sprintf("… and %d more", len(problems)-maxNotes)) + "\n")
				break
			}
			mark := styBad.Render("✗")
			switch j.State {
			case JobSkipped:
				mark = styWarn.Render("≡")
			case JobCanceled:
				mark = styDim.Render("⊘")
			}
			note := j.Err
			if note == "" {
				note = j.State.String()
			}
			b.WriteString("   " + mark + " " +
				pad(clip(baseName(j.Source), 38), 38) +
				styDim.Render(clip(note, max(10, m.width-48))) + "\n")
		}
		b.WriteString("\n")
	}

	if out := m.summaryDir(); out != "" {
		b.WriteString(" " + styDim.Render("output: ") + clip(shortPath(out), m.width-12) + "\n")
		b.WriteString(" " + styDim.Render("press ") + keycap("o") + styDim.Render(" to open it in "+fileManagerName()) + "\n")
	}

	b.WriteString(m.footer([][2]string{{"enter", "back to files"}, {"o", "open folder"}, {"q", "quit"}}))
	return b.String()
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, filepath.Separator); i >= 0 {
		return p[i+1:]
	}
	return p
}

func fileManagerName() string {
	switch runtime.GOOS {
	case "darwin":
		return "Finder"
	case "windows":
		return "Explorer"
	default:
		return "the file manager"
	}
}

// --------------------------------------------------------------------- help

func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(m.header("Help", "press any key"))
	b.WriteString("\n\n")

	sections := [][2]string{
		{"Move around", ""},
		{"↑ ↓ / k j", "move one line"},
		{"PgUp PgDn", "jump a page"},
		{"g / G", "first / last item"},
		{"→ / l / enter", "open a folder, or select the file under the cursor"},
		{"← / h / backspace", "go to the parent folder"},
		{"/", "filter the list by name"},
		{"H", "show / hide dotfiles"},
		{"o", "open the current folder in the file manager"},
		{"", ""},
		{"Choose files", ""},
		{"space", "select the item — on a folder it selects every image/video inside, recursively"},
		{"a", "select every image/video in this folder, recursively"},
		{"x", "deselect everything currently visible"},
		{"n / esc", "clear the whole selection"},
		{"", ""},
		{"Compress", ""},
		{"c", "ask whether to replace the original videos, then start compressing"},
		{"S", "start compressing right now with the current settings, no question"},
		{"s", "review or change the settings first, then start with enter"},
		{"", ""},
		{"While running", ""},
		{"ctrl+c", "cancel everything (partial files are removed)"},
		{"↑ ↓", "scroll the job list"},
		{"", ""},
		{"Other", ""},
		{"?", "this help"},
		{"q", "quit"},
		{"ctrl+r", "rescan the current folder"},
	}

	for _, s := range sections {
		if s[1] == "" {
			if s[0] == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString("   " + accent(s[0]) + "\n")
			continue
		}
		b.WriteString("   " + pad(keycap(s[0]), 24) + styDim.Render(s[1]) + "\n")
	}
	b.WriteString("\n" + m.footer([][2]string{{"esc", "back"}}))
	return b.String()
}
