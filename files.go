package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxCollect = 20000

type Entry struct {
	Path  string
	Name  string
	Dir   bool
	Kind  Kind
	Size  int64
	ModAt time.Time
	Meta  Meta
}

type scanned struct {
	dir  string
	list []Entry
	err  error
}

func (e Entry) IsUp() bool { return e.Dir && e.Name == ".." }

func dirSizeOf(p string) (int64, int) {
	var total int64
	count := 0
	filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if isMedia(kindOf(d.Name())) {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
			count++
		}
		return nil
	})
	return total, count
}

func listDir(dir string, cache *probeCache) ([]Entry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(ents)+1)
	out = append(out, Entry{Path: filepath.Dir(dir), Name: "..", Dir: true, Kind: KindDir})

	for _, de := range ents {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if de.IsDir() {
			e := Entry{Path: full, Name: name, Dir: true, Kind: KindDir}
			if info, err := de.Info(); err == nil {
				e.ModAt = info.ModTime()
			}
			out = append(out, e)
			continue
		}
		if de.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		k := kindOf(name)
		e := Entry{Path: full, Name: name, Kind: k, Size: info.Size(), ModAt: info.ModTime()}
		if m, ok := cache.get(full); ok {
			e.Meta = m
		}
		out = append(out, e)
	}
	return out, nil
}

type sortMode int

const (
	sortName sortMode = iota
	sortSize
	sortDate
	sortKind
)

func (s sortMode) String() string {
	switch s {
	case sortSize:
		return "size"
	case sortDate:
		return "date"
	case sortKind:
		return "kind"
	default:
		return "name"
	}
}

func sortEntries(list []Entry, mode sortMode) {
	less := func(a, b Entry) bool {
		if a.IsUp() != b.IsUp() {
			return a.IsUp()
		}
		if a.Dir != b.Dir {
			return a.Dir
		}
		switch mode {
		case sortSize:
			if a.Size != b.Size {
				return a.Size > b.Size
			}
		case sortDate:
			if !a.ModAt.Equal(b.ModAt) {
				return a.ModAt.After(b.ModAt)
			}
		case sortKind:
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	}
	sort.SliceStable(list, func(i, j int) bool { return less(list[i], list[j]) })
}

// collectMedia walks root and returns every media file underneath it. Any
// directory whose name matches skipDir is not descended into, which keeps a
// previous run's output from being fed back in as new input.
func collectMedia(root string, skipDir string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if isMedia(kindOf(root)) {
			return []string{root}, nil
		}
		return nil, nil
	}

	var files []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(files) >= maxCollect {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if skipDir != "" && strings.EqualFold(d.Name(), skipDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if isMedia(kindOf(p)) {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

func filterEntries(list []Entry, filter string) []Entry {
	if filter == "" {
		return list
	}
	needle := strings.ToLower(filter)
	out := make([]Entry, 0, len(list))
	for _, e := range list {
		if e.IsUp() {
			out = append(out, e)
			continue
		}
		if strings.Contains(strings.ToLower(e.Name), needle) {
			out = append(out, e)
		}
	}
	return out
}
