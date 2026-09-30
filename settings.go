package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

type ImageFmt struct {
	Name  string
	Label string
	Ext   string
	Mime  string
	lossy bool
}

var imageFormats = []ImageFmt{
	{Name: "webp", Label: "WebP", Ext: ".webp", lossy: true},
	{Name: "jpeg", Label: "JPEG", Ext: ".jpg", lossy: true},
	{Name: "png", Label: "PNG", Ext: ".png"},
}

type VideoFmt struct {
	Name    string
	Label   string
	Ext     string
	Encoder string
	Args    func(quality int, preset string) []string
	// Audio codecs the container can carry without re-encoding.
	CopyableAudio map[string]bool
	// Used when the source audio is not in CopyableAudio.
	AudioFallback func() []string
}

func crfFromQuality(q int) int {
	// Map a friendly 1..100 quality onto each encoder's own CRF range.
	switch {
	case q >= 95:
		return 10
	case q >= 90:
		return 16
	case q >= 80:
		return 20
	case q >= 70:
		return 24
	case q >= 60:
		return 28
	case q >= 50:
		return 32
	case q >= 40:
		return 36
	case q >= 30:
		return 40
	case q >= 20:
		return 46
	default:
		return 52
	}
}

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func aacArgs(bitrate string) func() []string {
	return func() []string { return []string{"-c:a", "aac", "-b:a", bitrate, "-ac", "2"} }
}

func opusArgs(bitrate string) func() []string {
	return func() []string { return []string{"-c:a", "libopus", "-b:a", bitrate, "-ac", "2"} }
}

// MP4 carries the common audio codecs; anything else is re-encoded to AAC.
var mp4Audio = setOf("aac", "mp3", "ac3", "eac3", "alac")

// Matroska is flexible enough to pass almost anything through untouched.
var mkvAudio = setOf("aac", "ac3", "eac3", "alac", "mp3", "flac", "opus",
	"vorbis", "pcm_s16le", "pcm_s24le", "wmav2", "truehd")

// WebM only allows Opus and Vorbis. Copying AAC here produces an unreadable
// file, so anything else has to be re-encoded.
var webmAudio = setOf("opus", "vorbis")

var videoFormats = []VideoFmt{
	{
		Name: "h264", Label: "MP4 · H.264", Ext: ".mp4", Encoder: "libx264",
		Args: func(q int, preset string) []string {
			return []string{"-c:v", "libx264", "-crf", itoa(crfFromQuality(q)), "-preset", preset,
				"-pix_fmt", "yuv420p", "-movflags", "+faststart"}
		},
		CopyableAudio: mp4Audio,
		AudioFallback: aacArgs("160k"),
	},
	{
		Name: "h265", Label: "MP4 · H.265", Ext: ".mp4", Encoder: "libx265",
		Args: func(q int, preset string) []string {
			return []string{"-c:v", "libx265", "-crf", itoa(crfFromQuality(q) + 4), "-preset", preset,
				"-pix_fmt", "yuv420p", "-tag:v", "hvc1", "-movflags", "+faststart"}
		},
		CopyableAudio: mp4Audio,
		AudioFallback: aacArgs("160k"),
	},
	{
		Name: "av1", Label: "MKV · AV1", Ext: ".mkv", Encoder: "libsvtav1",
		Args: func(q int, preset string) []string {
			return []string{"-c:v", "libsvtav1", "-crf", itoa(crfFromQuality(q) + 8),
				"-preset", svtPreset(preset), "-pix_fmt", "yuv420p"}
		},
		CopyableAudio: mkvAudio,
		AudioFallback: aacArgs("160k"),
	},
	{
		Name: "vp9", Label: "WebM · VP9", Ext: ".webm", Encoder: "libvpx-vp9",
		Args: func(q int, preset string) []string {
			return []string{"-c:v", "libvpx-vp9", "-crf", itoa(crfFromQuality(q)),
				"-b:v", "0", "-row-mt", "1", "-deadline", "good", "-cpu-used", vp9Speed(preset)}
		},
		CopyableAudio: webmAudio,
		AudioFallback: opusArgs("128k"),
	},
}

var presets = []string{"ultrafast", "superfast", "veryfast", "faster", "fast",
	"medium", "slow", "slower", "veryslow"}

