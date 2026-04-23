package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/LocalKinAI/kinrec"
)

func runVersion(args []string) int {
	_ = kinrec.Load()
	fmt.Printf("kinrec v%s\n", kinrec.Version)
	fmt.Printf("  go:     %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  macOS:  %s\n", macOSVersion())
	if p := kinrec.ResolvedDylibPath(); p != "" {
		if info, err := os.Stat(p); err == nil {
			fmt.Printf("  dylib:  %s (%d bytes)\n", p, info.Size())
		} else {
			fmt.Printf("  dylib:  %s\n", p)
		}
	} else {
		fmt.Println("  dylib:  (not yet loaded)")
	}
	return 0
}

func macOSVersion() string {
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return "(unknown)"
	}
	ver := strings.TrimSpace(string(out))
	name, err := exec.Command("sw_vers", "-productName").Output()
	if err != nil {
		return ver
	}
	return fmt.Sprintf("%s %s", strings.TrimSpace(string(name)), ver)
}
