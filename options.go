package kinrec

// Option configures a recorder. Options follow the functional-options
// pattern and compose in left-to-right order (later overrides earlier).
type Option func(*config)

type config struct {
	output          string
	displayID       uint32 // 0 = auto-pick
	width           int    // 0 = display native
	height          int    // 0 = display native
	fps             int    // 0 = 60
	captureAudio    bool
	captureMic      bool
	micDeviceID     string // empty = default input
	codec           Codec
	container       Container
	cursorHighlight bool
}

func defaultConfig() config {
	return config{
		fps:       60,
		codec:     CodecH264,
		container: ContainerMP4,
	}
}

// WithOutput sets the destination path. Required — recorders without
// WithOutput fail to construct.
//
// The file extension determines nothing by itself; use WithContainer
// to choose .mp4 vs .mov. WithOutput simply sets the filename.
func WithOutput(path string) Option {
	return func(c *config) { c.output = path }
}

// WithDisplay selects a specific display by CGDirectDisplayID. Zero
// (default) auto-picks the first attached display.
func WithDisplay(id uint32) Option {
	return func(c *config) { c.displayID = id }
}

// WithResolution forces the output frame size in pixels. Zero values
// keep the target's native resolution. Typical use: downsample a 4K
// display to 1080p to save bitrate.
func WithResolution(w, h int) Option {
	return func(c *config) { c.width, c.height = w, h }
}

// WithFrameRate caps the output frame rate. Default: 60.
func WithFrameRate(fps int) Option {
	return func(c *config) {
		if fps > 0 {
			c.fps = fps
		}
	}
}

// WithAudio toggles capture of system audio (application audio via
// SCStreamConfiguration.capturesAudio). Excludes the recording
// process's own output to prevent feedback loops.
//
// Default: false.
//
// In v0.1, WithAudio(true) and WithMic(true) are mutually exclusive.
// v0.2 will mix both into a single track.
func WithAudio(enabled bool) Option {
	return func(c *config) { c.captureAudio = enabled }
}

// WithMic toggles capture of the default microphone (or the device
// selected by [WithMicDevice]). Triggers a macOS Microphone permission
// prompt on first use.
//
// Default: false.
func WithMic(enabled bool) Option {
	return func(c *config) { c.captureMic = enabled }
}

// WithMicDevice selects a specific microphone by its AVCaptureDevice
// uniqueID. Use [ListMics] to enumerate. When unset, the system's
// default input device is used.
func WithMicDevice(uniqueID string) Option {
	return func(c *config) { c.micDeviceID = uniqueID }
}

// WithCodec selects the video codec. Default: [CodecH264].
//
// HEVC files are roughly 30% smaller at equivalent quality but require
// modern decoders (macOS 10.13+, Windows with the HEVC extension,
// modern browsers). For distribution to unknown audiences, stick with
// H.264.
func WithCodec(codec Codec) Option {
	return func(c *config) { c.codec = codec }
}

// WithContainer selects the output file format. Default: [ContainerMP4].
func WithContainer(container Container) Option {
	return func(c *config) { c.container = container }
}

// WithCursorHighlight overlays a subtle ring around the cursor and a
// yellow expanding "ripple" on every left-button click, making mouse
// activity unmistakable in screen recordings. Useful for tutorials and
// UI demos.
//
// Currently supports Display-target capture at the display's native
// resolution. Window / App / Region targets and --resolution scaling
// will misalign the overlay until coordinate mapping is generalized
// in a future release.
//
// Does NOT require Accessibility permission: uses NSEvent.mouseLocation
// and CGEventSourceButtonState.
//
// Default: false.
func WithCursorHighlight(enabled bool) Option {
	return func(c *config) { c.cursorHighlight = enabled }
}
