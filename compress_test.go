package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestKindOf(t *testing.T) {
	cases := map[string]Kind{
		"a.JPG": KindImage, "a.png": KindImage, "a.webp": KindImage,
		"a.mp4": KindVideo, "a.MOV": KindVideo, "a.mkv": KindVideo,
		"a.ts": KindVideo, "a.txt": KindOther, "noext": KindOther,
	}
	for in, want := range cases {
		if got := kindOf(in); got != want {
			t.Errorf("kindOf(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseRate(t *testing.T) {
	cases := map[string]float64{
		"30/1": 30, "30000/1001": 29.97, "0/0": 0, "": 0, "60": 60,
	}
	for in, want := range cases {
		got := parseRate(in)
		if diff := got - want; diff > 0.02 || diff < -0.02 {
			t.Errorf("parseRate(%q) = %v, want ~%v", in, got, want)
		}
	}
}

func TestJpegQscaleMonotonic(t *testing.T) {
	prev := 0
	for q := 100; q >= 1; q -= 5 {
		v := jpegQscale(q)
		n := atoi(t, v)
		if n < 2 || n > 31 {
			t.Fatalf("q=%d produced out-of-range qscale %s", q, v)
		}
		if n < prev {
			t.Fatalf("q=%d produced %d which is better than lower quality %d", q, n, prev)
		}
		prev = n
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func TestCRFFromQualityMonotonic(t *testing.T) {
	// Higher quality must never mean a higher (worse) CRF.
	prev := 0
	for q := 5; q <= 100; q += 5 {
		v := crfFromQuality(q)
		if prev != 0 && v > prev {
			t.Fatalf("quality %d gave crf %d, worse than lower quality crf %d", q, v, prev)
		}
		prev = v
	}
	if crfFromQuality(100) >= crfFromQuality(5) {
		t.Errorf("quality range collapsed: 100->%d, 5->%d", crfFromQuality(100), crfFromQuality(5))
	}
}

func TestFfmpegArgsVideo(t *testing.T) {
	s := defaultSettings()
	s.Resolution = 3 // 1920p
	r := &Runner{settings: s}
	meta := Meta{Width: 3840, Height: 2160, Duration: 30 * time.Second, HasAudio: true, Codec: "h264"}
	args := r.ffmpegArgs("/in/clip.mov", "/out/clip.mp4", KindVideo, meta)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"libx264", "-crf", "-preset fast", "yuv420p", "+faststart",
		"scale=", "1920", "-progress pipe:1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("video args missing %q\ngot: %s", want, joined)
		}
	}
	if !strings.Contains(joined, "/out/clip.mp4") {
		t.Errorf("output path missing: %s", joined)
	}
	// audio map must be requested since the source has audio
	if !strings.Contains(joined, "0:a:0") {
		t.Errorf("audio stream not mapped: %s", joined)
	}
}

func TestFfmpegArgsNoUpscale(t *testing.T) {
	s := defaultSettings()
	s.Resolution = 6 // 1080p
	r := &Runner{settings: s}
	// Source is already smaller than the cap: no scale filter at all.
	args := r.ffmpegArgs("/in/a.mov", "/out/a.mp4", KindVideo, Meta{Width: 640, Height: 480})
	if strings.Contains(strings.Join(args, " "), "scale=") {
		t.Errorf("upscaling a 640x480 source: %v", args)
	}
}

func TestFfmpegArgsStripAudio(t *testing.T) {
	s := defaultSettings()
	s.Audio = AudioStrip
	r := &Runner{settings: s}
	args := r.ffmpegArgs("/in/a.mov", "/out/a.mp4", KindVideo, Meta{Width: 100, Height: 100})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-an") {
		t.Errorf("expected -an: %s", joined)
	}
	if strings.Contains(joined, "0:a:0") {
		t.Errorf("audio should not be mapped: %s", joined)
	}
}

func TestFfmpegArgsImageJPEG(t *testing.T) {
	s := defaultSettings()
	s.ImageFmt = 1 // jpeg
	r := &Runner{settings: s}
	args := r.ffmpegArgs("/in/a.png", "/out/a.jpg", KindImage, Meta{Width: 800, Height: 600})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-frames:v 1") {
		t.Errorf("image must be limited to one frame: %s", joined)
	}
	if !strings.Contains(joined, "mjpeg") {
		t.Errorf("expected mjpeg encoder: %s", joined)
	}
}

func TestCwebpArgs(t *testing.T) {
	s := defaultSettings()
	s.Resolution = 4 // 1600p
	r := &Runner{settings: s}
	args := r.cwebpArgs("/in/a.png", "/out/a.webp", Meta{Width: 4000, Height: 3000})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-q 78") {
		t.Errorf("quality not passed: %s", joined)
	}
	if !strings.Contains(joined, "-resize 1600 0") {
		t.Errorf("resize not applied: %s", joined)
	}
	n := len(args)
	if args[n-2] != "-o" || args[n-1] != "/out/a.webp" {
		t.Errorf("output args wrong: %v", args)
	}
}

func TestOutputDirModes(t *testing.T) {
	s := defaultSettings()
	s.Output = OutputAlongside
	if got := s.outputDirFor("/a/b/c.mov", KindVideo); got != "/a/b" {
		t.Errorf("alongside: %q", got)
	}
	s.Output = OutputSubfolder
	if got := s.outputDirFor("/a/b/c.mov", KindVideo); got != "/a/b/compressed" {
		t.Errorf("subfolder: %q", got)
	}
	s.Output = OutputCustom
	s.OutPath = "~/out"
	if got := s.outputDirFor("/a/b/c.mov", KindVideo); !strings.HasPrefix(got, homeDir()) {
		t.Errorf("custom not expanded: %q", got)
	}
	s.Output = OutputReplace
	if got := s.outputDirFor("/a/b/c.mov", KindVideo); got != "/a/b" {
		t.Errorf("replace: %q", got)
	}
}

