package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/LocalKinAI/kinrec"
)

func runRecord(args []string) int {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	var (
		output     string
		duration   time.Duration
		fps        int
		resolution string
		audio      bool
		mic        bool
		micDevice  string
		codec      string
		format     string
		displayID  uint
		shorts     bool
		clickHi    bool
	)
	fs.StringVar(&output, "o", "", "output path (required)")
	fs.StringVar(&output, "output", "", "output path (required)")
	fs.DurationVar(&duration, "duration", 0, "stop after this duration (e.g. 30s, 5m); 0 = Ctrl+C to stop")
	fs.IntVar(&fps, "fps", 60, "target frame rate")
	fs.StringVar(&resolution, "resolution", "", "output size WxH (default: display native)")
	fs.BoolVar(&audio, "audio", false, "capture system audio (from apps on screen)")
	fs.BoolVar(&mic, "mic", false, "capture microphone (default input)")
	fs.StringVar(&micDevice, "mic-device", "", "mic AVCaptureDevice uniqueID (see `kinrec list mics`)")
	fs.StringVar(&codec, "codec", "h264", "video codec: h264 | hevc")
	fs.StringVar(&format, "format", "mp4", "container: mp4 | mov")
	fs.UintVar(&displayID, "display", 0, "display ID (default: main)")
	fs.BoolVar(&shorts, "shorts", false, "short-video preset: 1080x1920 vertical, 30fps, +audio +mic")
	fs.BoolVar(&clickHi, "click-highlight", false, "overlay cursor ring + click ripple for UI demos")
	_ = fs.Parse(args)

	// --shorts overrides other defaults with values optimized for
	// X / TikTok / YT Shorts / Reels uploads: vertical 1080x1920,
	// 30 fps (smaller files, phone-friendly), mixed audio track.
	// Any explicit --fps / --resolution / --audio / --mic still wins
	// (this check runs before their values are used below).
	if shorts {
		if resolution == "" {
			resolution = "1080x1920"
		}
		// Only override fps if user didn't pass it (flag default == 60).
		// We can't distinguish "user passed 60" from "default 60" with
		// stdlib flag, so we accept the rare case where an explicit
		// --fps 60 + --shorts becomes 30. Document in help.
		if fps == 60 {
			fps = 30
		}
		if !audio && !mic {
			audio = true
			mic = true
		}
	}

	if output == "" {
		fmt.Fprintln(os.Stderr, "kinrec record: -o <path> is required")
		return 2
	}

	opts := []kinrec.Option{
		kinrec.WithOutput(output),
		kinrec.WithFrameRate(fps),
		kinrec.WithAudio(audio),
		kinrec.WithMic(mic),
		kinrec.WithDisplay(uint32(displayID)),
		kinrec.WithCursorHighlight(clickHi),
	}
	if micDevice != "" {
		opts = append(opts, kinrec.WithMicDevice(micDevice))
	}
	if resolution != "" {
		w, h, err := parseResolution(resolution)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kinrec: %v\n", err)
			return 2
		}
		opts = append(opts, kinrec.WithResolution(w, h))
	}
	switch strings.ToLower(codec) {
	case "h264", "":
		opts = append(opts, kinrec.WithCodec(kinrec.CodecH264))
	case "hevc", "h265":
		opts = append(opts, kinrec.WithCodec(kinrec.CodecHEVC))
	default:
		fmt.Fprintf(os.Stderr, "kinrec: unknown --codec %q (want h264 | hevc)\n", codec)
		return 2
	}
	switch strings.ToLower(format) {
	case "mp4", "":
		opts = append(opts, kinrec.WithContainer(kinrec.ContainerMP4))
	case "mov":
		opts = append(opts, kinrec.WithContainer(kinrec.ContainerMOV))
	default:
		fmt.Fprintf(os.Stderr, "kinrec: unknown --format %q (want mp4 | mov)\n", format)
		return 2
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build the recorder.
	r, err := kinrec.NewRecorder(ctx, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kinrec: %v\n", err)
		return 1
	}
	fmt.Printf("→ recording to %s\n", output)
	switch {
	case audio && mic:
		fmt.Println("  audio:     system + mic (mixed via AVAudioEngine)")
	case audio:
		fmt.Println("  audio:     system (AAC 128 kbps, stereo 48 kHz)")
	case mic:
		if micDevice != "" {
			fmt.Printf("  audio:     mic (device=%s)\n", micDevice)
		} else {
			fmt.Println("  audio:     mic (default input)")
		}
	default:
		fmt.Println("  audio:     none  (add --audio or --mic or both)")
	}
	fmt.Printf("  codec:     %s\n", codec)
	fmt.Printf("  container: %s\n", format)
	fmt.Printf("  fps:       %d\n", fps)
	if clickHi {
		fmt.Println("  cursor:    highlight ring + click ripple")
	}

	if err := r.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "kinrec: %v\n", err)
		return 1
	}

	// Listen for Ctrl+C (SIGINT / SIGTERM) and wall-clock deadline.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Progress ticker: print frame count every second.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var deadline <-chan time.Time
	if duration > 0 {
		t := time.NewTimer(duration)
		defer t.Stop()
		deadline = t.C
		fmt.Printf("  stopping in %s (or Ctrl+C)\n\n", duration)
	} else {
		fmt.Print("  recording — press Ctrl+C to stop\n\n")
	}

	start := time.Now()
	stopReason := "timer"
	for {
		select {
		case <-sigCh:
			stopReason = "signal"
			goto done
		case <-deadline:
			goto done
		case <-ticker.C:
			s := r.Stats()
			el := time.Since(start).Round(time.Second)
			if audio || mic {
				fmt.Printf("\r  %s  %d frames  %d audio bufs   ",
					el, s.Frames, s.AudioBuffers)
			} else {
				fmt.Printf("\r  %s  %d frames   ", el, s.Frames)
			}
		}
	}
done:
	fmt.Printf("\n→ finalizing (%s)...\n", stopReason)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	if err := r.Stop(stopCtx); err != nil {
		fmt.Fprintf(os.Stderr, "kinrec: stop: %v\n", err)
		return 1
	}
	if info, err := os.Stat(output); err == nil {
		fmt.Printf("✓ %s  (%.1f MB)\n", output, float64(info.Size())/1024/1024)
	} else {
		fmt.Printf("✓ %s\n", output)
	}
	return 0
}

func parseResolution(s string) (int, int, error) {
	parts := strings.Split(s, "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("bad --resolution %q (want WxH, e.g. 1920x1080)", s)
	}
	var w, h int
	if _, err := fmt.Sscanf(parts[0], "%d", &w); err != nil {
		return 0, 0, fmt.Errorf("bad width %q", parts[0])
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &h); err != nil {
		return 0, 0, fmt.Errorf("bad height %q", parts[1])
	}
	return w, h, nil
}