func svtPreset(p string) string {
	idx := indexOf(presets, p)
	// SVT-AV1 uses 0..13 and gets slower as the number grows.
	return itoa(min(13, max(0, idx+3)))
}

func vp9Speed(p string) string {
	idx := indexOf(presets, p)
	return itoa(min(8, max(0, 8-idx)))
}

var resolutions = []struct {
	Label string
	Long  int // 0 = keep original
}{
	{"original", 0},
	{"3840p", 3840},
	{"2560p", 2560},
	{"1920p", 1920},
	{"1600p", 1600},
	{"1280p", 1280},
	{"1080p", 1080},
	{"720p", 720},
	{"480p", 480},
	{"360p", 360},
}

type AudioMode int

const (
	AudioKeep AudioMode = iota
	AudioStrip
	AudioAAC
)

var audioModes = []string{"keep original", "no audio", "AAC 128k"}

type OutputMode int

const (
	OutputAlongside OutputMode = iota
	OutputSubfolder
	OutputCustom
	OutputReplace
	// OutputReplaceVideo replaces videos with their compressed result in
	// place and leaves images alone, writing them to the subfolder instead.
	OutputReplaceVideo
)

var outputModes = []string{
	"next to originals",
	"compressed subfolder",
	"custom path",
	"replace original",
	"replace videos only",
}

type overwriteMode int

const (
	OverwriteSkip overwriteMode = iota
	OverwriteReplace
)

var overwriteModes = []string{"skip existing", "replace existing"}

type Settings struct {
	ImageFmt   int
	VideoFmt   int
	Quality    int
	Resolution int
	Preset     int
	Audio      AudioMode
	Output     OutputMode
	OutPath    string
	Overwrite  overwriteMode
	Parallel   int
	AlsoImages bool
	AlsoVideos bool
}

func defaultSettings() Settings {
	return Settings{
		ImageFmt:   0,
		VideoFmt:   0,
		Quality:    78,
		Resolution: 3, // 1920p
		Preset:     4, // fast
		Audio:      AudioKeep,
		Output:     OutputSubfolder,
		OutPath:    "",
		Overwrite:  OverwriteSkip,
		Parallel:   2,
		AlsoImages: true,
		AlsoVideos: true,
	}
}

func (s Settings) ImageFormat() ImageFmt {
	return imageFormats[clampInt(s.ImageFmt, 0, len(imageFormats)-1)]
}
func (s Settings) VideoFormat() VideoFmt {
	return videoFormats[clampInt(s.VideoFmt, 0, len(videoFormats)-1)]
}
func (s Settings) ResolutionOpt() (string, int) {
	r := resolutions[clampInt(s.Resolution, 0, len(resolutions)-1)]
	return r.Label, r.Long
}
func (s Settings) PresetName() string { return presets[clampInt(s.Preset, 0, len(presets)-1)] }
func (s Settings) AudioLabel() string {
	return audioModes[clampInt(int(s.Audio), 0, len(audioModes)-1)]
}
func (s Settings) OutputLabel() string {
	return outputModes[clampInt(int(s.Output), 0, len(outputModes)-1)]
}
func (s Settings) OverwriteLabel() string {
	return overwriteModes[clampInt(int(s.Overwrite), 0, len(overwriteModes)-1)]
}

// rows of the settings editor
type setField int

const (
	fImageFmt setField = iota
	fVideoFmt
	fQuality
	fResolution
	fPreset
	fAudio
	fOutput
	fOutPath
	fOverwrite
	fParallel
	fScope
	fCount
)

type setRow struct {
	field setField
	label string
	kind  int // 0 = option list, 1 = number, 2 = toggle text, 3 = text input
}

var setRows = []setRow{
	{fImageFmt, "Image format", 0},
	{fVideoFmt, "Video format", 0},
	{fQuality, "Quality", 1},
	{fResolution, "Max resolution", 0},
	{fPreset, "Video preset", 0},
	{fAudio, "Audio", 0},
	{fOutput, "Output location", 0},
	{fOutPath, "Custom path", 3},
	{fOverwrite, "Existing files", 0},
	{fParallel, "Parallel jobs", 1},
	{fScope, "Process", 2},
}

