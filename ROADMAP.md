# kinrec Roadmap

Features live here until they ship. Order is intentional but not binding —
real user feedback reorders faster than this file.

## v0.2 — The Magic Features

Three polish features that move kinrec from "works" to "Screen Studio class".
All three are post-processing additions on top of the v0.1 pipeline — none
require changes to the existing recorder; they consume the finalized mp4
and either emit a new file or re-mux.

---

### 1. Click highlight + ripple

**Why it matters.** Screen Studio's signature feature (the product sells on
this at $89). Without it, a screen recording of a UI demo looks amateur —
viewers can't tell when the presenter clicked vs. just moved the cursor. With
it, every click is visually unmistakable.

**Visual target.**
```
Idle cursor:         [ 🖱 ]         small static ring around cursor (optional)

Click moment:        [ 🖱 ]         ripple: 3 expanding concentric
                    ⟰⟰⟰           rings fading over ~300ms
                  (semi-transparent)

Dragging:          [ 🖱━━━━━ ]      mild motion trail
```

**Approach.** Post-processing, not real-time.

1. **During recording**: a `CGEventTap` in the dylib records every mouse
   event (`kCGEventMouseMoved`, `kCGEventLeftMouseDown`,
   `kCGEventLeftMouseUp`, `kCGEventLeftMouseDragged`) with its timestamp
   and screen coordinates into a ring buffer. On `Stop`, the buffer flushes
   to a JSON sidecar file beside the mp4.
2. **Compose step** (new `kinrec compose` subcommand): reads the mp4 via
   `AVAssetReader`, reads the event log, uses `AVVideoComposition` with a
   custom `AVVideoCompositing` implementation to draw `CIFilter`-based
   overlays at the correct timestamps, writes a new mp4 via
   `AVAssetWriter`.

Post-processing is preferred over real-time because:
- Parameters (ring color, ripple duration, fade curve) can be tweaked
  without re-recording.
- The recorder's hot path stays simple and fast.
- Effect quality doesn't depend on consumer's CPU headroom during capture.

**Implementation sketch.**
```
objc/kinrec_cursor_tap.m            ~300 lines
  - CGEventTapCreate listening for mouse events
  - ring buffer (1024 events, overwrite on overflow)
  - export to flat JSON array on recorder.Stop

objc/kinrec_compositor.m            ~400 lines
  - AVAssetReader → frame-by-frame CVPixelBuffer
  - for each frame: binary-search event log for relevant events
  - CIFilter("CIRadialGradient") or custom to render ring + ripple
  - AVAssetWriter → new mp4

cmd/kinrec/compose.go               ~80 lines
  - kinrec compose --input in.mp4 --events in.json --output out.mp4
  - options: --click-style minimal | screen-studio | neon
```

**Effort estimate.** 2-3 days. Hardest part is tuning the `CIFilter` so it
looks professional, not cheap. Ship with one preset (`screen-studio`),
add more in v0.2.1+.

**Permission note.** `CGEventTap` requires Accessibility permission. First
`--click-highlight` record prompts for it. If denied, the record still
succeeds but without click data; compose step emits a warning and writes
the mp4 unmodified.

---

### 2. Auto-GIF

**Why it matters.** Twitter, Slack, GitHub issues, bug reports — most
channels where developers share screen recordings accept GIFs, not mp4.
CleanShot's one-click-to-GIF is one of its most-cited features. `ffmpeg
-i in.mp4 ... out.gif` works but forces users to install ffmpeg and
memorize filter chains.

**Interface.**
```bash
# GIF-only (no mp4 emitted)
kinrec record --gif -o demo.gif --duration 10s

# mp4 + GIF side-by-side
kinrec record --gif-also -o demo.mp4 --duration 10s
# → produces demo.mp4 AND demo.gif

# GIF parameters
kinrec record --gif -o demo.gif --gif-fps 15 --gif-width 800 --duration 10s
```

Defaults: 15 fps, max width 800 px, quality ~65 (Go image/gif's auto
palette quantization).

**Approach.** Pure Go encoder + ObjC frame extractor. No ffmpeg dependency.

1. **Frame extraction** (dylib): `AVAssetReader` with an
   `AVAssetReaderTrackOutput` set to `kCVPixelFormatType_32BGRA` streams
   frames. Skip frames as needed to hit target fps. Downsample via
   `CIImage` with `Lanczos` scaling to target width. Emit raw BGRA bytes
   per frame over a C callback or a shared buffer.
2. **GIF encoding** (Go): stdlib `image/gif`'s `Encoder` accepts
   `*image.Paletted` frames. Go 1.22's `image/draw.FloydSteinberg` +
   `image/color.Palette` does acceptable auto-quantization. Loop forever.

Output file size estimate: 10 seconds of 800×450 at 15 fps with 128-color
palette ≈ 2-4 MB. Acceptable for Slack (upload cap: 1 GB).

**Bonus.** With this pipeline in place, `--webp` (animated WebP) is a
10-line addition using `golang.org/x/image/webp` — same frame extraction,
different encoder. WebP gives ~3x better quality at ~1/4 size vs GIF.

**Implementation sketch.**
```
objc/kinrec_frame_reader.m          ~150 lines
  - AVAssetReader with video track
  - CIImage Lanczos downsample
  - BGRA byte export

