//go:build integration

// Integration tests — require the dylib and the "Screen Recording"
// TCC permission. Mic-touching tests additionally need "Microphone".
//
// Run:
//
//	go test -tags integration ./...
//
// Tests are behind a build tag because GitHub Actions macOS runners
// cannot grant TCC programmatically; letting them run untagged would
// hang on the first permission prompt.
package kinrec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ─── Enumeration ─────────────────────────────────────────────

func TestIntListDisplays(t *testing.T) {
	ds, err := ListDisplays(shortCtx(t))
	if err != nil {
		t.Fatalf("ListDisplays: %v", err)
	}
	if len(ds) == 0 {
		t.Fatal("expected ≥1 display")
	}
	for i, d := range ds {
		if d.ID == 0 {
			t.Errorf("display[%d].ID = 0", i)
		}
		if d.Width <= 0 || d.Height <= 0 {
			t.Errorf("display[%d] size = %dx%d", i, d.Width, d.Height)
		}
	}
}

func TestIntListMics(t *testing.T) {
	ms, err := ListMics(shortCtx(t))
	if err != nil {
		t.Fatalf("ListMics: %v", err)
	}
	if len(ms) == 0 {
		t.Skip("no mics on this machine — skipping")
	}
	defaults := 0
	for _, m := range ms {
		if m.UniqueID == "" {
			t.Errorf("mic %q has empty uniqueID", m.Name)
		}
		if m.IsDefault {
			defaults++
		}
	}
	if defaults > 1 {
		t.Errorf("multiple mics marked as default: %d", defaults)
	}
}

// ─── Recording — no audio (video-only) ───────────────────────

func TestIntRecordVideoOnly(t *testing.T) {
	out := filepath.Join(t.TempDir(), "video.mp4")
	ctx := shortCtx(t)
	err := Record(ctx, 2*time.Second,
		WithOutput(out),
		WithFrameRate(30),
	)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() < 10*1024 {
		t.Errorf("video-only mp4 suspiciously small: %d bytes", info.Size())
	}
}

// ─── Recording — system audio only ───────────────────────────

func TestIntRecordSystemAudio(t *testing.T) {
	out := filepath.Join(t.TempDir(), "audio.mp4")
	err := Record(shortCtx(t), 2*time.Second,
		WithOutput(out),
		WithFrameRate(30),
		WithAudio(true),
	)
	if err != nil {
		t.Fatalf("Record --audio: %v", err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() < 10*1024 {
		t.Errorf("audio mp4 too small or missing: %v", err)
	}
}

// ─── Recording — mic only (needs mic permission) ─────────────

func TestIntRecordMic(t *testing.T) {
	out := filepath.Join(t.TempDir(), "mic.mp4")
	err := Record(shortCtx(t), 2*time.Second,
		WithOutput(out),
		WithFrameRate(30),
		WithMic(true),
	)
	if err != nil {
		// Permission denial is the expected failure mode on a fresh runner.
		if err == ErrPermissionDenied {
			t.Skip("microphone permission not granted")
		}
		t.Fatalf("Record --mic: %v", err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() < 10*1024 {
		t.Errorf("mic mp4 too small or missing: %v", err)
	}
}

// ─── Recording — MIX of system audio + mic ───────────────────

func TestIntRecordMixedAudio(t *testing.T) {
	out := filepath.Join(t.TempDir(), "mix.mp4")
	err := Record(shortCtx(t), 2*time.Second,
		WithOutput(out),
		WithFrameRate(30),
		WithAudio(true),
		WithMic(true),
	)
	if err != nil {
		if err == ErrPermissionDenied {
			t.Skip("audio/mic permission not granted")
		}
		t.Fatalf("Record --audio --mic: %v", err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() < 10*1024 {
		t.Errorf("mix mp4 too small or missing: %v", err)
	}
}

// ─── Lifecycle: start + stop primitives ──────────────────────

func TestIntStartStop(t *testing.T) {
	out := filepath.Join(t.TempDir(), "lifecycle.mp4")
	r, err := NewRecorder(shortCtx(t), WithOutput(out), WithFrameRate(30))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if err := r.Start(shortCtx(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Stats should report some frames after a short interval.
	time.Sleep(1 * time.Second)
	s := r.Stats()
	if s.Frames == 0 {
		t.Error("expected some frames after 1s")
	}
	if err := r.Stop(shortCtx(t)); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Second Stop is a no-op.
	if err := r.Stop(shortCtx(t)); err != nil {
		t.Errorf("second Stop returned error: %v", err)
	}
}

// ─── Error paths ─────────────────────────────────────────────

func TestIntNewRecorderRequiresOutput(t *testing.T) {
	if _, err := NewRecorder(shortCtx(t)); err == nil {
		t.Fatal("NewRecorder without WithOutput should fail")
	}
}

func TestIntStopBeforeStartErrors(t *testing.T) {
	out := filepath.Join(t.TempDir(), "never.mp4")
	r, err := NewRecorder(shortCtx(t), WithOutput(out))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if err := r.Stop(shortCtx(t)); err != ErrNotStarted {
		t.Errorf("Stop-before-Start = %v, want ErrNotStarted", err)
	}
}

func TestIntResolvedDylibPathSet(t *testing.T) {
	if err := Load(); err != nil {
		t.Skipf("Load failed: %v", err)
	}
	if ResolvedDylibPath() == "" {
		t.Error("ResolvedDylibPath should be non-empty after Load")
	}
}