// Videos are replaced in place, but images must survive: they go to the
// compressed subfolder so the originals stay exactly as they were.
func TestOutputDirReplaceVideoKindAware(t *testing.T) {
	s := defaultSettings()
	s.Output = OutputReplaceVideo
	if got := s.outputDirFor("/a/b/c.mov", KindVideo); got != "/a/b" {
		t.Errorf("replace-video, video: %q want /a/b", got)
	}
	if got := s.outputDirFor("/a/b/c.jpg", KindImage); got != "/a/b/compressed" {
		t.Errorf("replace-video, image: %q want /a/b/compressed", got)
	}
	if s.outputSubdir() != "compressed" {
		t.Errorf("replace-video subdir = %q, want compressed", s.outputSubdir())
	}
	if s.replacesOriginal(KindVideo) != true {
		t.Error("replace-video must replace videos")
	}
	if s.replacesOriginal(KindImage) != false {
		t.Error("replace-video must not replace images")
	}
	// A full replace-original run still covers both kinds.
	s.Output = OutputReplace
	if !s.replacesOriginal(KindImage) || !s.replacesOriginal(KindVideo) {
		t.Error("replace must cover images and videos")
	}
	s.Output = OutputSubfolder
	if s.replacesOriginal(KindImage) || s.replacesOriginal(KindVideo) {
		t.Error("subfolder mode must not replace anything")
	}
}

// TestReplacePlan targets replace-original job resolution: same-extension
// sources replace themselves in place, other sources write next to the
// original, and a colliding second source is skipped instead of racing.
func TestReplacePlan(t *testing.T) {
	s := defaultSettings()
	s.Output = OutputReplace
	s.ImageFmt = 2 // png
	root := t.TempDir()
	shot := filepath.Join(root, "shot.png")
	clipMOV := filepath.Join(root, "clip.mov")
	clipMP4 := filepath.Join(root, "clip.mp4")
	for _, p := range []string{shot, clipMOV, clipMP4} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	jobs := []*Job{
		{Source: shot, Kind: KindImage, Before: 100},
		{Source: clipMOV, Kind: KindVideo, Before: 100},
		{Source: clipMP4, Kind: KindVideo, Before: 100},
	}
	r := NewRunner(jobs, s)
	r.plan()

	if got := r.plan_[0].target; got != shot {
		t.Errorf("png→png: target %q, want in-place %q", got, shot)
	}
	if got := r.plan_[1].target; got != clipMP4 {
		t.Errorf("mov→mp4: target %q, want %q", got, clipMP4)
	}
	if !r.plan_[2].skip {
		t.Errorf("clip.mp4 collides with the mov→mp4 target and should be skipped")
	}

	// A different stem that maps to a fresh path must not be skipped.
	other := filepath.Join(root, "other.png")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := NewRunner([]*Job{{Source: other, Kind: KindImage, Before: 100}}, s)
	r2.plan()
	if r2.plan_[0].skip {
		t.Errorf("fresh other.png should not be skipped")
	}
	if got := r2.plan_[0].target; got != other {
		t.Errorf("fresh target %q, want in-place %q", got, other)
	}
}

// TestReplaceVideoPlan is the end-to-end plan check for the "replace videos
// only" mode: the video target is its own path (in place), the image target is
// the compressed subfolder, and a colliding second video still skips instead
// of racing.
func TestReplaceVideoPlan(t *testing.T) {
	s := defaultSettings()
	s.Output = OutputReplaceVideo
	s.ImageFmt = 2 // png
	root := t.TempDir()
	shot := filepath.Join(root, "shot.png")
	clipMP4 := filepath.Join(root, "clip.mp4")
	clipMOV := filepath.Join(root, "clip.mov")
	for _, p := range []string{shot, clipMP4, clipMOV} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := NewRunner([]*Job{
		{Source: shot, Kind: KindImage, Before: 100},
		{Source: clipMP4, Kind: KindVideo, Before: 100},
		{Source: clipMOV, Kind: KindVideo, Before: 100},
	}, s)
	r.plan()

	if got, want := r.plan_[0].target, filepath.Join(root, "compressed", "shot.png"); got != want {
		t.Errorf("image target %q, want %q (original must be kept)", got, want)
	}
	if r.plan_[0].target == shot {
		t.Error("image must not be planned onto its own path")
	}
	if got := r.plan_[1].target; got != clipMP4 {
		t.Errorf("mp4→mp4: target %q, want in-place %q", got, clipMP4)
	}
	if !r.plan_[2].skip {
		t.Error("clip.mov collides with the clip.mp4 target and should be skipped")
	}
}

func TestCollectMediaSkipsHidden(t *testing.T) {
	dir := t.TempDir()
	mk := func(p string) {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("a.png")
	mk("b.mov")
	mk(".hidden/c.png")
	mk("notes.txt")

	files, err := collectMedia(dir, "compressed")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 media files, got %v", files)
	}
}

func TestSortEntriesPutsUpFirst(t *testing.T) {
	list := []Entry{
		{Name: "b.png", Kind: KindImage},
		{Name: "..", Dir: true, Kind: KindDir},
		{Name: "zdir", Dir: true, Kind: KindDir},
		{Name: "a.png", Kind: KindImage},
	}
	sortEntries(list, sortName)
	if list[0].Name != ".." {
		t.Errorf("parent must sort first, got %s", list[0].Name)
	}
	if list[1].Name != "zdir" {
		t.Errorf("dirs before files, got %s", list[1].Name)
	}
	if list[2].Name != "a.png" {
		t.Errorf("files alphabetical, got %s", list[2].Name)
	}
}

func TestBookmarkNameAvoidsClobbering(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "a.webp")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(nil, defaultSettings())
	first := r.bookmarkName(existing)
	second := r.bookmarkName(existing)
	if first == existing {
		t.Errorf("should have avoided the existing file: %s", first)
	}
	if second == first {
		t.Errorf("two callers got the same name: %s", second)
	}
}

// ------------------------------------------------------------------ e2e

