package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Kind int

const (
	KindOther Kind = iota
	KindDir
	KindImage
	KindVideo
)

func (k Kind) String() string {
	switch k {
	case KindDir:
		return "dir"
	case KindImage:
		return "image"
	case KindVideo:
		return "video"
	default:
		return "file"
	}
}

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".jpe": true, ".png": true, ".gif": true,
	".webp": true, ".bmp": true, ".tif": true, ".tiff": true, ".heic": true,
	".heif": true, ".avif": true, ".ico": true, ".tga": true, ".psd": true,
	".dng": true, ".raw": true, ".cr2": true, ".nef": true, ".arw": true,
	".ppm": true, ".pgm": true, ".pbm": true, ".jxl": true,
}

var videoExts = map[string]bool{
	".mp4": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true,
	".m4v": true, ".mpg": true, ".mpeg": true, ".m2v": true, ".wmv": true,
	".flv": true, ".ts": true, ".mts": true, ".m2ts": true, ".3gp": true,
	".ogv": true, ".vob": true, ".rm": true, ".rmvb": true, ".asf": true,
	".divx": true, ".f4v": true, ".mxf": true, ".y4m": true,
}

func kindOf(path string) Kind {
	switch strings.ToLower(filepath.Ext(path)) {
	case "":
		return KindOther
	case ".ts":
		return KindVideo
	}
	ext := strings.ToLower(filepath.Ext(path))
	if imageExts[ext] {
		return KindImage
	}
	if videoExts[ext] {
		return KindVideo
	}
	return KindOther
}

func isMedia(k Kind) bool { return k == KindImage || k == KindVideo }

// Meta holds everything the UI shows about a file and what the encoder needs.
type Meta struct {
	Width      int
	Height     int
	Duration   time.Duration
	Codec      string
	AudioCodec string
	FPS        float64
	HasAudio   bool
	Bitrate    int64
	Probed     bool
	Failed     bool
}

func (m Meta) Resolution() string {
	if m.Width <= 0 || m.Height <= 0 {
		return ""
	}
	return strconv.Itoa(m.Width) + "x" + strconv.Itoa(m.Height)
}

func (m Meta) LongEdge() int {
	if m.Width > m.Height {
		return m.Width
	}
	return m.Height
}

func (m Meta) DurationString() string {
	if m.Duration <= 0 {
		return ""
	}
	total := int(m.Duration.Round(time.Second).Seconds())
	h, rem := total/3600, total%3600
	mnt, sec := rem/60, rem%60
	if h > 0 {
		return pad2(h) + ":" + pad2(mnt) + ":" + pad2(sec)
	}
	return pad2(mnt) + ":" + pad2(sec)
}

func (m Meta) FPSString() string {
	if m.FPS <= 0 {
		return ""
	}
	return strconv.FormatFloat(m.FPS, 'f', 2, 64) + " fps"
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// probeCache avoids re-running ffprobe for files we already know about.
type probeCache struct {
	mu sync.RWMutex
	m  map[string]Meta
}

func newProbeCache() *probeCache { return &probeCache{m: map[string]Meta{}} }

func (p *probeCache) get(path string) (Meta, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.m[path]
	return v, ok
}

func (p *probeCache) put(path string, m Meta) {
	p.mu.Lock()
	p.m[path] = m
	p.mu.Unlock()
}

// get2 reports whether the cache already knows about this path.
func (p *probeCache) get2(path string) bool {
	p.mu.RLock()
	_, ok := p.m[path]
	p.mu.RUnlock()
	return ok
}

type ffprobeStream struct {
	CodecType    string `json:"codec_type"`
	CodecName    string `json:"codec_name"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	RFrameRate   string `json:"r_frame_rate"`
	Duration     string `json:"duration"`
	BitRate      string `json:"bit_rate"`
	AvgFrameRate string `json:"avg_frame_rate"`
}

type ffprobeFormat struct {
	Duration string `json:"duration"`
	BitRate  string `json:"bit_rate"`
	Format   string `json:"format_name"`
}

type ffprobeOut struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

func parseRate(s string) float64 {
	if s == "" || s == "0/0" {
		return 0
	}
	parts := strings.SplitN(s, "/", 2)
	num, err1 := strconv.ParseFloat(parts[0], 64)
	if err1 != nil {
		return 0
	}
	if len(parts) == 1 {
		return num
	}
	den, err2 := strconv.ParseFloat(parts[1], 64)
	if err2 != nil || den == 0 {
		return 0
	}
	return num / den
}

func probe(ctx context.Context, path string) Meta {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return Meta{Failed: true}
	}
	var parsed ffprobeOut
	if err := json.Unmarshal(out, &parsed); err != nil {
		return Meta{Failed: true}
	}

	m := Meta{Probed: true}
	for _, s := range parsed.Streams {
		switch s.CodecType {
		case "video":
			if m.Width == 0 && s.Width > 0 {
				m.Width, m.Height = s.Width, s.Height
				m.Codec = s.CodecName
			}
			if f := parseRate(s.AvgFrameRate); f > 0 {
				m.FPS = f
			} else if f := parseRate(s.RFrameRate); f > 0 {
				m.FPS = f
			}
		case "audio":
			m.HasAudio = true
			if m.AudioCodec == "" {
				m.AudioCodec = s.CodecName
			}
		}
	}
	m.Duration = parseDuration(parsed.Format.Duration)
	if m.Duration == 0 {
		for _, s := range parsed.Streams {
			if s.CodecType == "video" {
				if d := parseDuration(s.Duration); d > 0 {
					m.Duration = d
					break
				}
			}
		}
	}
	if b, err := strconv.ParseInt(parsed.Format.BitRate, 10, 64); err == nil {
		m.Bitrate = b
	}
	return m
}

func parseDuration(s string) time.Duration {
	if s == "" || s == "N/A" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return time.Duration(f * float64(time.Second))
}
