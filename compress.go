package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func itoa(n int) string { return strconv.Itoa(n) }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func wrap(v, n int) int {
	if n <= 0 {
		return 0
	}
	v %= n
	if v < 0 {
		v += n
	}
	return v
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return h
}

// ---------------------------------------------------------------- job model

type JobState int

const (
	JobQueued JobState = iota
	JobRunning
	JobDone
	JobSkipped
	JobFailed
	JobCanceled
)

func (s JobState) String() string {
	switch s {
	case JobQueued:
		return "queued"
	case JobRunning:
		return "running"
	case JobDone:
		return "done"
	case JobSkipped:
		return "skipped"
	case JobFailed:
		return "failed"
	case JobCanceled:
		return "canceled"
	}
	return "?"
}

type Job struct {
	Source  string
	Target  string
	Kind    Kind
	Before  int64
	After   int64
	State   JobState
	Percent float64
	Err     string
	Log     string
	started time.Time
	ended   time.Time
}

func (j *Job) Saved() int64 {
	if j.State != JobDone {
		return 0
	}
	return j.Before - j.After
}

func (j *Job) SavedPercent() float64 {
	if j.State != JobDone || j.Before <= 0 {
		return 0
	}
	return float64(j.Saved()) / float64(j.Before) * 100
}

// apply folds a runner event into the job. The UI calls this on the main
// goroutine, never from the worker pool.
func (j *Job) apply(ev Event) {
	if ev.Percent > 0 && !isFinished(j.State) {
		j.Percent = ev.Percent
	}
	if ev.Log != "" {
		j.Log = ev.Log
	}
	if ev.Target != "" {
		j.Target = ev.Target
	}
	if ev.Err != "" {
		j.Err = ev.Err
	}
	// A finished job never goes back to running, even if a stray progress
	// event arrives late.
	if isFinished(j.State) {
		j.close()
		return
	}
	if ev.State != JobQueued {
		j.State = ev.State
	}
	if !isFinished(j.State) {
		return
	}
	// Done and skipped both report a real output size: a skipped job measured
	// the file that was already there, so it can be counted in the savings
	// just like a freshly written one. Only a terminal event may set it, never
	// a late progress report.
	if (j.State == JobDone || j.State == JobSkipped) && ev.After > 0 {
		j.After = ev.After
	}
	j.close()
}

// close stamps the end of a job and fills in any missing timings.
func (j *Job) close() {
	if j.started.IsZero() {
		j.started = time.Now()
	}
	if j.ended.IsZero() {
		j.ended = time.Now()
	}
	j.Percent = 1
}

func isFinished(s JobState) bool {
	switch s {
	case JobDone, JobSkipped, JobFailed, JobCanceled:
		return true
	}
	return false
}

type Event struct {
	Index   int
	Percent float64
	State   JobState
	Err     string
	Log     string
	After   int64
	Target  string
}

// Runner executes jobs with a bounded worker pool and streams progress.
type Runner struct {
	jobs     []*Job
	settings Settings
	// queue holds every state change until the UI takes it, and wake pokes it
	// awake. It is unbounded on purpose: dropping a terminal event would make
	// applyRunDone report a file that was really written as canceled. Progress
	// reports are coalesced in pending instead, one per job.
	queue   []Event
	pending map[int]Event
	wake    chan struct{}
	// claimed and plan_ are written once by plan() before any worker starts
	// and only read afterwards, so they need no locking.
	claimed  map[string]bool
	plan_    []planEntry
	finished []bool
	// drained is set once every worker has reported and nothing is queued.
	drained bool
	mu      sync.Mutex

	// Totals are accumulated by the runner itself. Reading Job fields from a
	// worker goroutine would race with the UI, which owns those.
	TotalBefore int64
	totalAfter_ int64
	Done        bool
	Canceled    bool

	cancel context.CancelFunc
}

func NewRunner(jobs []*Job, s Settings) *Runner {
	r := &Runner{
		jobs:     jobs,
		settings: s,
		wake:     make(chan struct{}, 1),
		claimed:  map[string]bool{},
		plan_:    make([]planEntry, len(jobs)),
		pending:  map[int]Event{},
	}
	for _, j := range jobs {
		r.TotalBefore += j.Before
	}
	return r
}