func requireTools(t *testing.T) {
	t.Helper()
	for _, b := range []string{"ffmpeg", "ffprobe", "cwebp"} {
		if !lookPath(b) {
			t.Skipf("%s not installed", b)
		}
	}
}

func runJobs(t *testing.T, jobs []*Job, s Settings) map[int]Event {
	t.Helper()
	r := NewRunner(jobs, s)
	r.Run(context.Background())
	states := map[int]Event{}
	for {
		ev, ok := r.Next()
		if !ok {
			return states
		}
		states[ev.Index] = ev
		jobs[ev.Index].apply(ev)
	}
}

func makeJob(t *testing.T, src string) *Job {
	t.Helper()
	st, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	return &Job{Source: src, Kind: kindOf(src), Before: st.Size(), State: JobQueued}
}

func TestEndToEndImageToWebP(t *testing.T) {
	requireTools(t)
	src := realAsset(t, ".png", 1<<20)
	out := t.TempDir()

	s := defaultSettings()
	s.Output = OutputCustom
	s.OutPath = out
	s.Resolution = 0
	s.Quality = 75

	jobs := []*Job{makeJob(t, src)}
	ev := runJobs(t, jobs, s)

	if e := ev[0]; e.State != JobDone {
		t.Fatalf("expected done, got %v (%s)", e.State, e.Err)
	}
	if jobs[0].After >= jobs[0].Before {
		t.Errorf("webp (%d) not smaller than png (%d)", jobs[0].After, jobs[0].Before)
	}
	assertProbeable(t, jobs[0].Target)

	// Second run with skip-existing must not touch the file.
	ev2 := runJobs(t, []*Job{makeJob(t, src)}, s)
	if ev2[0].State != JobSkipped {
		t.Errorf("expected skip, got %v", ev2[0].State)
	}
}

