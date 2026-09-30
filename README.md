# compress-tui

Browse, select and batch-compress images and videos without leaving the
terminal. Written with Bubble Tea.

```
 ▐▛ Compress TUI                                2 selected · 1 image(s) · 1 video(s)
 /Users/you/Pictures/holiday
  [+] ▸ ..
 [x] ▣ beach.png          32.1 KB            261x245   │  details
 [x] ▶ sunset.mov         13.0 MB    1920x1080 00:06   │  name          beach.png
                                                       │  type          image
                                                       │  size          32.1 KB (32857 bytes)
                                                       │  will become
                                                       │    WebP · quality 78 · unchanged
```

## Requirements

| Tool     | Needed for                                  | Install               |
| -------- | ------------------------------------------- | --------------------- |
| Go 1.26+ | building                                    | `brew install go`     |
| ffmpeg   | all video, and JPEG/PNG images              | `brew install ffmpeg` |
| ffprobe  | resolution, duration, codec shown in the UI | comes with ffmpeg     |
| cwebp    | WebP images                                 | `brew install webp`   |

Run `compress-tui -check` to see what is available:

```
$ compress-tui -check
compress-tui 1.0.0

  ffmpeg  ok         video + jpeg/png
  ffprobe ok         metadata (resolution, duration)
  cwebp   ok         webp images

  ffmpeg encoders used by this app:
    libsvtav1
    libx264
    libx264rgb
    h264_videotoolbox
    libx265
    mjpeg
    png
    libvpx-vp9
```

## Build and run

```sh
go build -o compress-tui .
./compress-tui                  # start in the current directory
./compress-tui ~/Pictures       # start somewhere else
./compress-tui a.mov b.png      # preselect specific files
./compress-tui -d ~/Pictures    # same as the first form
```

`go install github.com/you/compress-tui@latest` also works if you move the
module to a repository.

> If you keep the source inside pCloud Drive, Dropbox, OneDrive or another
> sync client, the built binary may lose its executable bit. Build somewhere
> else (`go build -o /tmp/compress-tui .`) or restore it with
> `chmod +x compress-tui`.

## Keys

### Move around

| Key                 | Action                                             |
| ------------------- | -------------------------------------------------- |
| `↑` `↓` / `k` `j`   | move one line                                      |
| `PgUp` `PgDn`       | jump a page                                        |
| `g` / `G`           | first / last item                                  |
| `→` `l` `enter`     | open a folder, or select the file under the cursor |
| `←` `h` `backspace` | go to the parent folder                            |
| `/`                 | filter the list by name                            |
| `H`                 | show / hide dotfiles                               |
| `o`                 | open the current folder in Finder                  |

### Choose files

| Key         | Action                                                                                  |
| ----------- | --------------------------------------------------------------------------------------- |
| `space`     | select the item — on a folder this selects every image and video inside it, recursively |
| `a`         | select every image and video in this folder, recursively                                |
| `x`         | deselect everything currently visible                                                   |
| `n` / `esc` | clear the whole selection                                                               |

The output subfolder (`compressed` by default) is never descended into, so
running the tool twice in a row will not feed the previous results back in as
new input.

### Compress

| Key      | Action                                          |
| -------- | ----------------------------------------------- |
| `c`      | start compressing now with the current settings |
| `s`      | open settings, then start with `enter`          |
| `ctrl+c` | cancel everything; partial files are removed    |

## Settings

| Setting         | Options                                                              |
| --------------- | -------------------------------------------------------------------- |
| Image format    | WebP, JPEG, PNG                                                      |
| Video format    | MP4 · H.264, MP4 · H.265, MKV · AV1, WebM · VP9                      |
| Quality         | 1–100 (default 78), shown with a plain-language label                |
| Max resolution  | original, 3840p, 2560p, 1920p, 1600p, 1280p, 1080p, 720p, 480p, 360p |
| Video preset    | ultrafast…veryslow                                                   |
| Audio           | keep original, no audio, AAC 128k                                    |
| Output location | next to originals, `compressed` subfolder (default), custom path, replace original |
| Existing files  | skip existing, replace existing                                      |
| Parallel jobs   | 1–8 (default 2)                                                      |
| Process         | images and videos, images only, videos only                          |

Never upscales: a photo smaller than the resolution limit is written at its
own size. Audio is only copied when the container can carry the source codec
(H.265 into MP4, for example, is re-encoded rather than blindly muxed).

Every setting is shown with the exact command line it produces, so nothing is
a mystery.

## Output

By default each source is compressed into a `compressed` subfolder next to it
(`~/Pictures/holiday/compressed/`). Nothing is ever overwritten unless you
switch _Existing files_ to _replace existing_, and two sources with the same
name (`clip.mov` and `clip.mp4`) can never collide: every output path is
allocated before any encoding starts.

_Replace original_ writes the result into the source's own folder: same
extension means the file is replaced in place, otherwise the new file is
written next to it and the original is deleted only once the result is safely
on disk. The original is never lost to a bigger file — an output that is not
smaller aborts the job and keeps the original.

The summary reports only finished work, so a cancelled batch cannot inflate
the savings figure. Press `o` to open the output folder in Finder.

## Development

```sh
go vet ./...
go test -race ./...
```

The test suite covers the argument builders, the runner, cancellation, audio
container compatibility, output-name collisions, the savings maths and
end-to-end runs through the real `ffmpeg` and `cwebp` binaries. Tests that
need those tools skip themselves when they are missing.