kinrec/gif.go                       ~100 lines
  - BGRA → image.RGBA → image.Paletted (FloydSteinberg)
  - image/gif.Encoder.Encode

cmd/kinrec/record.go flag additions
  --gif / --gif-also / --gif-fps N / --gif-width N
```

**Effort estimate.** 1-2 days. stdlib `image/gif` is the secret weapon.

---

### 3. Auto-trim silence

**Why it matters.** Every tutorial recording starts with 2-3 seconds of
"press record → settle into seat → start talking". Every tutorial ends
with "stop talking → fumble to Ctrl+C". Users want the middle. Trimming
manually is a chore.

**Interface.**
```bash
# Off by default — safe default, in case someone's recording a silent
# demo where the opening silence is intentional
kinrec record --audio --mic -o out.mp4 --duration 30s

# Opt in
kinrec record --auto-trim --audio --mic -o out.mp4 --duration 30s
```

Documented behavior:
- Scan the audio track at 100 ms resolution.
- Compute RMS per window.
- Find first window with RMS > −40 dB sustained for 500 ms → `t_start`.
- Find last such window (scanning from end) → `t_end`.
- If `t_start > 0.5s` or `(duration - t_end) > 0.5s`, re-mux the mp4
  keeping only `[t_start, t_end]`.
- If no qualifying windows (entirely silent recording), leave the file
  untouched and emit a warning.

**Approach.** Post-processing using `AVAssetExportSession` with a
`timeRange` parameter — Apple's native trimming API, no re-encode needed
(just container-level trim, fast).

**Implementation sketch.**
```
objc/kinrec_trim.m                  ~120 lines
  - AVAssetReader reads audio track
  - 100ms windows, compute RMS
  - find t_start, t_end, return [CMTime, CMTime]

objc/kinrec_export.m                 ~80 lines
  - AVAssetExportSession with timeRange
  - AVAssetExportPresetPassthrough (no re-encode)

recorder.go Stop addition
  if cfg.autoTrim {
      t0, t1 := scanSilence(output)
      if t0 > 0 || t1 < duration {
          exportTrimmed(output, t0, t1)
      }
  }
```

**Effort estimate.** 1 day. No re-encode means it's fast even for long
recordings — a 30-minute meeting trims in seconds.

---

## v0.2 Summary

```
Feature                          Days    Priority*
──────────────────────────────────────────────────
Post-processing framework        1       prerequisite
--click-highlight                2-3     S
--gif / --gif-also               1-2     S
--auto-trim                      1       A
--webp (bonus, after --gif)      0.5     B
--mp4-small (auto downsize)      0.5     B
──────────────────────────────────────────────────
Total                            ~7-9 days
```

*Priority is speculative — reshuffle based on real user feedback after v0.1
goes public.

---

## v0.3+ — Out-of-scope for now

Flagged for later consideration, not committed:

- **Webcam PIP** — round overlay of camera feed (Loom / Screen Studio
  standard). AVFoundation has a `AVCaptureDevice` for webcam; compositor
  would draw a rounded CIImage in a corner. ~3 days.
- **Keystroke display** — when user presses `⌘C`, overlay "⌘C" on screen
  bottom for 1s. Uses `CGEventTap` for keys (same permission as clicks).
  ~2 days.
- **Background blur for webcam** — uses Vision framework's person
  segmentation. Depends on vision-go library (future KinKit component).
- **Cloud upload** — `--upload --share` emits a URL. Needs object-store
  credentials; stays out of scope until kinrec has external users who
  ask for it.
- **Timeline editor (GUI)** — out of scope for CLI-first tool. Defer to
  v1.0 or a separate `kinrec-studio` project.

---

## Decision-Forcing Criteria

For each proposed feature, we ship only if **at least one** is true:

1. A real user filed an issue asking for it.
2. We hit a concrete pain point using kinrec ourselves for 5+ real demos.
3. Screen Studio / CleanShot / Loom all have it, competitive gap is
   blocking adoption.

If none of the three applies, the feature waits.

---

## One-Line Log

- **2026-04-22** — v0.1.0-dev shipped private. ROADMAP captured.
- **2026-04-22 (late)** — `--shorts` preset + `--click-highlight` overlay
  shipped as late additions to v0.1. Motivation: user had immediate
  demo-recording needs; shipped by compositing onto CVPixelBuffer via
  CGBitmapContext (no Accessibility permission, no post-processing).
  Remaining magic features (auto-GIF, auto-trim) still deferred to v0.2.
