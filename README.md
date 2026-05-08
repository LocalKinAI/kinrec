# kinrec

**Mac screen + audio → mp4. One binary, `go install`, no Electron.**

`kinrec` is an open-source screen recorder for macOS. It's pure Go, no
cgo, no app bundle, no Electron, no subscription. One command:

```bash
go install github.com/LocalKinAI/kinrec/cmd/kinrec@latest
kinrec record -o demo.mp4 --audio --duration 30s
```

[![Go Reference](https://pkg.go.dev/badge/github.com/LocalKinAI/kinrec.svg)](https://pkg.go.dev/github.com/LocalKinAI/kinrec)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![macOS 14+](https://img.shields.io/badge/macOS-14+-blue.svg)](https://www.apple.com/macos/)

---

## Why

Recording your Mac screen today looks like this:

| Tool | Problem |
|---|---|
| QuickTime (built-in) | No hotkeys, no system audio, no shortcuts |
| `screencapture -v` CLI | **Can't record system audio** |
| OBS | 200 MB download, Mac is a second-class citizen |
| CleanShot X | **$29**, closed source |
| Screen Studio | **$89**, closed source |
| Loom | Subscription, login required |
| [Kap](https://github.com/wulkano/Kap) (open source) | **Abandoned since 2022**, Electron-based |
| [aperture-node](https://github.com/wulkano/aperture-node) | Abandoned, macOS 15+ broken |

Everyone recording a screencast either pays, juggles OBS, or watches
their recorder break with each macOS update.

**kinrec** is what happens when you start from scratch in 2026:

- **One command**: `go install` + `kinrec record -o x.mp4 --audio`.
- **5 MB binary**, not a 200 MB Electron bundle.
- **Hardware H.264 / HEVC** via VideoToolbox — no ffmpeg needed.
- **System audio** via ScreenCaptureKit's native audio path
  (macOS 14+; skips the feedback loop from your own process).
- **macOS 15 / 26 ready** — built on the modern `SCStream` +
  `AVAssetWriter` pipeline, not the deprecated `CGDisplayCreateImage`
  path that every other Go recorder (and many commercial ones) used.
- **Pure Go downstream**: the ObjC dylib is embedded via `//go:embed`
  and auto-extracted. Your users never need `clang`, `CGO_ENABLED`,
  or a Mac developer toolchain.
- **Built on [sckit-go](https://github.com/LocalKinAI/sckit-go)** —
  the same low-level ScreenCaptureKit bindings power both libraries.

---

## Install

```bash
go install github.com/LocalKinAI/kinrec/cmd/kinrec@latest
```

That's it. No Homebrew cask, no `.dmg`, no signing ceremony.

> **First run triggers macOS permission prompts**. Grant
> "Screen Recording" in System Settings → Privacy & Security →
> Screen Recording. If you use `--mic`, also grant "Microphone"
> on first use. Both permissions are per-binary and persist.

---

## Usage

### Record until Ctrl+C

```bash
kinrec record -o demo.mp4
```

### Timed recording

```bash
kinrec record -o tutorial.mp4 --duration 5m
```

### With system audio (what apps on screen play)

```bash
kinrec record -o meeting.mp4 --audio --duration 30m
```

Captures what Chrome / Spotify / Zoom / any app sends to the speakers.
Your microphone is **not** recorded — use `--mic` for that.

### With microphone (your voice — for tutorials)

```bash
kinrec record -o tutorial.mp4 --mic --duration 5m
```

Captures from the default input device. Triggers a one-time macOS
"Microphone" permission prompt on first use.

### Short-video preset (`--shorts`)

```bash
kinrec record --shorts -o clip.mp4 --duration 60s
```

One flag applies the X / TikTok / YT Shorts / Reels defaults:
1080×1920 vertical, 30 fps, `--audio` + `--mic` both on (mixed into a
single track via AVAudioEngine). Any explicit `--fps` / `--resolution`
/ `--audio` / `--mic` you pass still wins.

### Cursor highlight + click ripple (`--click-highlight`)

```bash
kinrec record --click-highlight -o tutorial.mp4 --mic --duration 2m
```

Draws a subtle white ring around the cursor at all times and a yellow
expanding ripple every time you left-click. Matches the effect Screen
Studio ($89) built its brand on. Requires no Accessibility permission.

Current v0.1 limits: works on Display-target capture at the display's
native resolution. `--click-highlight --shorts` or `--click-highlight
--resolution WxH` will misalign the overlay until coordinate mapping
is generalized in v0.2.

### Pick a specific mic

```bash
kinrec list mics
kinrec record -o demo.mp4 --mic --mic-device=<unique-id>
```

### Mix: system audio + mic together (the Loom default)

```bash
kinrec record -o tutorial.mp4 --audio --mic --duration 5m
```

Both sources flow into an `AVAudioEngine` pipeline (two player nodes
→ main mixer → tap), and the mixed PCM is encoded to a single AAC
track. Use this for:
- Tutorials where you narrate over a video playing on screen
- Zoom recordings that capture both yourself and the other side
- Podcasts that include desktop-app audio

### Custom framerate + resolution

```bash
kinrec record -o demo.mp4 --fps 30 --resolution 1280x720
```

### HEVC for ~30% smaller files

```bash
kinrec record -o demo.mov --codec hevc --format mov
```

### Pick a specific display

```bash
kinrec list displays
kinrec record -o ext.mp4 --display 2
```

### Full flag reference

```
kinrec record [flags]

  -o, --output PATH      Output path (required)
      --duration D       Stop after this long (30s, 5m, 1h); 0 = Ctrl+C
      --fps N            Target frame rate (default 60)
      --resolution WxH   Output size (default: display native)
      --audio            Capture system audio (what apps play)
      --mic              Capture microphone (your voice)
      --mic-device ID    Mic AVCaptureDevice uniqueID (see `kinrec list mics`)
      --codec h264|hevc  Video codec (default h264)
      --format mp4|mov   Container (default mp4)
      --display ID       Specific display ID (default: main)
      --shorts           Vertical 1080x1920 + 30fps + audio + mic preset
      --click-highlight  Overlay cursor ring + click ripple (for demos)
```

---

## Use as a library

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/LocalKinAI/kinrec"
)

func main() {
    ctx := context.Background()
    err := kinrec.Record(ctx, 10*time.Second,
        kinrec.WithOutput("/tmp/demo.mp4"),
        kinrec.WithAudio(true),
        kinrec.WithFrameRate(60),
    )
    if err != nil {
        log.Fatal(err)
    }
}
```

For fine-grained control (start/stop pairs, live stats):

```go
r, err := kinrec.NewRecorder(ctx,
    kinrec.WithOutput("demo.mp4"),
    kinrec.WithAudio(true),
)
r.Start(ctx)

go func() {
    for range time.Tick(time.Second) {
        s := r.Stats()
        log.Printf("frames=%d audio_bufs=%d", s.Frames, s.AudioBuffers)
    }
}()

<-someSignalToStop
r.Stop(ctx)
```

---

## Output

All recordings are real mp4 / mov files with:

- **Video**: H.264 High profile or HEVC, yuv420p, hardware-encoded via
  VideoToolbox (≈ 5 bits per pixel per 30fps-second baseline bitrate).
- **Audio** (when `--audio`): AAC-LC, 48 kHz stereo, 128 kbps.
- **Container**: mp4 (default, web-compatible) or mov.
- Playable in QuickTime, Safari, Chrome, VLC, Premiere, DaVinci, iMovie.

Verified with ffprobe on a 5-second `--audio` capture:

```
Stream #0: Video: h264 (High), yuv420p, 1920x1080, 894 kb/s, 30 fps
Stream #1: Audio: aac (LC), 48000 Hz, stereo, 128 kb/s
```

---

## Roadmap

### v0.1.0 — Recorder foundation (shipped 2026-04-23)
- [x] Display recording with system audio
- [x] Microphone recording (default input + device-selectable)
- [x] **`--audio --mic` mixed via `AVAudioEngine` into a single track**
- [x] `kinrec list mics` to enumerate input devices
- [x] H.264 + HEVC + mp4 + mov
- [x] Duration timer + Ctrl+C stop + live frame/audio-buffer counters
- [x] Hardware encoding via VideoToolbox
- [x] `//go:embed` universal dylib
- [x] `--shorts` preset for X / TikTok / YT Shorts / Reels publishing
- [x] `--click-highlight` cursor overlay for UI demos
- [x] v0.1.0 GitHub release

### v0.2.0 — Quality of life
- [ ] Window / app / region recording (via sckit-go target pass-through)
- [ ] Progress bar with estimated file size
- [ ] Automatic file-size limit (`--max-size 500M`)
- [ ] Dual-track mode as an opt-in (`--audio --mic --dual-track`) —
      useful for editors who want to adjust the two sources independently

### v0.3.0 — The magic features
- [ ] Click highlight / ripple effects (the thing CleanShot charges $29 for)
- [ ] Cursor smoothing
- [ ] Auto-zoom on click (the thing Screen Studio charges $89 for)
- [ ] Keystroke display (show `⌘C` on screen when pressed)
- [ ] Webcam picture-in-picture overlay

### v0.4.0 — Share + edit
- [ ] R2 / S3 upload with shareable link (`--upload --share`)
- [ ] GIF export (`--gif`)
- [ ] Timeline trim post-recording
- [ ] Automatic thumbnail generation

### v0.5.0 — UI (optional, if demand exists)
- [ ] Menu bar app (SwiftUI wrapper around the same binary)
- [ ] Global hotkey daemon

---

## Architecture

```
  Go code
    │
    │  purego.RegisterLibFunc (no cgo)
    ▼
  libkinrec_writer.dylib  (~190 KB universal arm64+x86_64, //go:embed'd)
    │
    ├─── SCStream (video + audio capture)
    └─── AVAssetWriter (H.264 / HEVC + AAC mux)
                 ↓
              out.mp4
```

The dylib is ~400 lines of ObjC (`objc/kinrec_writer.m`). It wraps
ScreenCaptureKit's async delivery with `dispatch_semaphore` + a
`SCStreamOutput` delegate, forwards each `CMSampleBuffer` to an
`AVAssetWriterInput`, and blocks on `finishWritingWithCompletionHandler`
when the caller stops.

Hardware encoding is provided by `AVAssetWriter` selecting VideoToolbox
automatically based on the codec key (`AVVideoCodecTypeH264` /
`AVVideoCodecTypeHEVC`). No external ffmpeg dependency.

---

## Requirements

- macOS 14 (Sonoma) or newer — `SCStreamConfiguration.capturesAudio`
  lands in macOS 13 but the full API matures in 14.
- Xcode Command Line Tools (contributors only; downstream users do not
  need them thanks to `//go:embed`).
- Go 1.22+.

---

## Development

```bash
git clone https://github.com/LocalKinAI/kinrec
cd kinrec

make help           # list targets
make dylib          # build universal libkinrec_writer.dylib
make cli            # build ./kinrec
make verify         # build + vet + record a 3-second clip
make install-cli    # go install ./cmd/kinrec to $GOBIN
```

### Layout

```
kinrec/
├── kinrec.go           # Load + Recorder + Record convenience
├── options.go          # functional options
├── cmd/kinrec/
│   ├── main.go         # subcommand dispatch
│   ├── record.go       # `kinrec record`
│   ├── list.go         # `kinrec list displays`
│   └── version.go
├── objc/
│   └── kinrec_writer.m # SCStream → AVAssetWriter pipeline
└── internal/dylib/
    ├── dylib.go        # //go:embed
    └── libkinrec_writer.dylib
```

---

## License

MIT — see [LICENSE](LICENSE).

Built by [LocalKin AI](https://localkin.ai) on top of
[sckit-go](https://github.com/LocalKinAI/sckit-go). Dogfood of the
primitive capture library — proves that sckit-go is strong enough to
build full products on.
