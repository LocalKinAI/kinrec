# Changelog

All notable changes to kinrec are documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning: [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

*Targeting the v0.1.0 tag.*

### Added

#### Audio — all three modes in v0.1
- `--audio` — captures **system audio** (what apps on screen play) via
  `SCStreamConfiguration.capturesAudio`. `excludesCurrentProcessAudio`
  prevents feedback loops when the recorder itself plays sound.
- `--mic` — captures **microphone** via `AVCaptureSession` +
  `AVCaptureAudioDataOutput` forced to 48 kHz stereo PCM.
- `--audio --mic` — captures **both sources mixed into a single AAC
  track** via an `AVAudioEngine` pipeline:
  - Two `AVAudioPlayerNode`s feed `AVAudioMixerNode` (main mixer).
  - Engine runs in **manual rendering mode** (no output device needed
    for CLI binaries).
  - 20 ms timer pulls mixed PCM chunks and forwards them to
    `AVAssetWriterInput`.
  - `AVAudioConverter` handles format conversion per-source so SCK's
    float32 non-interleaved and AVCaptureSession's int16 interleaved
    both reach the mixer in the canonical 48 kHz float32 format.
- `WithAudio` / `WithMic` / `WithMicDevice` Go options; all three can
  be set simultaneously.
- `kinrec.ListMics(ctx)` — enumerate input devices via
  `AVCaptureDeviceDiscoverySession` (built-in, external, USB, AirPods).
- `kinrec list mics` CLI subcommand.
- First use triggers macOS "Microphone" TCC prompt when `--mic` is
  enabled; permission is per-binary and persists across invocations.

#### Recording core

- `kinrec.NewRecorder(ctx, opts...)` — configure a recorder.
- `(*Recorder).Start(ctx)` / `(*Recorder).Stop(ctx)` — begin / finalize
  a recording. `Stop` blocks until the mp4 is fully written.
- `(*Recorder).Stats()` — live frame and audio-buffer counters for
  progress display.
- `kinrec.Record(ctx, duration, opts...)` — convenience wrapper for
  fixed-duration captures.
- `kinrec.ListDisplays(ctx)` — enumerate attached displays (standalone
  — does not require importing sckit-go).
- Functional options: `WithOutput`, `WithDisplay`, `WithResolution`,
  `WithFrameRate`, `WithAudio`, `WithCodec` (h264 / hevc),
  `WithContainer` (mp4 / mov).
- **Screen + system audio** captured via a single `SCStream` with
  `capturesAudio = YES` and `excludesCurrentProcessAudio = YES`
  (prevents feedback when recording a meeting app).
- **Hardware H.264 / HEVC encoding** via VideoToolbox through
  `AVAssetWriter` — no ffmpeg dependency.
- **AAC audio** at 128 kbps stereo 48 kHz.
- **`//go:embed` universal dylib** (arm64 + x86_64, ~130 KB) —
  downstream users never need `clang` / `CGO_ENABLED` / a C toolchain.
  Auto-extracts to `~/Library/Caches/kinrec/<content-hash>/` on first
  call.
- `DylibPath` override for contributors shipping patched dylibs.
- `ResolvedDylibPath()` for diagnostics.

### CLI — `cmd/kinrec`
- `kinrec record [flags]`:
  - `-o / --output PATH` (required)
  - `--duration D` (stop after; 0 = Ctrl+C to stop)
  - `--fps N` (default 60)
  - `--resolution WxH` (default: display native)
  - `--audio` (system audio, default off)
  - `--mic` (microphone, default off; mutually exclusive with `--audio` in v0.1)
  - `--mic-device ID` (specific mic's uniqueID)
  - `--codec h264 | hevc` (default h264)
  - `--format mp4 | mov` (default mp4)
  - `--display ID` (default: main)
  - Graceful stop on SIGINT / SIGTERM with mp4 finalization.
  - Live per-second progress: elapsed time, frames captured, audio
    buffer count.
- `kinrec list displays` — tabular display listing.
- `kinrec list mics` — tabular input-device listing, marks system default.
- `kinrec version` — kinrec version, Go toolchain, macOS version,
  resolved dylib path + size.
- `kinrec help <cmd>` — per-command long-form help.

### Dylib / ObjC
- 7 exported C-ABI functions: `kinrec_create` / `kinrec_start` /
  `kinrec_stop` / `kinrec_free` / `kinrec_stats` /
  `kinrec_list_displays` / `kinrec_list_mics`.
- `SCStreamFrameInfoStatus` filter — idle / blank frames are skipped
  so the AVAssetWriter only sees real-content buffers (same lesson
  learned in sckit-go's stability pass).
- `NSApplicationLoad()` one-shot initializer — preempts
  `CGS_REQUIRE_INIT` assertions that otherwise fire in CLI contexts
  without a WindowServer connection.
- Atomic first-sample session-start via `@synchronized` — protects
  the writer-session initialization race across the separate video
  and audio delivery queues.
- Reasonable bitrate default (`width * height * 5 * fps / 30`) —
  scales with both resolution and frame rate, clamped to 1.5 Mbps
  minimum.

### Documentation
- `README.md` — install, usage, library API, roadmap, architecture.
- `CHANGELOG.md` — this file.

### Tests
- **28 unit tests** (pure Go, no dylib needed): options defaults /
  overrides / composition, `cstr` / `contains` / `sliceStr` /
  `boolI` / `wrapErr` / permission-denied mapping, sentinel-error
  distinctness, Codec + Container enum wire values (ABI stability
  gate), Version constant.
- **10 integration tests** (build-tag `integration`, requires Screen
  Recording + Microphone permissions): `ListDisplays`, `ListMics`
  dedup, video-only record, system-audio record, mic-only record,
  **mixed `--audio --mic` record**, Start→Stats→Stop lifecycle with
  idempotent stop, "stop before start" error path,
  "output required" validation, `ResolvedDylibPath` populated.
- `go test -count=1 -cover` main package: **73.3%**.
- `staticcheck` + `golangci-lint` (v2, 9 linters): **0 warnings**.

### Verified

Smoke tests on M-series Mac, macOS 26.3:

- 5-second capture at 30 fps with `--audio`: **150 video frames + 252
  audio buffers**, valid MP4 per `ffprobe`:

  ```
  Stream #0: Video: h264 (High), yuv420p, 1920x1080, 894 kb/s, 30 fps
  Stream #1: Audio: aac (LC), 48000 Hz, stereo, 128 kb/s
  ```

- 5-second capture at 30 fps with `--mic`: **147 video frames + 474
  audio buffers**. `ffmpeg volumedetect` reports `mean_volume: -34.1 dB,
  max_volume: -15.9 dB` — real ambient room audio, not digital silence.

- 5-second capture at 30 fps with **`--audio --mic` (mixing)**, with
  `afplay` looping in the background to guarantee system audio
  content: **147 video frames + 726 audio buffers**. Single AAC audio
  track (not dual-track), 5.1 s duration.
  `volumedetect: mean_volume: -30.9 dB, max_volume: -5.2 dB` — the
  mixed signal carries the Ping peaks alongside mic ambient, and the
  combined level sits louder than either source alone.

- Output file 577 KB for 5 seconds → ~7 MB per minute at 1080p30 with
  audio, within expectation for H.264 at this bitrate.

### Fixed during v0.1 development (pre-release)

- **Mixer clock misalignment** — the `AVAudioEngine` output buffers
  were emitted with PTS starting at `kCMTimeZero`, but SCStream
  delivers video with PTS on the mach host clock (seconds since boot).
  A mix recording ended up with video "scheduled" ~10 days after the
  audio track — files played with audio but no visible video. Fixed
  by anchoring the mixer's output PTS to the first input sample
  buffer's PTS (which is on the same host clock as video), so all
  three tracks share a timeline. Regression test: every audio mode
  now checks that video and audio first-PTS are within 1.5s of each
  other.

### Known limitations

- Window / app / region targets not yet implemented (v0.2.0;
  will integrate with sckit-go `Target` types).
- No opt-in dual-track mode yet — when mixing, both sources collapse
  into one AAC track. `--audio --mic --dual-track` planned for v0.2.
- No click highlight, auto-zoom, or keystroke display (v0.3.0).
- No upload, GIF export, or trim UI (v0.4.0).
- Tested only on macOS 26.3 arm64 so far; Intel + macOS 14/15 verification pending CI.