// Run starts the worker pool. It returns as soon as the pool is launched;
// call r.Next until it reports false to know when everything finished.
// planEntry is the resolved destination for one job, decided before any
// worker starts so that two sources with the same stem cannot collide.
type planEntry struct {
	target string
	skip   bool
	size   int64
	err    string
}

// Run compresses every job and returns immediately; the work happens on a
// worker pool. The caller must keep calling Next until it returns false, which
// happens once every job has reached a terminal state.
func (r *Runner) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()

	r.plan()

	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < r.settings.parallel(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				if ctx.Err() != nil {
					r.markDone(i)
					r.emit(Event{Index: i, State: JobCanceled})
					continue
				}
				r.runOne(ctx, i)
				r.markDone(i)
			}
		}()
	}
	go func() {
	feed:
		for i := range r.jobs {
			select {
			case work <- i:
			case <-ctx.Done():
				break feed
			}
		}
		close(work)
		wg.Wait()
		// Anything never handed to a worker (we canceled mid-queue) is canceled too.
		for i := range r.jobs {
			if !r.isDone(i) {
				r.markDone(i)
				r.emit(Event{Index: i, State: JobCanceled})
			}
		}
		r.mu.Lock()
		r.Done = true
		r.mu.Unlock()
		cancel()
		r.finish()
	}()
}

func (r *Runner) markDone(i int) {
	r.mu.Lock()
	if r.finished == nil {
		r.finished = make([]bool, len(r.jobs))
	}
	if i < len(r.finished) {
		r.finished[i] = true
	}
	r.mu.Unlock()
}

func (r *Runner) isDone(i int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return i < len(r.finished) && r.finished[i]
}

// recordOutput adds a finished file's size to the runner's own total.
func (r *Runner) recordOutput(n int64) {
	r.mu.Lock()
	r.totalAfter_ += n
	r.mu.Unlock()
}

func (r *Runner) TotalAfter() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalAfter_
}

// emit queues an event for the UI. Nothing is ever dropped: a lost terminal
// event would leave its job looking unfinished, and applyRunDone would then
// report a file that really was written as canceled. Progress reports are
// coalesced instead, keeping only the newest per job, so a fast encoder cannot
// pile up thousands of stale readings.
func (r *Runner) emit(e Event) {
	r.mu.Lock()
	if e.State == JobQueued {
		// Only the newest reading per job is worth showing, so a fast encoder
		// cannot pile up thousands of stale ones.
		r.pending[e.Index] = e
	} else {
		// A state change supersedes any pending progress for the same job.
		delete(r.pending, e.Index)
		r.queue = append(r.queue, e)
	}
	r.mu.Unlock()
	r.poke()
}

func (r *Runner) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// finish marks the run complete. It is called only after the last event is
// queued, so a Next that is still waiting hands that event over before it
// starts reporting the run as finished.
func (r *Runner) finish() {
	r.mu.Lock()
	r.drained = true
	r.mu.Unlock()
	r.poke()
}

// Next returns the next event for the UI, blocking until one is available. It
// reports false once the run has finished and nothing is left to hand over.
func (r *Runner) Next() (Event, bool) {
	for {
		r.mu.Lock()
		if len(r.queue) > 0 {
			ev := r.queue[0]
			r.queue = r.queue[1:]
			r.mu.Unlock()
			return ev, true
		}
		// Queued state changes come first; pending progress is a fallback so a
		// long stretch of progress cannot starve a Done event.
		for i, ev := range r.pending {
			delete(r.pending, i)
			r.mu.Unlock()
			return ev, true
		}
		done := r.drained
		r.mu.Unlock()
		if done {
			return Event{}, false
		}
		<-r.wake
	}
}

// IsFinished reports whether every worker has reported in. The UI reads this
// from its own goroutine, so it goes through the mutex.
func (r *Runner) IsFinished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Done
}

