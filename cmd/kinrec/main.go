// kinrec — record your Mac screen (video + audio) to mp4 / mov, from the
// command line.
//
// Install:
//
//	go install github.com/LocalKinAI/kinrec/cmd/kinrec@latest
//
// Record until Ctrl+C:
//
//	kinrec record -o demo.mp4 --audio
//
// Timed:
//
//	kinrec record --duration 30s -o tutorial.mp4 --audio --fps 30
//
// Run `kinrec help <command>` for details.
package main

import (
	"fmt"
	"os"
	"sort"
)

type command struct {
	run   func([]string) int
	short string
	long  string
}

var cmds map[string]command

func init() {
	cmds = map[string]command{
		"record": {
			run:   runRecord,
			short: "Record the screen (+ optional audio) to mp4 / mov",
			long: `kinrec record [flags]

Record the main display to an mp4 (or mov) file. Stops on Ctrl+C or
after --duration elapses.

Flags:
  -o, --output PATH      Output path (required)
      --duration D       Stop after this long (30s, 5m); 0 = until Ctrl+C
      --fps N            Target frame rate (default 60)
      --resolution WxH   Output size (default: display native)
      --audio            Capture system audio (what apps on screen play)
      --mic              Capture microphone (your voice)
      --mic-device ID    Mic AVCaptureDevice uniqueID (see 'kinrec list mics')
      --codec h264|hevc  Video codec (default h264)
      --format mp4|mov   Container (default mp4)
      --display ID       Specific display ID (default: main)
      --shorts           Short-video preset: 1080x1920 + 30fps + audio + mic.
                         Optimized for X / TikTok / YT Shorts / Reels.
                         Any explicit --fps / --resolution / --audio / --mic
                         still wins.
      --click-highlight  Overlay a subtle cursor ring + click ripple for
                         UI demos (Screen Studio style). Uses
                         NSEvent.mouseLocation — no Accessibility prompt.
                         v0.1 supports Display target at native resolution
                         only; combining with --shorts / --resolution may
                         misalign the overlay.

Examples:
  kinrec record -o demo.mp4
  kinrec record -o tutorial.mp4 --mic --duration 5m
  kinrec record -o meeting.mp4 --audio --duration 1h
  kinrec record -o loom-style.mp4 --audio --mic --duration 30s    # mixed
  kinrec record -o demo.mov --format mov --codec hevc --fps 30
  kinrec record -o short.mp4 --shorts --duration 60s              # vertical`,
		},
		"list": {
			run:   runList,
			short: "List available displays or microphones",
			long: `kinrec list <displays|mics>

Enumerate attached displays (ID + size) or audio input devices
(unique-id + name). Use the ID / unique-id with:

  kinrec record --display N
  kinrec record --mic --mic-device=<unique-id>`,
		},
		"version": {
			run:   runVersion,
			short: "Print version info",
		},
		"help": {
			run:   runHelp,
			short: "Show help",
		},
	}
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		_ = runHelp(nil)
		os.Exit(2)
	}
	cmd, ok := cmds[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "kinrec: unknown command %q\n\n", args[0])
		_ = runHelp(nil)
		os.Exit(2)
	}
	os.Exit(cmd.run(args[1:]))
}

func runHelp(args []string) int {
	if len(args) == 1 {
		if c, ok := cmds[args[0]]; ok {
			if c.long != "" {
				fmt.Println(c.long)
			} else {
				fmt.Printf("kinrec %s — %s\n", args[0], c.short)
			}
			return 0
		}
		fmt.Fprintf(os.Stderr, "kinrec: unknown command %q\n", args[0])
		return 2
	}
	fmt.Println(`kinrec — record your Mac screen to mp4 / mov.

USAGE:
  kinrec <command> [flags]

COMMANDS:`)
	names := make([]string, 0, len(cmds))
	for n := range cmds {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-10s  %s\n", n, cmds[n].short)
	}
	fmt.Println(`
Run 'kinrec help <command>' for detailed usage.

DOCS:
  https://pkg.go.dev/github.com/LocalKinAI/kinrec
  https://github.com/LocalKinAI/kinrec`)
	return 0
}
