package kinrec

import "testing"

func TestDefaultConfig(t *testing.T) {
	c := defaultConfig()
	if c.fps != 60 {
		t.Errorf("fps default = %d, want 60", c.fps)
	}
	if c.codec != CodecH264 {
		t.Errorf("codec default = %d, want CodecH264", c.codec)
	}
	if c.container != ContainerMP4 {
		t.Errorf("container default = %d, want ContainerMP4", c.container)
	}
	if c.captureAudio {
		t.Error("captureAudio should default to false")
	}
	if c.captureMic {
		t.Error("captureMic should default to false")
	}
	if c.width != 0 || c.height != 0 {
		t.Errorf("size defaults should be 0/native, got %dx%d", c.width, c.height)
	}
}

func TestWithOutput(t *testing.T) {
	c := defaultConfig()
	WithOutput("/tmp/x.mp4")(&c)
	if c.output != "/tmp/x.mp4" {
		t.Errorf("output = %q", c.output)
	}
}

func TestWithDisplay(t *testing.T) {
	c := defaultConfig()
	WithDisplay(42)(&c)
	if c.displayID != 42 {
		t.Errorf("displayID = %d", c.displayID)
	}
}

func TestWithResolution(t *testing.T) {
	c := defaultConfig()
	WithResolution(1280, 720)(&c)
	if c.width != 1280 || c.height != 720 {
		t.Errorf("resolution = %dx%d", c.width, c.height)
	}
}

func TestWithFrameRate(t *testing.T) {
	c := defaultConfig()
	WithFrameRate(30)(&c)
	if c.fps != 30 {
		t.Errorf("fps = %d", c.fps)
	}
}

func TestWithFrameRateIgnoresNonPositive(t *testing.T) {
	c := defaultConfig()
	// Zero / negative must not overwrite the default.
	WithFrameRate(0)(&c)
	if c.fps != 60 {
		t.Errorf("WithFrameRate(0) changed fps to %d", c.fps)
	}
	WithFrameRate(-30)(&c)
	if c.fps != 60 {
		t.Errorf("WithFrameRate(-30) changed fps to %d", c.fps)
	}
}

func TestWithAudio(t *testing.T) {
	c := defaultConfig()
	WithAudio(true)(&c)
	if !c.captureAudio {
		t.Error("WithAudio(true) did not take effect")
	}
	WithAudio(false)(&c)
	if c.captureAudio {
		t.Error("WithAudio(false) did not toggle off")
	}
}

func TestWithMic(t *testing.T) {
	c := defaultConfig()
	WithMic(true)(&c)
	if !c.captureMic {
		t.Error("WithMic(true) did not take effect")
	}
}

func TestWithMicDevice(t *testing.T) {
	c := defaultConfig()
	WithMicDevice("AppleUSBAudio:0")(&c)
	if c.micDeviceID != "AppleUSBAudio:0" {
		t.Errorf("micDeviceID = %q", c.micDeviceID)
	}
}

func TestWithCodec(t *testing.T) {
	for _, tc := range []struct {
		codec Codec
		name  string
	}{
		{CodecH264, "h264"},
		{CodecHEVC, "hevc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := defaultConfig()
			WithCodec(tc.codec)(&c)
			if c.codec != tc.codec {
				t.Errorf("codec = %d, want %d", c.codec, tc.codec)
			}
		})
	}
}

func TestWithContainer(t *testing.T) {
	c := defaultConfig()
	WithContainer(ContainerMOV)(&c)
	if c.container != ContainerMOV {
		t.Errorf("container = %d, want ContainerMOV", c.container)
	}
}

func TestOptionsComposition(t *testing.T) {
	// Later options override earlier ones (functional options semantics).
	c := defaultConfig()
	WithFrameRate(30)(&c)
	WithFrameRate(120)(&c)
	if c.fps != 120 {
		t.Errorf("later fps override failed: %d", c.fps)
	}
}

func TestWithCursorHighlight(t *testing.T) {
	c := defaultConfig()
	if c.cursorHighlight {
		t.Error("cursorHighlight should default to false")
	}
	WithCursorHighlight(true)(&c)
	if !c.cursorHighlight {
		t.Error("WithCursorHighlight(true) did not take effect")
	}
	WithCursorHighlight(false)(&c)
	if c.cursorHighlight {
		t.Error("WithCursorHighlight(false) did not toggle off")
	}
}

func TestOptionsAllInteract(t *testing.T) {
	c := defaultConfig()
	opts := []Option{
		WithOutput("/tmp/test.mp4"),
		WithDisplay(7),
		WithResolution(1920, 1080),
		WithFrameRate(30),
		WithAudio(true),
		WithMic(true),
		WithMicDevice("mic-id"),
		WithCodec(CodecHEVC),
		WithContainer(ContainerMOV),
	}
	for _, o := range opts {
		o(&c)
	}
	if c.output != "/tmp/test.mp4" {
		t.Error("output")
	}
	if c.displayID != 7 {
		t.Error("displayID")
	}
	if c.width != 1920 || c.height != 1080 {
		t.Error("resolution")
	}
	if c.fps != 30 {
		t.Error("fps")
	}
	if !c.captureAudio {
		t.Error("captureAudio")
	}
	if !c.captureMic {
		t.Error("captureMic")
	}
	if c.micDeviceID != "mic-id" {
		t.Error("micDeviceID")
	}
	if c.codec != CodecHEVC {
		t.Error("codec")
	}
	if c.container != ContainerMOV {
		t.Error("container")
	}
}