// TestEndToEndReplaceVideosOnly is the behaviour the "c" prompt promises: a
// real run where the video is replaced in place and the image is written to
// compressed/ with its original left untouched.
func TestEndToEndReplaceVideosOnly(t *testing.T) {
	requireTools(t)
	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	png, err := os.ReadFile(realAsset(t, ".png", 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(img, png, 0o644); err != nil {
		t.Fatal(err)
	}
	imgBefore, err := os.Stat(img)
	if err != nil {
		t.Fatal(err)
	}
	vid := synthVideoTo(t, filepath.Join(dir, "clip.mov"), 320, 240, 2, false)
	vidBefore, err := os.Stat(vid)
	if err != nil {
		t.Fatal(err)
	}

	s := defaultSettings()
	s.Output = OutputReplaceVideo
	s.Resolution = 1
	s.Quality = 40
	s.Overwrite = OverwriteReplace

	ev := runJobs(t, []*Job{makeJob(t, img), makeJob(t, vid)}, s)

	if e := ev[0]; e.State != JobDone {
		t.Errorf("image job = %v (%s), want done", e.State, e.Err)
	}
	if e := ev[1]; e.State != JobDone {
		t.Errorf("video job = %v (%s), want done", e.State, e.Err)
	}

	// The image original is byte-for-byte what it was, and the compressed
	// result sits next to it in the subfolder.
	gotImg, err := os.Stat(img)
	if err != nil {
		t.Fatalf("image original was removed: %v", err)
	}
	if gotImg.Size() != imgBefore.Size() {
		t.Errorf("image original changed size: %d → %d", imgBefore.Size(), gotImg.Size())
	}
	wantImg := filepath.Join(dir, "compressed", "shot.webp")
	if st, err := os.Stat(wantImg); err != nil || st.Size() == 0 {
		t.Errorf("compressed image missing: %s (%v)", wantImg, err)
	}

	// The video was replaced in place: same path, smaller file, and the .mov
	// is gone because the result is an .mp4.
	if _, err := os.Stat(vid); !os.IsNotExist(err) {
		t.Errorf("original video still present at %s: %v", vid, err)
	}
	replaced := filepath.Join(dir, "clip.mp4")
	st, err := os.Stat(replaced)
	if err != nil {
		t.Fatalf("replaced video missing: %v", err)
	}
	if st.Size() >= vidBefore.Size() {
		t.Errorf("replacement not smaller: %d → %d", vidBefore.Size(), st.Size())
	}
	assertProbeable(t, replaced)
}

func TestEndToEndImageToJPEG(t *testing.T) {
	requireTools(t)
	src := realAsset(t, ".png", 1<<20)
	out := t.TempDir()

	s := defaultSettings()
	s.ImageFmt = 1
	s.Output = OutputCustom
	s.OutPath = out
	s.Resolution = 5 // 1280p
	s.Quality = 80

	jobs := []*Job{makeJob(t, src)}
	ev := runJobs(t, jobs, s)
	if e := ev[0]; e.State != JobDone {
		t.Fatalf("expected done, got %v (%s)", e.State, e.Err)
	}
	assertProbeable(t, jobs[0].Target)
	if jobs[0].After >= jobs[0].Before {
		t.Errorf("jpeg (%d) not smaller than png (%d)", jobs[0].After, jobs[0].Before)
	}
	m := metaOf(t, jobs[0].Target)
	if m.LongEdge() > 1280 {
		t.Errorf("not resized: long edge %d", m.LongEdge())
	}
}

func TestEndToEndVideoToH264(t *testing.T) {
	requireTools(t)
	src := synthVideo(t, 1280, 720, 3, true)
	out := t.TempDir()

	s := defaultSettings()
	s.Output = OutputCustom
	s.OutPath = out
	s.Resolution = 0
	s.Quality = 70
	s.Preset = 7 // slower

	jobs := []*Job{makeJob(t, src)}
	ev := runJobs(t, jobs, s)
	if e := ev[0]; e.State != JobDone {
		t.Fatalf("expected done, got %v (%s)", e.State, e.Err)
	}
	assertProbeable(t, jobs[0].Target)
	if jobs[0].After >= jobs[0].Before {
		t.Errorf("output (%d) not smaller than source (%d)", jobs[0].After, jobs[0].Before)
	}
	m := metaOf(t, jobs[0].Target)
	if m.Duration <= 0 {
		t.Errorf("output has no duration: %+v", m)
	}
	if m.Width != 1280 || m.Height != 720 {
		t.Errorf("unexpected output size %dx%d", m.Width, m.Height)
	}
	if !m.HasAudio {
		t.Errorf("audio track was dropped")
	}
}

func TestEndToEndVideoDownscale(t *testing.T) {
	requireTools(t)
	src := synthVideo(t, 1920, 1080, 2, false)
	out := t.TempDir()

	s := defaultSettings()
	s.Output = OutputCustom
	s.OutPath = out
	s.Resolution = 8 // 480p
	s.Preset = 5

	jobs := []*Job{makeJob(t, src)}
	ev := runJobs(t, jobs, s)
	if e := ev[0]; e.State != JobDone {
		t.Fatalf("expected done, got %v (%s)", e.State, e.Err)
	}
	m := metaOf(t, jobs[0].Target)
	if m.LongEdge() != 480 {
		t.Errorf("long edge = %d, want 480", m.LongEdge())
	}
	// Aspect ratio must survive the resize.
	srcRatio := float64(1920) / 1080
	gotRatio := float64(m.Width) / float64(m.Height)
	if diff := gotRatio - srcRatio; diff > 0.02 || diff < -0.02 {
		t.Errorf("aspect ratio drifted: %.3f vs %.3f", gotRatio, srcRatio)
	}
	if m.HasAudio {
		t.Errorf("synthesized source had no audio")
	}
}

func TestEndToEndVideoStripAudio(t *testing.T) {
	requireTools(t)
	src := synthVideo(t, 640, 480, 2, true)
	out := t.TempDir()

	s := defaultSettings()
	s.Output = OutputCustom
	s.OutPath = out
	s.Audio = AudioStrip

	jobs := []*Job{makeJob(t, src)}
	if ev := runJobs(t, jobs, s)[0]; ev.State != JobDone {
		t.Fatalf("expected done, got %v (%s)", ev.State, ev.Err)
	}
	if metaOf(t, jobs[0].Target).HasAudio {
		t.Errorf("audio survived even though it was stripped")
	}
}

func TestEndToEndVideoMixedBatch(t *testing.T) {
	requireTools(t)
	dir := t.TempDir()
	out := t.TempDir()

	files := []string{
		synthVideoTo(t, filepath.Join(dir, "a.mp4"), 320, 240, 2, true),
		synthVideoTo(t, filepath.Join(dir, "b.mkv"), 320, 240, 2, false),
	}
	var jobs []*Job
	for _, f := range files {
		jobs = append(jobs, makeJob(t, f))
	}

	s := defaultSettings()
	s.Output = OutputCustom
	s.OutPath = out
	s.Parallel = 3
	s.Preset = 6

	evs := runJobs(t, jobs, s)
	for i := range jobs {
		if evs[i].State != JobDone {
			t.Errorf("job %d: %v (%s)", i, evs[i].State, evs[i].Err)
			continue
		}
		if jobs[i].Target == "" {
			t.Errorf("job %d: no target recorded", i)
			continue
		}
		assertProbeable(t, jobs[i].Target)
	}
	// Two sources with the same stem in different containers must not collide.
	if jobs[0].Target == jobs[1].Target {
		t.Errorf("both jobs wrote to %s", jobs[0].Target)
	}
}

func TestEndToEndAllVideoCodecs(t *testing.T) {
	requireTools(t)
	src := synthVideo(t, 640, 480, 2, true)

	need := map[string]bool{"libx264": false, "libx265": false, "libsvtav1": false, "libvpx-vp9": false}
	out, _ := runCapture("ffmpeg", "-hide_banner", "-encoders")
	for name := range need {
		if strings.Contains(out, " "+name+" ") {
			need[name] = true
		}
	}

	for i, f := range videoFormats {
		if !need[f.Encoder] {
			t.Logf("skipping %s: encoder not built into this ffmpeg", f.Name)
			continue
		}
		out := t.TempDir()
		s := defaultSettings()
		s.VideoFmt = i
		s.Output = OutputCustom
		s.OutPath = out
		s.Preset = 5
		s.Quality = 70

		jobs := []*Job{makeJob(t, src)}
		ev := runJobs(t, jobs, s)
		if e := ev[0]; e.State != JobDone {
			t.Errorf("%s: got %v (%s)", f.Name, e.State, e.Err)
			continue
		}
		assertProbeable(t, jobs[0].Target)
		m := metaOf(t, jobs[0].Target)
		if !strings.Contains(m.Codec, wantCodecName(f.Encoder)) {
			t.Errorf("%s: output codec is %q, want %q", f.Name, m.Codec, wantCodecName(f.Encoder))
		}
		if m.Width != 640 {
			t.Errorf("%s: output is %dx%d", f.Name, m.Width, m.Height)
		}
	}
}

func wantCodecName(encoder string) string {
	switch encoder {
	case "libx265":
		return "hevc"
	case "libsvtav1":
		return "av1"
	case "libvpx-vp9":
		return "vp9"
	default:
		return "h264"
	}
}

func TestCancelLeavesNoPartialFile(t *testing.T) {
	requireTools(t)
	src := synthVideo(t, 1920, 1080, 30, true)
	out := t.TempDir()

	s := defaultSettings()
	s.Output = OutputCustom
	s.OutPath = out
	s.Preset = 8 // veryslow, gives us time to interrupt
	s.Resolution = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := []*Job{makeJob(t, src)}
	r := NewRunner(jobs, s)
	r.Run(ctx)

	// A single goroutine consumes the whole run, mirroring the UI event loop.
	started := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		var once bool
		for {
			ev, ok := r.Next()
			if !ok {
				return
			}
			jobs[ev.Index].apply(ev)
			if ev.State == JobRunning && !once {
				once = true
				close(started)
			}
		}
	}()

	// Wait until the encoder is actually producing frames, then pull the plug.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("job never started")
	}

	time.Sleep(1200 * time.Millisecond)
	r.Cancel()

	select {
	case <-drained:
	case <-time.After(60 * time.Second):
		t.Fatal("runner never finished after cancel")
	}

	if !r.Canceled {
		t.Error("runner did not record that it was canceled")
	}
	if jobs[0].State != JobCanceled {
		t.Errorf("expected canceled, got %v", jobs[0].State)
	}

	ents, _ := os.ReadDir(out)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".part") {
			t.Errorf("partial file left behind: %s", e.Name())
		}
	}
	if len(ents) != 0 {
		t.Errorf("canceled job should not leave a final file, found %d", len(ents))
	}
}