func (s Settings) valueOf(f setField) string {
	switch f {
	case fImageFmt:
		return s.ImageFormat().Label
	case fVideoFmt:
		return s.VideoFormat().Label
	case fQuality:
		return fmt.Sprintf("%d  (%s)", s.Quality, s.qualityWord())
	case fResolution:
		l, _ := s.ResolutionOpt()
		return l
	case fPreset:
		return s.PresetName()
	case fAudio:
		return s.AudioLabel()
	case fOutput:
		return s.OutputLabel()
	case fOutPath:
		if s.OutPath == "" {
			return "(not set)"
		}
		return s.OutPath
	case fOverwrite:
		return s.OverwriteLabel()
	case fParallel:
		return fmt.Sprintf("%d", s.parallel())
	case fScope:
		return s.scopeLabel()
	}
	return ""
}

func (s Settings) scopeLabel() string {
	switch {
	case s.AlsoImages && s.AlsoVideos:
		return "images and videos"
	case s.AlsoImages:
		return "images only"
	case s.AlsoVideos:
		return "videos only"
	default:
		return "nothing selected"
	}
}

func (s Settings) parallel() int { return clampInt(s.Parallel, 1, 8) }

func (s Settings) qualityWord() string {
	switch {
	case s.Quality >= 92:
		return "visually lossless"
	case s.Quality >= 82:
		return "high"
	case s.Quality >= 68:
		return "balanced"
	case s.Quality >= 48:
		return "small"
	default:
		return "tiny"
	}
}

func (s *Settings) adjust(f setField, delta int) {
	switch f {
	case fImageFmt:
		s.ImageFmt = wrap(s.ImageFmt+delta, len(imageFormats))
	case fVideoFmt:
		s.VideoFmt = wrap(s.VideoFmt+delta, len(videoFormats))
	case fQuality:
		s.Quality = clampInt(s.Quality+delta*5, 5, 100)
	case fResolution:
		s.Resolution = wrap(s.Resolution+delta, len(resolutions))
	case fPreset:
		s.Preset = wrap(s.Preset+delta, len(presets))
	case fAudio:
		s.Audio = AudioMode(wrap(int(s.Audio)+delta, len(audioModes)))
	case fOutput:
		s.Output = OutputMode(wrap(int(s.Output)+delta, len(outputModes)))
	case fOverwrite:
		s.Overwrite = overwriteMode(wrap(int(s.Overwrite)+delta, len(overwriteModes)))
	case fParallel:
		s.Parallel = clampInt(s.Parallel+delta, 1, 8)
	case fScope:
		switch delta {
		case 1:
			s.AlsoImages, s.AlsoVideos = true, true
		case -1:
			s.AlsoImages, s.AlsoVideos = true, true
		default:
			s.AlsoImages = !s.AlsoImages
			if !s.AlsoImages {
				s.AlsoVideos = true
			}
			if !s.AlsoVideos {
				s.AlsoImages = true
			}
		}
		if delta < 0 {
			if s.AlsoImages && s.AlsoVideos {
				s.AlsoVideos = false
			} else {
				s.AlsoImages = false
			}
		}
	}
}

// outputSubdir is the folder name created by the default "subfolder" output
// mode. Recursive selection uses it to avoid picking up a previous run.
func (s Settings) outputSubdir() string {
	switch s.Output {
	case OutputSubfolder, OutputReplaceVideo:
		return "compressed"
	}
	return ""
}

// replacesOriginal reports whether a job of this kind ends up written over its
// source file. It is the single place that decides which kinds are destructive
// so the planner and the worker can never disagree.
func (s Settings) replacesOriginal(k Kind) bool {
	switch s.Output {
	case OutputReplace:
		return true
	case OutputReplaceVideo:
		return k == KindVideo
	}
	return false
}

// outputDirFor resolves the folder a job of the given kind is written into.
func (s Settings) outputDirFor(src string, kind Kind) string {
	switch {
	case s.Output == OutputCustom:
		if s.OutPath != "" {
			return expandHome(s.OutPath)
		}
		return filepath.Dir(src)
	case s.Output == OutputAlongside || s.Output == OutputReplace:
		return filepath.Dir(src)
	case s.Output == OutputReplaceVideo && kind == KindVideo:
		return filepath.Dir(src)
	default:
		return filepath.Join(filepath.Dir(src), s.outputSubdir())
	}
}

func expandHome(p string) string {
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}