func (r *Runner) Cancel() {
	r.mu.Lock()
	r.Canceled = true
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// bookmarkName returns a non-clobbering name and records how many were taken.
// It is only called from plan(), which runs single-threaded before any worker
// starts, so no locking is needed.
func (r *Runner) bookmarkName(desired string) string {
	base, ext := splitExt(desired)
	dir := filepath.Dir(desired)
	for n := 0; ; n++ {
		candidate := desired
		if n > 0 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, n, ext))
		}
		if r.claimed[candidate] {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			continue
		}
		r.claimed[candidate] = true
		return candidate
	}
}

// claim records a path as taken by this run and reports whether it was free.
func (r *Runner) claim(path string) bool {
	if r.claimed[path] {
		return false
	}
	r.claimed[path] = true
	return true
}

// plan resolves every job's output path up front, in order and single
// threaded. Doing this before the workers start is what keeps two sources
// with the same stem (clip.mov and clip.mp4) from racing for one target file.
func (r *Runner) plan() {
	for i, j := range r.jobs {
		dir := r.settings.outputDirFor(j.Source, j.Kind)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			r.plan_[i] = planEntry{target: j.Source, err: err.Error()}
			continue
		}

		ext := r.settings.ImageFormat().Ext
		if j.Kind == KindVideo {
			ext = r.settings.VideoFormat().Ext
		}
		desired := filepath.Join(dir, stemOf(j.Source)+ext)

		if r.settings.replacesOriginal(j.Kind) {
			// Replace-original: the result stays in the source's folder. A
			// source whose same-extension result lands on its own path is
			// replaced in place; anything else is written next to it and the
			// original is removed only after the new file is safely on disk.
			// A path already claimed by an earlier job (e.g. clip.mov and
			// clip.mp4 both resolving to clip.mp4) makes the later one skip
			// rather than race.
			if r.claimed[desired] {
				r.plan_[i] = planEntry{target: j.Source, skip: true, size: j.Before}
				continue
			}
			r.claimed[desired] = true
			r.plan_[i] = planEntry{target: desired}
			continue
		}

		if r.settings.Overwrite == OverwriteSkip {
			// A path already taken by an earlier job in this same run counts
			// as existing too, otherwise two jobs would write over each other.
			if st, err := os.Stat(desired); err == nil && r.claim(desired) {
				r.plan_[i] = planEntry{target: desired, skip: true, size: st.Size()}
				continue
			}
			if r.claimed[desired] {
				r.plan_[i] = planEntry{target: desired, skip: true}
				continue
			}
			r.claimed[desired] = true
			r.plan_[i] = planEntry{target: desired}
			continue
		}
		r.plan_[i] = planEntry{target: r.bookmarkName(desired)}
	}
}

func (r *Runner) runOne(ctx context.Context, i int) {
	j := r.jobs[i]
	pe := r.plan_[i]
	r.emit(Event{Index: i, State: JobRunning, Log: "starting…", Target: j.Source})

	if pe.err != "" {
		r.emit(Event{Index: i, State: JobFailed, Err: pe.err, Target: j.Source})
		return
	}
	if pe.skip {
		r.recordOutput(pe.size)
		r.emit(Event{Index: i, State: JobSkipped, After: pe.size, Target: pe.target})
		return
	}

	meta, ok := r.cacheGet(j.Source)
	if !ok || !meta.Probed {
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		meta = probe(pctx, j.Source)
		cancel()
		metaCache.put(j.Source, meta)
	}

	target := pe.target

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		r.emit(Event{Index: i, State: JobFailed, Err: err.Error(), Target: target})
		return
	}
	// Keep the real extension on the temp name: ffmpeg and cwebp both pick
	// their muxer from it, and a trailing ".part" would hide it.
	tmp := partPathFor(target)

	if j.Kind == KindImage && r.settings.ImageFormat().Name == "webp" {
		r.runCwebp(ctx, i, j.Source, tmp, meta)
	} else {
		r.runFfmpeg(ctx, i, j.Source, tmp, j.Kind, meta)
	}

	if ctx.Err() != nil {
		os.Remove(tmp)
		r.emit(Event{Index: i, State: JobCanceled, Target: target})
		return
	}

	st, err := os.Stat(tmp)
	if err != nil {
		r.emit(Event{Index: i, State: JobFailed, Err: "encoder produced no output", Target: target})
		return
	}
	if st.Size() == 0 {
		os.Remove(tmp)
		r.emit(Event{Index: i, State: JobFailed, Err: "encoder produced an empty file", Target: target})
		return
	}
	// Replacing the original is only worth it when the result is actually
	// smaller; a larger file keeps the original and is reported as a failure
	// so the user can raise the quality or pick another format.
	if r.settings.replacesOriginal(j.Kind) && st.Size() >= j.Before {
		os.Remove(tmp)
		r.emit(Event{Index: i, State: JobFailed, Err: fmt.Sprintf(
			"result is not smaller (%s → %s); original kept", humanSize(j.Before), humanSize(st.Size())), Target: target})
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		r.emit(Event{Index: i, State: JobFailed, Err: err.Error(), Target: target})
		return
	}
	r.recordOutput(st.Size())
	log := ""
	if r.settings.replacesOriginal(j.Kind) && target != j.Source {
		if err := os.Remove(j.Source); err != nil {
			log = "new file written; original kept: " + err.Error()
		}
	}
	r.emit(Event{Index: i, State: JobDone, After: st.Size(), Percent: 1, Target: target, Log: log})
}

