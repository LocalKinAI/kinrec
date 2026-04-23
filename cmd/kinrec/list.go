package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/LocalKinAI/kinrec"
)

func runList(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "kinrec list: need subtarget (displays | mics)")
		return 2
	}
	switch args[0] {
	case "displays", "d":
		return runListDisplays()
	case "mics", "m", "mic":
		return runListMics()
	default:
		fmt.Fprintf(os.Stderr, "kinrec list: unknown target %q (want displays | mics)\n", args[0])
		return 2
	}
}

func runListDisplays() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ds, err := kinrec.ListDisplays(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kinrec: %v\n", err)
		return 1
	}
	fmt.Printf("%-8s  %s\n", "id", "size")
	fmt.Println("--------  ---------")
	for _, d := range ds {
		fmt.Printf("%-8d  %dx%d\n", d.ID, d.Width, d.Height)
	}
	fmt.Printf("\n%d display(s)\n", len(ds))
	return 0
}

func runListMics() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ms, err := kinrec.ListMics(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kinrec: %v\n", err)
		return 1
	}
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].IsDefault != ms[j].IsDefault {
			return ms[i].IsDefault
		}
		return ms[i].Name < ms[j].Name
	})
	fmt.Printf("%-3s  %-50s  %s\n", "def", "unique-id", "name")
	fmt.Println("---  --------------------------------------------------  -----")
	for _, m := range ms {
		mark := "   "
		if m.IsDefault {
			mark = " * "
		}
		fmt.Printf("%-3s  %-50s  %s\n", mark, truncate(m.UniqueID, 50), m.Name)
	}
	fmt.Printf("\n%d mic(s)  (* = system default)\n", len(ms))
	if len(ms) > 0 {
		fmt.Println("\nTip: use --mic-device=<unique-id> when recording.")
	}
	return 0
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return s[:max-1] + "…"
}
