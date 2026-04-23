// Package kinrec records the macOS screen (and optionally system
// audio) into an H.264/HEVC .mp4 or .mov file.
//
// Built on top of ScreenCaptureKit + AVAssetWriter via a small embedded
// ObjC dylib. Pure Go on the calling side (no cgo needed downstream).
//
// Quick start:
//
//	ctx := context.Background()
//	r, _ := kinrec.NewRecorder(ctx,
//	    kinrec.WithOutput("/tmp/demo.mp4"),
//	    kinrec.WithAudio(true),
//	    kinrec.WithFrameRate(60),
//	)
//	r.Start(ctx)
//	time.Sleep(10 * time.Second)
//	r.Stop(ctx)
//
// For duration-bounded recordings use the blocking helper:
//
//	kinrec.Record(ctx, 10*time.Second,
//	    kinrec.WithOutput("/tmp/demo.mp4"),
//	    kinrec.WithAudio(true),
//	)
//
// # Requirements
//
// macOS 14 (Sonoma) or newer. First run triggers the "Screen Recording"
// TCC prompt (and "Microphone" if --mic is set in a future release).
package kinrec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/LocalKinAI/kinrec/internal/dylib"
	"github.com/ebitengine/purego"
)

// Version is the semver tag for this build. Updated per release.
const Version = "0.1.0"

// DylibPath optionally overrides the embedded dylib. Leave empty
// (default) to use the bundled copy auto-extracted to the user cache.
var DylibPath = ""

// Codec selects the video encoder.
type Codec int

const (
	// CodecH264 is broadly compatible baseline. Default.
	CodecH264 Codec = 0
	// CodecHEVC (H.265) is ~30% smaller at equivalent quality, but not
	// every consumer plays it without extras.
	CodecHEVC Codec = 1
)

// Container selects the output file format.
type Container int

const (
	// ContainerMP4 is the web-friendly default.
	ContainerMP4 Container = 0
	// ContainerMOV is Apple's native container; slightly better metadata
	// support and better for long captures that might be edited later.
	ContainerMOV Container = 1
)

// ─── Sentinel errors ─────────────────────────────────────────

// ErrPermissionDenied is returned when macOS Screen Recording (and, for
// future mic capture, Microphone) permission has not been granted.
var ErrPermissionDenied = errors.New("kinrec: screen recording permission denied")

// ErrAlreadyStarted is returned when Start is called twice.
var ErrAlreadyStarted = errors.New("kinrec: recorder already started")

// ErrNotStarted is returned when Stop is called before Start.
var ErrNotStarted = errors.New("kinrec: recorder not started")

// createParams mirrors the ObjC kinrec_params_t struct exactly.
// 48 bytes, naturally aligned. DO NOT reorder fields without also
// updating objc/kinrec_writer.m.
type createParams struct {
	Width           int32
	Height          int32
	FPS             int32
	CaptureAudio    int32
	CaptureMic      int32
	Codec           int32
	Container       int32
	CursorHighlight int32
	MicDeviceID     unsafe.Pointer
	OutputPath      unsafe.Pointer
}

// ─── Dylib resolution + function pointers ───────────────────

var (
	loadOnce sync.Once
	loadErr  error

	createFn       func(uint32, unsafe.Pointer, unsafe.Pointer, int32) uintptr
	startFn        func(uintptr, unsafe.Pointer, int32) int32
	stopFn         func(uintptr, unsafe.Pointer, int32) int32
	statsFn        func(uintptr, unsafe.Pointer, unsafe.Pointer)
	freeFn         func(uintptr)
	listDisplaysFn func(unsafe.Pointer, int32, unsafe.Pointer, int32) int32
	listMicsFn     func(unsafe.Pointer, int32, unsafe.Pointer, int32, unsafe.Pointer, unsafe.Pointer, int32) int32

	resolvedPath string
)

// ResolvedDylibPath returns the filesystem path Dlopen'd by the current
// process. Empty until the first Load call.
func ResolvedDylibPath() string { return resolvedPath }

// Load explicitly loads the companion dylib. Idempotent; the first
// public call triggers it implicitly.
func Load() error {
	loadOnce.Do(func() {
		if runtime.GOOS != "darwin" {
			loadErr = fmt.Errorf("kinrec: macOS only (runtime.GOOS=%s)", runtime.GOOS)
			return
		}
		path, err := resolveDylib()
		if err != nil {
			loadErr = err
			return
		}
		resolvedPath = path
		h, err := purego.Dlopen(path, purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("kinrec: dlopen %q: %w", path, err)
			return
		}
		purego.RegisterLibFunc(&createFn, h, "kinrec_create")
		purego.RegisterLibFunc(&startFn, h, "kinrec_start")
		purego.RegisterLibFunc(&stopFn, h, "kinrec_stop")
		purego.RegisterLibFunc(&statsFn, h, "kinrec_stats")
		purego.RegisterLibFunc(&freeFn, h, "kinrec_free")
		purego.RegisterLibFunc(&listDisplaysFn, h, "kinrec_list_displays")
		purego.RegisterLibFunc(&listMicsFn, h, "kinrec_list_mics")
	})
	return loadErr
}