var metaCache = newProbeCache()

func (r *Runner) cacheGet(path string) (Meta, bool) { return metaCache.get(path) }

func stemOf(p string) string {
	base := filepath.Base(p)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

func splitExt(p string) (string, string) {
	ext := filepath.Ext(p)
	return strings.TrimSuffix(filepath.Base(p), ext), ext
}

// partPathFor inserts ".part" before the extension so encoders still see it.
func partPathFor(target string) string {
	stem, ext := splitExt(target)
	return filepath.Join(filepath.Dir(target), stem+".part"+ext)
}

// scaleFilter builds a scale filter that never upscales and keeps aspect ratio.
func scaleFilter(longEdge int, meta Meta) string {
	if longEdge <= 0 || meta.LongEdge() <= 0 || meta.LongEdge() <= longEdge {
		return ""
	}
	return fmt.Sprintf("scale='if(gt(iw,ih),min(%d,iw),-2)':'if(gt(iw,ih),-2,min(%d,ih))':flags=lanczos",
		longEdge, longEdge)
}

// -------------------------------------------------------------- ffmpeg path

func (r *Runner) ffmpegArgs(src, dst string, kind Kind, meta Meta) []string {
	s := r.settings
	_, longEdge := s.ResolutionOpt()

	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-y", "-i", src,
		"-progress", "pipe:1", "-nostats",
	}

	if kind == KindImage {
		// ffmpeg treats a still image as a 1-frame video.
		args = append(args, "-frames:v", "1")
	}

	filter := scaleFilter(longEdge, meta)
	if filter != "" {
		args = append(args, "-vf", filter)
	}

	switch kind {
	case KindImage:
		switch s.ImageFormat().Name {
		case "jpeg":
			args = append(args, "-c:v", "mjpeg", "-q:v", jpegQscale(s.Quality), "-pix_fmt", "yuvj420p")
		default: // png
			args = append(args, "-c:v", "png", "-pix_fmt", "rgb24", "-compression_level", "9")
		}
	default:
		vf := s.VideoFormat()
		args = append(args, vf.Args(s.Quality, s.PresetName())...)
		switch s.Audio {
		case AudioStrip:
			args = append(args, "-an")
		case AudioAAC:
			args = append(args, aacArgs("128k")()...)
		default:
			// Only copy when the container can actually hold the codec.
			if meta.HasAudio && !vf.CopyableAudio[meta.AudioCodec] {
				args = append(args, vf.AudioFallback()...)
			} else {
				args = append(args, "-c:a", "copy")
			}
		}
		args = append(args, "-map", "0:v:0")
		if s.Audio != AudioStrip && meta.HasAudio {
			args = append(args, "-map", "0:a:0?")
		}
		args = append(args, dst)
		return args
	}

	args = append(args, dst)
	return args
}

// ffmpeg's -q:v for mjpeg uses 2 (best) .. 31 (worst).
func jpegQscale(quality int) string {
	q := clampInt(quality, 1, 100)
	v := 2 + (100-q)*29/99
	return itoa(clampInt(v, 2, 31))
}