func TestBrokenInputFailsGracefully(t *testing.T) {
	requireTools(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(bad, []byte("this is not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := defaultSettings()
	s.Output = OutputCustom
	s.OutPath = filepath.Join(dir, "out")

	jobs := []*Job{{Source: bad, Kind: KindImage, Before: 21, State: JobQueued}}
	ev := runJobs(t, jobs, s)
	if ev[0].State != JobFailed {
		t.Errorf("expected failure, got %v", ev[0].State)
	}
	if ev[0].Err == "" {
		t.Errorf("failure carried no message for the user")
	}
}

// ---------------------------------------------------------------- helpers

// synthVideoTo renders a noisy test clip with ffmpeg's lavfi sources, so the
// video tests never depend on whatever happens to be on the user's disk.
func synthVideoTo(t *testing.T, path string, w, h, seconds int, withAudio bool) string {
	t.Helper()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30:duration=%d", w, h, seconds),
	}
	if withAudio {
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("sine=frequency=440:sample_rate=44100:duration=%d", seconds))
	}
	args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-crf", "18", "-pix_fmt", "yuv420p")
	if withAudio {
		args = append(args, "-c:a", "aac", "-b:a", "128k", "-shortest")
	}
	args = append(args, path)

	if out, err := runCaptureErr("ffmpeg", args...); err != nil {
		t.Fatalf("could not build fixture %s: %v\n%s", path, err, out)
	}
	return path
}

func synthVideo(t *testing.T, w, h, seconds int, withAudio bool) string {
	t.Helper()
	return synthVideoTo(t, filepath.Join(t.TempDir(), "src.mov"), w, h, seconds, withAudio)
}

func realAsset(t *testing.T, ext string, minSize int64) string {
	t.Helper()
	dirs := []string{
		filepath.Join(homeDir(), "pCloud Drive", "My Pictures"),
		filepath.Join(homeDir(), "pCloud Drive", "My Videos"),
		filepath.Join(homeDir(), "Pictures"),
		filepath.Join(homeDir(), "Movies"),
		filepath.Join(homeDir(), "Desktop"),
		filepath.Join(homeDir(), "Downloads"),
	}
	var best string
	var bestSize int64
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ext) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Size() < minSize {
				continue
			}
			if best == "" || info.Size() < bestSize {
				bestSize, best = info.Size(), filepath.Join(d, e.Name())
			}
		}
	}
	return best
}

func assertProbeable(t *testing.T, p string) {
	t.Helper()
	if m := metaOf(t, p); m.Failed || m.Width == 0 {
		t.Errorf("output is not a readable media file: %s (%+v)", p, m)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return
	}
	out, err := runCapture("ffprobe", "-v", "error", "-show_entries", "format=format_name", "-of", "csv=p=0", p)
	if err != nil {
		t.Errorf("ffprobe rejected %s: %v", out, err)
	}
}

func metaOf(t *testing.T, p string) Meta {
	t.Helper()
	m := probe(context.Background(), p)
	return m
}

// A smoke test against whatever real media the user actually has. Skips when
// the machine has nothing suitable.
func TestRealWorldAssets(t *testing.T) {
	requireTools(t)
	img := realAsset(t, ".png", 1<<20)
	vid := realAsset(t, ".mov", 1<<20)
	if img == "" {
		t.Log("no real image found; skipping image half")
	}
	if vid == "" {
		t.Log("no real video found; skipping video half")
	}
	if img == "" && vid == "" {
		t.Skip("no real media on this machine")
	}

	s := defaultSettings()
	s.Output = OutputCustom
	s.OutPath = t.TempDir()
	s.Preset = 6

	if img != "" {
		jobs := []*Job{makeJob(t, img)}
		if ev := runJobs(t, jobs, s)[0]; ev.State != JobDone {
			t.Errorf("real image %s: %v (%s)", filepath.Base(img), ev.State, ev.Err)
		} else {
			t.Logf("%s: %s -> %s", filepath.Base(img), humanSize(jobs[0].Before), humanSize(jobs[0].After))
		}
	}
	if vid != "" {
		s2 := s
		s2.Resolution = 6 // 1080p
		jobs := []*Job{makeJob(t, vid)}
		if ev := runJobs(t, jobs, s2)[0]; ev.State != JobDone {
			t.Errorf("real video %s: %v (%s)", filepath.Base(vid), ev.State, ev.Err)
		} else {
			t.Logf("%s: %s -> %s", filepath.Base(vid), humanSize(jobs[0].Before), humanSize(jobs[0].After))
		}
	}
}

func runCaptureErr(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestContainerAudioCompatibility(t *testing.T) {
	// Copying audio into a container that cannot hold it silently produces a
	// broken file, so the args must follow the format's own table.
	cases := []struct {
		videoIdx int
		srcAudio string
		wantHas  bool
		wantStr  string
	}{
		{0, "aac", false, "-c:a copy"},  // mp4 keeps aac
		{0, "opus", true, "-c:a aac"},   // mp4 does not, re-encode
		{2, "opus", false, "-c:a copy"}, // mkv keeps opus
		{2, "pcm_s16le", false, "-c:a copy"},
		{3, "opus", false, "-c:a copy"},  // webm keeps opus
		{3, "aac", true, "-c:a libopus"}, // webm cannot hold aac
		{3, "vorbis", false, "-c:a copy"},
	}
	for _, c := range cases {
		s := defaultSettings()
		s.VideoFmt = c.videoIdx
		r := &Runner{settings: s}
		args := r.ffmpegArgs("/in/a.mov", "/out/a", KindVideo,
			Meta{Width: 16, Height: 16, HasAudio: true, AudioCodec: c.srcAudio})
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, c.wantStr) {
			t.Errorf("video=%s audio=%s: want %q in\n%s",
				videoFormats[c.videoIdx].Name, c.srcAudio, c.wantStr, joined)
		}
		if !c.wantHas && !strings.Contains(joined, "-map 0:a:0?") {
			t.Errorf("video=%s: audio stream not mapped", videoFormats[c.videoIdx].Name)
		}
	}
}

