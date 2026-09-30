package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

var version = "1.0.0"

func main() {
	var (
		showVersion = flag.Bool("v", false, "print version and exit")
		checkOnly   = flag.Bool("check", false, "report available encoders and exit")
		dirFlag     = flag.String("d", "", "directory to open (default: current directory)")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("compress-tui", version)
		return
	}
	if *checkOnly {
		printToolReport()
		return
	}

	wd, err := os.Getwd()
	if err != nil {
		wd = homeDir()
	}

	// -d wins; otherwise the first positional argument decides where to start.
	// A directory argument only sets the location; named files get preselected.
	start := expandHome(strings.TrimSpace(*dirFlag))
	args := flag.Args()
	if start == "" {
		switch {
		case len(args) > 0:
			start = expandHome(args[0])
			args = args[1:]
		default:
			start = wd
		}
	}
	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}
	st, err := os.Stat(start)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compress-tui: %s: %v\n", start, err)
		os.Exit(1)
	}
	if !st.IsDir() {
		// Opening a file means "open its folder, with that file selected".
		args = append([]string{start}, args...)
		start = filepath.Dir(start)
	}

	// Preselect only explicit file arguments, never whole directories.
	var preselect []string
	for _, a := range args {
		if a == "" {
			continue
		}
		if s, err := os.Stat(expandHome(a)); err == nil && !s.IsDir() {
			preselect = append(preselect, expandHome(a))
		}
	}

	m := NewModel(start, preselect)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "compress-tui:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `%s — browse, select and compress images and videos from a terminal.

usage:
  compress-tui [directory]
  compress-tui file1.mov folder/ photo.jpg

flags:
  -d dir    open this directory first
  -check    report which encoders are available, then exit
  -v        print version

keys:
  space select · enter open · a select all in folder · / filter
  s settings · c compress · ? help · q quit
`, version)
}

func printToolReport() {
	fmt.Printf("compress-tui %s\n\n", version)
	row := func(name, why string, ok bool) {
		mark := "missing"
		if ok {
			mark = "ok"
		}
		fmt.Printf("  %-7s %-10s %s\n", name, mark, why)
	}
	row("ffmpeg", "video + jpeg/png", lookPath("ffmpeg"))
	row("ffprobe", "metadata (resolution, duration)", lookPath("ffprobe"))
	row("cwebp", "webp images", lookPath("cwebp"))

	if lookPath("ffmpeg") {
		fmt.Println("\n  ffmpeg encoders used by this app:")
		out, _ := runCapture("ffmpeg", "-hide_banner", "-encoders")
		for _, l := range strings.Split(out, "\n") {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "V") {
				continue
			}
			fields := strings.Fields(l)
			if len(fields) < 2 {
				continue
			}
			name := fields[1]
			switch {
			case strings.Contains(name, "264"), strings.Contains(name, "265"),
				strings.Contains(name, "av1"), strings.Contains(name, "vp9"),
				strings.Contains(name, "webp"), name == "mjpeg", name == "png":
				fmt.Println("    " + name)
			}
		}
	}
}

func runCapture(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}