func (r *Runner) runFfmpeg(ctx context.Context, i int, src, dst string, kind Kind, meta Meta) {
	args := r.ffmpegArgs(src, dst, kind, meta)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.emit(Event{Index: i, State: JobFailed, Err: err.Error()})
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, limit: 4000}

	if err := cmd.Start(); err != nil {
		r.emit(Event{Index: i, State: JobFailed, Err: err.Error()})
		return
	}
	// ffmpeg keeps printing "progress=end" after the process is gone, so the
	// reader goroutine is told to stop before we report the outcome.
	stopped := make(chan struct{})
	done := make(chan struct{})
	go r.readProgress(done, stopped, i, stdout, meta.Duration, kind)
	defer close(stopped)

	waitErr := cmd.Wait()
	close(done)

	if waitErr != nil {
		if ctx.Err() != nil {
			r.emit(Event{Index: i, State: JobCanceled})
			return
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		if len(msg) > 220 {
			msg = msg[:220] + "…"
		}
		r.emit(Event{Index: i, State: JobFailed, Err: msg})
		return
	}
	r.emit(Event{Index: i, Percent: 1})
}

func (r *Runner) readProgress(done, stopped chan struct{}, i int, rc io.ReadCloser, total time.Duration, kind Kind) {
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	frame := 0
	var totalSize int64
	speed := ""
	var processed time.Duration

	// report drops everything once the job reached its final state, so a
	// trailing "progress=end" can never resurrect a finished job.
	report := func(pct float64, log string) {
		select {
		case <-stopped:
			return
		default:
		}
		r.emit(Event{Index: i, Percent: pct, Log: log})
	}

	for sc.Scan() {
		select {
		case <-done:
			return
		default:
		}

		line := sc.Text()
		if !strings.Contains(line, "=") {
			continue
		}
		key, val, _ := strings.Cut(line, "=")
		val = strings.TrimSpace(val)
		switch key {
		case "out_time_ms", "out_time_us":
			// Despite the name, ffmpeg reports microseconds under both keys.
			if us, err := strconv.ParseInt(val, 10, 64); err == nil && us >= 0 {
				processed = time.Duration(us) * time.Microsecond
			}
		case "total_size":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil {
				totalSize = n
			}
		case "speed":
			speed = val
		case "frame":
			if n, err := strconv.Atoi(val); err == nil {
				frame = n
			}
		}

		pct := 0.0
		switch {
		case total > 0:
			pct = float64(processed) / float64(total)
		case kind == KindImage && frame > 0:
			pct = 1
		}
		pct = clampFloat(pct, 0, 1)
		log := ""
		if speed != "" {
			log = "speed " + speed
			if totalSize > 0 {
				log += " · " + humanSize(totalSize)
			}
		}
		report(pct, log)
	}
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------- cwebp path

func (r *Runner) cwebpArgs(src, dst string, meta Meta) []string {
	s := r.settings
	_, longEdge := s.ResolutionOpt()
	q := float64(clampInt(s.Quality, 1, 100))

	args := []string{"-quiet", "-q", strconv.FormatFloat(q, 'f', 0, 64)}
	if longEdge > 0 && meta.LongEdge() > longEdge {
		args = append(args, "-resize", itoa(longEdge), "0")
	}
	args = append(args, "-m", "6", "-mt", "-sharp_yuv", src, "-o", dst)
	return args
}

func (r *Runner) runCwebp(ctx context.Context, i int, src, dst string, meta Meta) {
	cmd := exec.CommandContext(ctx, "cwebp", r.cwebpArgs(src, dst, meta)...)
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, limit: 2000}
	cmd.Stdout = io.Discard

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			r.emit(Event{Index: i, State: JobCanceled})
			return
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		r.emit(Event{Index: i, State: JobFailed, Err: msg})
		return
	}
	r.emit(Event{Index: i, State: JobDone, Percent: 1})
}

type limitedWriter struct {
	w       io.Writer
	limit   int
	written int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.written < l.limit {
		room := l.limit - l.written
		chunk := p
		if len(chunk) > room {
			chunk = chunk[:room]
		}
		n, err := l.w.Write(chunk)
		l.written += n
		return len(p), err
	}
	return len(p), nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