func resolveDylib() (string, error) {
	if DylibPath != "" {
		if _, err := os.Stat(DylibPath); err != nil {
			return "", fmt.Errorf("kinrec: DylibPath override %q: %w", DylibPath, err)
		}
		return DylibPath, nil
	}
	return extractEmbedded()
}

func extractEmbedded() (string, error) {
	if len(dylib.Bytes) == 0 {
		return "", errors.New("kinrec: embedded dylib empty (run make dylib)")
	}
	h := sha256.Sum256(dylib.Bytes)
	hashPrefix := hex.EncodeToString(h[:8])
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("kinrec: cache dir: %w", err)
	}
	dir := filepath.Join(base, "kinrec", hashPrefix)
	target := filepath.Join(dir, "libkinrec_writer.dylib")
	if existing, err := os.ReadFile(target); err == nil && len(existing) == len(dylib.Bytes) {
		return target, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("kinrec: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "libkinrec-*.dylib.tmp")
	if err != nil {
		return "", fmt.Errorf("kinrec: create tmp: %w", err)
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	if _, err := tmp.Write(dylib.Bytes); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Chmod(0o755); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return target, nil
}

// ─── Display enumeration (stand-alone; does not require sckit-go) ───

// Display describes an attached display.
type Display struct {
	ID     uint32
	Width  int
	Height int
}

// ListDisplays enumerates attached displays.
func ListDisplays(ctx context.Context) ([]Display, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type raw struct {
		ID   uint32
		W, H int32
	}
	buf := make([]raw, 16)
	errBuf := make([]byte, 256)
	n := listDisplaysFn(unsafe.Pointer(&buf[0]), int32(len(buf)),
		unsafe.Pointer(&errBuf[0]), int32(len(errBuf)))
	if n < 0 {
		return nil, wrapErr("list_displays", cstr(errBuf))
	}
	out := make([]Display, n)
	for i := int32(0); i < n; i++ {
		out[i] = Display{ID: buf[i].ID, Width: int(buf[i].W), Height: int(buf[i].H)}
	}
	return out, nil
}

// ─── Microphone enumeration ──────────────────────────────────

// Mic describes an available audio input device.
type Mic struct {
	UniqueID  string // pass to WithMicDevice to force selection
	Name      string // user-visible name, e.g. "MacBook Pro Microphone"
	IsDefault bool
}

// ListMics enumerates built-in and external microphones visible to
// AVCaptureDeviceDiscoverySession.
func ListMics(ctx context.Context) ([]Mic, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type raw struct {
		IsDefault uint32
		IDOff     uint32
		IDLen     uint32
		NameOff   uint32
		NameLen   uint32
	}
	const maxMics = 32
	const poolCap = 16 * 1024
	buf := make([]raw, maxMics)
	pool := make([]byte, poolCap)
	var used int32
	errBuf := make([]byte, 256)
	n := listMicsFn(
		unsafe.Pointer(&buf[0]), int32(maxMics),
		unsafe.Pointer(&pool[0]), int32(poolCap),
		unsafe.Pointer(&used),
		unsafe.Pointer(&errBuf[0]), int32(len(errBuf)),
	)
	if n < 0 {
		return nil, wrapErr("list_mics", cstr(errBuf))
	}
	out := make([]Mic, n)
	for i := int32(0); i < n; i++ {
		out[i] = Mic{
			UniqueID:  sliceStr(pool, buf[i].IDOff, buf[i].IDLen),
			Name:      sliceStr(pool, buf[i].NameOff, buf[i].NameLen),
			IsDefault: buf[i].IsDefault != 0,
		}
	}
	return out, nil
}

func sliceStr(pool []byte, off, length uint32) string {
	end := off + length
	if int(end) > len(pool) || length == 0 {
		return ""
	}
	return string(pool[off:end])
}

// ─── Recorder ────────────────────────────────────────────────

// Recorder is a live macOS screen + audio capture session that writes
// to a file. Create via NewRecorder; call Start then Stop.
type Recorder struct {
	handle uintptr

	mu      sync.Mutex
	started bool
	stopped bool
}

// NewRecorder creates a configured recorder. Capture starts only when
// Start is called. The output file is created (or truncated) at this
// point, not at Start.
func NewRecorder(ctx context.Context, opts ...Option) (*Recorder, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.output == "" {
		return nil, fmt.Errorf("kinrec: WithOutput is required")
	}

	// Null-terminate strings for the C side.
	pathBytes := append([]byte(cfg.output), 0)
	var micIDBytes []byte
	if cfg.micDeviceID != "" {
		micIDBytes = append([]byte(cfg.micDeviceID), 0)
	} else {
		micIDBytes = []byte{0}
	}
	errBuf := make([]byte, 256)

	// Pack params into a single struct — purego's reflect-based ABI
	// struggles with function signatures beyond ~12 args on some Go
	// versions. Packing mirrors the kinrec_params_t layout in
	// objc/kinrec_writer.m (48 bytes, naturally aligned).
	params := createParams{
		Width:           int32(cfg.width),
		Height:          int32(cfg.height),
		FPS:             int32(cfg.fps),
		CaptureAudio:    boolI(cfg.captureAudio),
		CaptureMic:      boolI(cfg.captureMic),
		Codec:           int32(cfg.codec),
		Container:       int32(cfg.container),
		CursorHighlight: boolI(cfg.cursorHighlight),
		MicDeviceID:     unsafe.Pointer(&micIDBytes[0]),
		OutputPath:      unsafe.Pointer(&pathBytes[0]),
	}
	// Keep pathBytes + micIDBytes alive across the dylib call — Go's
	// GC won't move them while they're referenced from the stack.
	_ = pathBytes
	_ = micIDBytes

	h := createFn(
		cfg.displayID,
		unsafe.Pointer(&params),
		unsafe.Pointer(&errBuf[0]), int32(len(errBuf)),
	)
	if h == 0 {
		return nil, wrapErr("create", cstr(errBuf))
	}
	r := &Recorder{handle: h}
	runtime.SetFinalizer(r, func(r *Recorder) {
		if !r.stopped && r.handle != 0 {
			freeFn(r.handle)
		}
	})
	return r, nil
}

// Start begins capture. Blocks until the SCStream is delivering
// (typically ~150ms).
func (r *Recorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrAlreadyStarted
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	errBuf := make([]byte, 256)
	n := startFn(r.handle, unsafe.Pointer(&errBuf[0]), int32(len(errBuf)))
	if n != 0 {
		return wrapErr("start", cstr(errBuf))
	}
	r.started = true
	return nil
}

// Stop flushes and finalizes the output file. Blocks until the mp4 is
// fully written (typically <500ms for short recordings, longer for
// multi-minute ones while the writer drains its AAC encoder).
func (r *Recorder) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		return ErrNotStarted
	}
	if r.stopped {
		return nil
	}
	errBuf := make([]byte, 256)
	n := stopFn(r.handle, unsafe.Pointer(&errBuf[0]), int32(len(errBuf)))
	if n != 0 {
		return wrapErr("stop", cstr(errBuf))
	}
	r.stopped = true
	runtime.SetFinalizer(r, nil)
	freeFn(r.handle)
	r.handle = 0
	return nil
}