func TestNoAudioSourceNeverMapsAudio(t *testing.T) {
	s := defaultSettings()
	r := &Runner{settings: s}
	args := r.ffmpegArgs("/in/a.mov", "/out/a.mp4", KindVideo, Meta{Width: 16, Height: 16})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "0:a:0") {
		t.Errorf("mapped audio from a source that has none: %s", joined)
	}
}

func TestJobApplyIgnoresLateProgress(t *testing.T) {
	j := &Job{State: JobQueued}
	j.apply(Event{Index: 0, State: JobRunning, Log: "starting…", Target: "/a"})
	if j.State != JobRunning {
		t.Fatalf("state = %v", j.State)
	}
	j.apply(Event{Index: 0, Percent: 0.5})
	if j.Percent != 0.5 {
		t.Errorf("percent = %v", j.Percent)
	}
	j.apply(Event{Index: 0, State: JobFailed, Err: "boom"})
	if j.State != JobFailed || j.Err != "boom" {
		t.Fatalf("state=%v err=%q", j.State, j.Err)
	}
	// A progress line that arrives after the outcome must not undo it.
	j.apply(Event{Index: 0, Percent: 0.9, Log: "speed 2x"})
	if j.State != JobFailed {
		t.Errorf("late event changed state to %v", j.State)
	}
	if j.Percent != 1 {
		t.Errorf("finished job should read as complete, got %v", j.Percent)
	}
}

