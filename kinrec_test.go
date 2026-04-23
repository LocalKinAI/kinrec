package kinrec

import (
	"errors"
	"testing"
)

// ─── Pure-Go helpers ─────────────────────────────────────────

func TestBoolI(t *testing.T) {
	if boolI(true) != 1 {
		t.Error("boolI(true) != 1")
	}
	if boolI(false) != 0 {
		t.Error("boolI(false) != 0")
	}
}

func TestCstrNulTerminated(t *testing.T) {
	b := []byte{'h', 'e', 'l', 'l', 'o', 0, 'x', 'x'}
	if got := cstr(b); got != "hello" {
		t.Errorf("cstr = %q, want hello", got)
	}
}

func TestCstrNoNul(t *testing.T) {
	if got := cstr([]byte("world")); got != "world" {
		t.Errorf("cstr = %q", got)
	}
}

func TestCstrEmpty(t *testing.T) {
	if cstr(nil) != "" {
		t.Error("nil should be empty")
	}
	if cstr([]byte{0}) != "" {
		t.Error("leading NUL should be empty")
	}
}

func TestContains(t *testing.T) {
	cases := []struct {
		s, sub string
		want   bool
	}{
		{"hello world", "world", true},
		{"hello world", "zzz", false},
		{"abc", "", true},
		{"", "x", false},
		{"", "", true},
	}
	for _, tc := range cases {
		if got := contains(tc.s, tc.sub); got != tc.want {
			t.Errorf("contains(%q, %q) = %v, want %v", tc.s, tc.sub, got, tc.want)
		}
	}
}

func TestSliceStr(t *testing.T) {
	pool := []byte("helloworld")
	if got := sliceStr(pool, 0, 5); got != "hello" {
		t.Errorf("got %q", got)
	}
	if got := sliceStr(pool, 5, 5); got != "world" {
		t.Errorf("got %q", got)
	}
	if got := sliceStr(pool, 0, 0); got != "" {
		t.Errorf("empty range should be empty, got %q", got)
	}
}

func TestSliceStrOutOfBounds(t *testing.T) {
	// Defensive: out-of-bounds returns empty, not panic.
	if got := sliceStr([]byte("abc"), 0, 999); got != "" {
		t.Errorf("out-of-bounds should be empty, got %q", got)
	}
}

// ─── Error wrapping ──────────────────────────────────────────

func TestWrapErrEmpty(t *testing.T) {
	err := wrapErr("capture", "")
	if err == nil {
		t.Fatal("nil")
	}
	if !contains(err.Error(), "unknown") {
		t.Errorf("empty raw should produce 'unknown': %v", err)
	}
}

func TestWrapErrPreservesOp(t *testing.T) {
	err := wrapErr("start", "camera lost")
	if !contains(err.Error(), "start") {
		t.Errorf("op label lost: %v", err)
	}
	if !contains(err.Error(), "camera lost") {
		t.Errorf("raw message lost: %v", err)
	}
}

func TestWrapErrDetectsPermissionDenied(t *testing.T) {
	for _, msg := range []string{
		"not authorized",
		"permission denied",
		"User denied access to the microphone",
	} {
		err := wrapErr("mic_setup", msg)
		if !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("%q should map to ErrPermissionDenied, got %v", msg, err)
		}
	}
}

func TestWrapErrNonPermissionNotMapped(t *testing.T) {
	err := wrapErr("writer", "disk full")
	if errors.Is(err, ErrPermissionDenied) {
		t.Error("disk-full should NOT map to permission-denied")
	}
}

// ─── Sentinel errors distinct ────────────────────────────────

func TestSentinelsDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range []error{
		ErrPermissionDenied,
		ErrAlreadyStarted,
		ErrNotStarted,
	} {
		if seen[e.Error()] {
			t.Errorf("duplicate sentinel text: %q", e.Error())
		}
		seen[e.Error()] = true
	}
}

// ─── Codec / Container enum stability ────────────────────────

func TestCodecEnumValues(t *testing.T) {
	// These values flow across the C ABI and must stay stable.
	if int(CodecH264) != 0 {
		t.Errorf("CodecH264 = %d, want 0 (wire-format stability)", CodecH264)
	}
	if int(CodecHEVC) != 1 {
		t.Errorf("CodecHEVC = %d, want 1 (wire-format stability)", CodecHEVC)
	}
}

func TestContainerEnumValues(t *testing.T) {
	if int(ContainerMP4) != 0 {
		t.Errorf("ContainerMP4 = %d, want 0", ContainerMP4)
	}
	if int(ContainerMOV) != 1 {
		t.Errorf("ContainerMOV = %d, want 1", ContainerMOV)
	}
}

// ─── Version constant ────────────────────────────────────────

func TestVersionNonEmpty(t *testing.T) {
	if Version == "" {
		t.Error("Version should never be empty")
	}
}