// Stats is live progress information about a running recorder.
type Stats struct {
	Frames       int64
	AudioBuffers int64
}

// Stats returns the current frame + audio buffer counts for progress UI.
// Safe to call from any goroutine.
func (r *Recorder) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handle == 0 {
		return Stats{}
	}
	var frames, audio int64
	statsFn(r.handle, unsafe.Pointer(&frames), unsafe.Pointer(&audio))
	return Stats{Frames: frames, AudioBuffers: audio}
}

// ─── Convenience ─────────────────────────────────────────────

// Record captures for the given duration and finalizes the output
// file. Equivalent to NewRecorder + Start + time.Sleep + Stop; returns
// only when the file is fully on disk.
func Record(ctx context.Context, duration time.Duration, opts ...Option) error {
	r, err := NewRecorder(ctx, opts...)
	if err != nil {
		return err
	}
	if err := r.Start(ctx); err != nil {
		_ = r.Stop(context.Background())
		return err
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		// Caller canceled — still flush whatever we captured so far.
		_ = r.Stop(context.Background())
		return ctx.Err()
	case <-timer.C:
		return r.Stop(ctx)
	}
}

// ─── Helpers ─────────────────────────────────────────────────

func boolI(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func wrapErr(op, raw string) error {
	if raw == "" {
		return fmt.Errorf("kinrec: %s: unknown error", op)
	}
	// Simple heuristic TCC detection; refined over time.
	if contains(raw, "not authorized") || contains(raw, "denied") ||
		contains(raw, "permission") {
		return fmt.Errorf("kinrec: %s: %w (%s)", op, ErrPermissionDenied, raw)
	}
	return fmt.Errorf("kinrec: %s: %s", op, raw)
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