func TestPartPathKeepsExtension(t *testing.T) {
	cases := map[string]string{
		"/out/clip.mp4":   "/out/clip.part.mp4",
		"/out/photo.jpg":  "/out/photo.part.jpg",
		"/out/a b/c.webm": "/out/a b/c.part.webm",
		"/out/clip-1.mkv": "/out/clip-1.part.mkv",
	}
	for in, want := range cases {
		if got := partPathFor(in); got != want {
			t.Errorf("partPathFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunnerTotalsMatchJobs(t *testing.T) {
	requireTools(t)
	dir := t.TempDir()
	out := t.TempDir()
	files := []string{
		synthVideoTo(t, filepath.Join(dir, "a.mp4"), 320, 240, 2, true),
		synthVideoTo(t, filepath.Join(dir, "b.mp4"), 320, 240, 2, false),
	}
	var jobs []*Job
	for _, f := range files {
		jobs = append(jobs, makeJob(t, f))
	}
	s := defaultSettings()
	s.Output = OutputCustom
	s.OutPath = out
	s.Preset = 6

	r := NewRunner(jobs, s)
	r.Run(context.Background())
	for {
		ev, ok := r.Next()
		if !ok {
			break
		}
		jobs[ev.Index].apply(ev)
	}

	var want int64
	for _, j := range jobs {
		if j.State == JobDone {
			want += j.After
		}
	}
	if got := r.TotalAfter(); got != want {
		t.Errorf("TotalAfter() = %d, jobs sum to %d", got, want)
	}
	if r.TotalBefore <= 0 {
		t.Errorf("TotalBefore = %d", r.TotalBefore)
	}
	if !r.IsFinished() {
		t.Errorf("runner not marked done")
	}
}

// The event queue must never lose a terminal event, however much progress
// traffic the encoders produce. Dropping one made the summary report hundreds
// of files that had been written as canceled.
func TestSlowConsumerStillSeesEveryTerminalEvent(t *testing.T) {
	const n = 879

	jobs := make([]*Job, n)
	for i := range jobs {
		jobs[i] = &Job{Source: filepath.Join("/p", "f"+itoa(i)+".jpg"),
			Kind: KindImage, Before: 1000, State: JobQueued}
	}
	r := NewRunner(jobs, defaultSettings())

	go func() {
		for i := range jobs {
			r.emit(Event{Index: i, State: JobRunning})
			// Each ffmpeg -progress line becomes an event; a fast run makes
			// thousands of them for hundreds of files.
			for p := 1; p <= 25; p++ {
				r.emit(Event{Index: i, Percent: float64(p) / 25, Log: "speed 12x"})
			}
			r.emit(Event{Index: i, State: JobDone, After: 400, Target: "/o/f"})
		}
		r.finish()
	}()

	// A deliberately lazy consumer, like a UI redrawing between events.
	for {
		ev, ok := r.Next()
		if !ok {
			break
		}
		jobs[ev.Index].apply(ev)
	}

	for i, j := range jobs {
		if j.State != JobDone {
			t.Errorf("job %d (%s) ended as %v, want done", i, j.Source, j.State)
		}
		if j.After != 400 {
			t.Errorf("job %d recorded After = %d, want 400", i, j.After)
		}
	}
}

// A job whose Done arrives while progress is still queued must not be dragged
// back to running by a late progress report.
func TestStateChangeSupersedesQueuedProgress(t *testing.T) {
	jobs := []*Job{{Source: "/p/a.jpg", Kind: KindImage, Before: 100, State: JobQueued}}
	r := NewRunner(jobs, defaultSettings())

	r.emit(Event{Index: 0, Percent: 0.4})
	r.emit(Event{Index: 0, Percent: 0.7})
	r.emit(Event{Index: 0, State: JobDone, After: 50, Target: "/o/a.webp"})
	r.finish()

	for {
		ev, ok := r.Next()
		if !ok {
			break
		}
		jobs[ev.Index].apply(ev)
	}

	if jobs[0].State != JobDone {
		t.Errorf("state = %v, want done", jobs[0].State)
	}
	if jobs[0].Percent != 1 {
		t.Errorf("percent = %v, want 1", jobs[0].Percent)
	}
}

// A previous run's output folder must not be fed back in as new input, or
// every run would nest one more "compressed" level.
func TestCollectMediaSkipsOutputSubfolder(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{
		"a.png", "nested/b.png", "compressed/a.webp", "nested/compressed/b.webp",
	} {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := collectMedia(dir, "compressed")
	if err != nil {
		t.Fatalf("collectMedia: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2: %v", len(got), got)
	}
	for _, f := range got {
		if strings.Contains(f, string(filepath.Separator)+"compressed"+string(filepath.Separator)) {
			t.Errorf("output file leaked into the collection: %s", f)
		}
	}

	// With no skip name the output folder is walked like any other directory.
	all, err := collectMedia(dir, "")
	if err != nil {
		t.Fatalf("collectMedia (no skip): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d files, want 4: %v", len(all), all)
	}
}

// The output subfolder name is only skipped when that output mode is active.
func TestOutputSubdirOnlyForSubfolderMode(t *testing.T) {
	s := defaultSettings()
	if got := s.outputSubdir(); got != "compressed" {
		t.Fatalf("default outputSubdir = %q, want %q", got, "compressed")
	}
	s.Output = OutputAlongside
	if got := s.outputSubdir(); got != "" {
		t.Fatalf("alongside outputSubdir = %q, want empty", got)
	}
}

func TestCommonAncestor(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"/a/b/c", "/a/b/c", "/a/b/c"},
		{"/a/b/c", "/a/b/d", "/a/b"},
		{"/a/b/compressed", "/a/b/compressed/compressed", "/a/b/compressed"},
		{"/a/b", "/c/d", "/"},
	}
	for _, c := range cases {
		if got := commonAncestor(c.a, c.b); got != c.want {
			t.Errorf("commonAncestor(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// Pressing "c" must raise the question instead of silently starting a run, and
// only "y" may select the destructive video-replace mode.
func TestPressCAsksBeforeReplacing(t *testing.T) {
	m := NewModel(t.TempDir(), nil)
	m.tools = toolsMsg{ffmpeg: true, cwebp: true}
	m.ready, m.width, m.height = true, 100, 40
	src := filepath.Join(m.dir, "clip.mp4")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.sel[src] = true
	m.selSizes[src] = 1
	m.selOrder = []string{src}
	m.settings = defaultSettings()

	tm, _ := m.Update(keyMsg("c"))
	got := tm.(Model)
	if !got.askingReplace {
		t.Fatal("pressing c must ask before running")
	}
	if got.screen == scrProgress {
		t.Error("pressing c must not start a run before the user answers")
	}
	if got.settings.Output != OutputSubfolder {
		t.Errorf("asking changed the output mode to %v", got.settings.Output)
	}
	if !strings.Contains(got.viewBrowse(), "Replace the original videos") {
		t.Errorf("prompt not visible in the browse view:\n%s", got.viewBrowse())
	}

	// A key that is not an answer must neither run nor dismiss the question.
	tm, _ = got.Update(keyMsg("x"))
	mid := tm.(Model)
	if !mid.askingReplace || mid.screen == scrProgress {
		t.Errorf("unrelated key dismissed the prompt: asking=%v screen=%v", mid.askingReplace, mid.screen)
	}

	// "n" starts the run but keeps the original output mode.
	tm, _ = mid.Update(keyMsg("n"))
	plain := tm.(Model)
	if plain.askingReplace {
		t.Error("answering must clear the prompt")
	}
	if plain.screen != scrProgress {
		t.Errorf("n did not start the run: screen = %v", plain.screen)
	}
	if plain.settings.Output != OutputSubfolder {
		t.Errorf("n must keep the current output mode, got %v", plain.settings.Output)
	}

	// "y" starts the run with the video-replace mode.
	tm, _ = mid.Update(keyMsg("y"))
	repl := tm.(Model)
	if repl.screen != scrProgress {
		t.Errorf("y did not start the run: screen = %v", repl.screen)
	}
	if repl.settings.Output != OutputReplaceVideo {
		t.Errorf("y must select replace-videos-only, got %v", repl.settings.Output)
	}

	// esc backs out without running anything.
	tm, _ = got.Update(keyMsg("esc"))
	cancelled := tm.(Model)
	if cancelled.askingReplace {
		t.Error("esc must clear the prompt")
	}
	if cancelled.screen != scrBrowse {
		t.Errorf("esc must not start a run: screen = %v", cancelled.screen)
	}
}

// Canceled and failed jobs have no output size, so they must not drag the
// savings figure down.
func TestSummarySavingsIgnoreUnfinishedJobs(t *testing.T) {
	jobs := []*Job{
		{Source: "/p/a.png", Target: "/p/compressed/a.webp", State: JobDone, Before: 100, After: 10},
		{Source: "/p/b.png", Target: "/p/compressed/b.webp", State: JobDone, Before: 100, After: 20},
		{Source: "/p/c.mov", Target: "/p/compressed/c.mp4", State: JobCanceled, Before: 9000},
		{Source: "/p/d.mov", Target: "/p/compressed/d.mp4", State: JobFailed, Before: 9000, Err: "boom"},
	}
	m := NewModel(t.TempDir(), nil)
	m.jobs = jobs
	m.settings = defaultSettings()
	m.width = 100
	m.height = 40

	view := m.viewSummary()
	if strings.Contains(view, "9.2 kB") || strings.Contains(view, "98%") {
		t.Errorf("savings figure counted unfinished jobs:\n%s", view)
	}
	if !strings.Contains(view, "200 B → 30 B") {
		t.Errorf("expected only completed bytes in the savings line:\n%s", view)
	}
	if !strings.Contains(view, "1 failed · 1 canceled · 2 written") {
		t.Errorf("missing outcome counts:\n%s", view)
	}
	if !strings.Contains(view, "boom") {
		t.Errorf("missing failure reason:\n%s", view)
	}
}

// A skipped job measured the file that was already on disk, so it must keep
// that size; otherwise the summary reports 0 bytes out and invents 100% saved.
func TestSkippedJobKeepsExistingSize(t *testing.T) {
	j := &Job{State: JobQueued, Before: 1000}
	j.apply(Event{Index: 0, State: JobSkipped, After: 250, Target: "/p/a.webp"})

	if j.State != JobSkipped {
		t.Fatalf("state = %v, want skipped", j.State)
	}
	if j.After != 250 {
		t.Errorf("After = %d, want 250 (the size of the existing file)", j.After)
	}
	if j.Target != "/p/a.webp" {
		t.Errorf("Target = %q, want /p/a.webp", j.Target)
	}
	if j.Percent != 1 {
		t.Errorf("Percent = %v, want 1", j.Percent)
	}

	// A late progress event must not revive a finished job or its size.
	j.apply(Event{Index: 0, State: JobRunning, Percent: 0.2, After: 999})
	if j.State != JobSkipped || j.After != 250 {
		t.Errorf("late event changed a finished job: state=%v after=%d", j.State, j.After)
	}
}

// Failed and canceled jobs have no measured output, so they stay out of the
// savings maths entirely.
func TestUnfinishedJobsHaveNoAfter(t *testing.T) {
	for _, st := range []JobState{JobFailed, JobCanceled} {
		j := &Job{State: JobQueued, Before: 500}
		j.apply(Event{Index: 0, State: st, Err: "nope"})
		if j.After != 0 {
			t.Errorf("%v job recorded After = %d, want 0", st, j.After)
		}
		if j.Percent != 1 {
			t.Errorf("%v job Percent = %v, want 1", st, j.Percent)
		}
	}
}

// Two sources with the same stem (clip.mov, clip.mp4) both want
// compressed/clip.mp4. plan() must hand out distinct paths up front, or the
// workers overwrite each other's half-written file.
func TestPlanGivesEachJobItsOwnTarget(t *testing.T) {
	dir := t.TempDir()
	sources := []string{
		filepath.Join(dir, "clip.mov"),
		filepath.Join(dir, "clip.mp4"),
		filepath.Join(dir, "clip.m4v"),
	}
	var jobs []*Job
	for _, s := range sources {
		jobs = append(jobs, &Job{Source: s, Kind: KindVideo, State: JobQueued, Before: 100})
	}
	s := defaultSettings()
	s.Output = OutputSubfolder
	s.Overwrite = OverwriteReplace

	r := NewRunner(jobs, s)
	r.plan()

	seen := map[string]bool{}
	for i, pe := range r.plan_ {
		if pe.err != "" {
			t.Fatalf("job %d: unexpected plan error: %s", i, pe.err)
		}
		if pe.target == "" {
			t.Fatalf("job %d has no target", i)
		}
		if seen[pe.target] {
			t.Errorf("job %d reuses target %s", i, pe.target)
		}
		seen[pe.target] = true
	}
}

// With the skip policy, an already claimed target counts as existing, so the
// second job skips instead of writing over the first one's file.
func TestPlanSkipCountsInRunClaims(t *testing.T) {
	dir := t.TempDir()
	jobs := []*Job{
		{Source: filepath.Join(dir, "clip.mov"), Kind: KindVideo, State: JobQueued, Before: 100},
		{Source: filepath.Join(dir, "clip.mp4"), Kind: KindVideo, State: JobQueued, Before: 100},
	}
	s := defaultSettings()
	s.Output = OutputSubfolder
	s.Overwrite = OverwriteSkip

	r := NewRunner(jobs, s)
	r.plan()

	if r.plan_[0].skip {
		t.Errorf("first job should write, not skip")
	}
	if !r.plan_[1].skip {
		t.Errorf("second job should skip, target already claimed")
	}
	if r.plan_[0].target != r.plan_[1].target {
		t.Errorf("skip should report the conflicting target: %q vs %q",
			r.plan_[0].target, r.plan_[1].target)
	}
}

// A real collision end to end: nothing may fail and no output may be lost.
func TestRunWithCollidingStems(t *testing.T) {
	dir := t.TempDir()
	template := synthVideoTo(t, filepath.Join(dir, "template.mov"), 160, 120, 1, false)
	body, err := os.ReadFile(template)
	if err != nil {
		t.Fatal(err)
	}

	// Same stem for all three, different extensions on disk.
	names := []string{"clip.mov", "clip.mp4", "clip.m4v"}
	var jobs []*Job
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, makeJob(t, p))
	}

	s := defaultSettings()
	s.Output = OutputSubfolder
	s.Overwrite = OverwriteReplace
	s.Parallel = 3

	r := NewRunner(jobs, s)
	r.Run(context.Background())
	// Run is asynchronous: Next reports false when every worker is finished.
	for {
		ev, ok := r.Next()
		if !ok {
			break
		}
		r.jobs[ev.Index].apply(ev)
	}

	targets := map[string]bool{}
	for i, j := range jobs {
		if j.State == JobFailed || j.State == JobCanceled {
			t.Errorf("job %d (%s) ended as %v: %s", i, names[i], j.State, j.Err)
		}
		if j.State != JobDone {
			t.Errorf("job %d (%s) state = %v, want done", i, names[i], j.State)
		}
		if j.Target == "" {
			t.Errorf("job %d (%s) has no target", i, names[i])
			continue
		}
		if targets[j.Target] {
			t.Errorf("two jobs wrote the same target: %s", j.Target)
		}
		targets[j.Target] = true
		if st, err := os.Stat(j.Target); err != nil || st.Size() == 0 {
			t.Errorf("job %d target missing or empty: %s (%v)", i, j.Target, err)
		}
	}
	if len(targets) != 3 {
		t.Errorf("got %d distinct outputs, want 3: %v", len(targets), targets)
	}
}
