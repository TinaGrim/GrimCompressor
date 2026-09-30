package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	colAccent  = lipgloss.AdaptiveColor{Light: "#0b6bcb", Dark: "#7cc4ff"}
	colDim     = lipgloss.AdaptiveColor{Light: "#8a8a8a", Dark: "#6c6c6c"}
	colGood    = lipgloss.AdaptiveColor{Light: "#0a7a3d", Dark: "#5fd18b"}
	colWarn    = lipgloss.AdaptiveColor{Light: "#a35a00", Dark: "#f0b45f"}
	colBad     = lipgloss.AdaptiveColor{Light: "#b3261e", Dark: "#ff8a80"}
	colSelBg   = lipgloss.AdaptiveColor{Light: "#dbeafe", Dark: "#1f3a5f"}
	colHeadBg  = lipgloss.AdaptiveColor{Light: "#eef0f3", Dark: "#23262b"}
	colBorder  = lipgloss.AdaptiveColor{Light: "#c9ced6", Dark: "#3a3f47"}
	colBarFull = lipgloss.AdaptiveColor{Light: "#0a7a3d", Dark: "#5fd18b"}
	colBarPart = lipgloss.AdaptiveColor{Light: "#0b6bcb", Dark: "#7cc4ff"}
)

var (
	styTitle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	styDim   = lipgloss.NewStyle().Foreground(colDim)
	styGood  = lipgloss.NewStyle().Foreground(colGood).Bold(true)
	styWarn  = lipgloss.NewStyle().Foreground(colWarn)
	styBad   = lipgloss.NewStyle().Foreground(colBad)
	styBold  = lipgloss.NewStyle().Bold(true)
	styKey   = lipgloss.NewStyle().Bold(true).Foreground(colAccent)

	styHeader  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(colHeadBg)
	styRowCur  = lipgloss.NewStyle().Background(colSelBg)
	styBorder  = lipgloss.NewStyle().Foreground(colBorder)
	styPanelTi = lipgloss.NewStyle().Foreground(colDim)
)

func styAccentDir(s string) string {
	return lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render(s)
}
func styAccentIcon(s string) string { return lipgloss.NewStyle().Foreground(colAccent).Render(s) }

func dim(s string) string    { return styDim.Render(s) }
func bold(s string) string   { return styBold.Render(s) }
func accent(s string) string { return styTitle.Render(s) }

func keycap(k string) string { return styKey.Render(k) }

func shortPath(p string) string {
	home := homeDir()
	if strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// elidePath keeps the informative tail of a long path and drops the head.
func elidePath(p string, width int) string {
	r := []rune(p)
	if len(r) <= width || width < 6 {
		return clip(p, width)
	}
	keep := width - 1
	// Prefer cutting at a separator so the tail reads as a real path.
	tail := string(r[len(r)-keep:])
	if i := strings.IndexByte(tail, '/'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	return "…/" + tail
}

// clip shortens s to width, appending an ellipsis when it had to cut.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

// pad right-pads accounting for ANSI escapes and wide runes.
func pad(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func padLeft(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
}

func iconFor(k Kind) string {
	switch k {
	case KindDir:
		return "▸"
	case KindImage:
		return "▣"
	case KindVideo:
		return "▶"
	default:
		return "·"
	}
}

func bar(fraction float64, width int) string {
	if width < 2 {
		return ""
	}
	inner := width - 2
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(fraction*float64(inner) + 0.5)
	full := strings.Repeat("█", filled)
	partN := 0
	rem := fraction*float64(inner) - float64(filled)
	if rem > 0.55 && filled < inner {
		partN = 1
	}
	part := ""
	if partN == 1 {
		part = "▌"
	}
	rest := inner - filled - len(part)
	if rest < 0 {
		rest = 0
	}
	return "[" + lipgloss.NewStyle().Foreground(colBarFull).Render(full+part) +
		lipgloss.NewStyle().Foreground(colBorder).Render(strings.Repeat("░", rest)) + "]"
}

func spinner(frame int) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[frame%len(frames)]
}

// wrapWords breaks a long line on spaces so nothing important is clipped away.
func wrapWords(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	cur := words[0]
	for _, w := range words[1:] {
		if lipgloss.Width(cur)+1+lipgloss.Width(w) <= width {
			cur += " " + w
			continue
		}
		out = append(out, cur)
		cur = w
	}
	return append(out, cur)
}
