//go:build uitest

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// The fake's resources: statsScript's and duScript's output, made up and
// moving with the clock, so the page parses what a real sandbox gives.
const (
	fakeHostCPUs   = 16
	fakeHostMemory = 32 << 30
	fakeGPUMemory  = 16 << 30
)

func (d *Fake) running(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.started[name]
}

func (d *Fake) ResourceStats(_ context.Context, sb Sandbox) (string, error) {
	if !d.running(sb.Name) {
		return "", errors.New("not running")
	}
	t := float64(time.Now().UnixMilli()) / 1000
	seed := float64(len(sb.Name))
	// CPU around 40% of 8 cores (the usage is the integral of that load).
	load := 3.2*t + 1.6*math.Sin(t/7+seed)*7
	used := uint64(2.5*(1<<30) + 1.2*(1<<30)*(1+math.Sin(t/40+seed)))
	var b strings.Builder
	fmt.Fprintf(&b, "== cpu.stat\nusage_usec %d\nuser_usec 1\n== cpu.max\n800000 100000\n", uint64(load*1e6))
	fmt.Fprintf(&b, "== memory.current\n%d\n== memory.max\n%d\n", used+(900<<20), uint64(8<<30))
	fmt.Fprintf(&b, "== memory.stat\nanon %d\nfile %d\ninactive_file %d\n", used-(300<<20), uint64(1200<<20), uint64(900<<20))
	fmt.Fprintf(&b, "== meminfo\nMemTotal:       %d kB\n== cpus\n%d\n", fakeHostMemory>>10, fakeHostCPUs)
	if sb.GPU {
		gpu := 9000 + 4000*(1+math.Sin(t/30))/2
		fmt.Fprintf(&b, "== gpu\n%d, %d, %d\n== gpu-apps\n1234, [N/A]\n", int(gpu), fakeGPUMemory>>20, int(50+40*math.Sin(t/9)))
	}
	return b.String(), nil
}

func (d *Fake) HomeUsage(_ context.Context, sb Sandbox) (string, error) {
	if !d.running(sb.Name) {
		return "", errors.New("not running")
	}
	const g = 1 << 20 // KiB per GiB
	lines := []struct {
		kib  int
		path string
	}{
		{24 * g, ""}, {12 * g, "/work"}, {11 * g, "/work/iwato"}, {8 * g, "/.cache"}, {6 * g, "/.cache/go-build"},
		{3 * g, "/.local"}, {3 * g, "/.local/share"}, {3 * g, "/.local/share/mise"}, {g, "/.claude"}, {g / 2, "/.npm"},
	}
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%d\t%s%s\n", l.kib, homeDir, l.path)
	}
	return b.String(), nil
}
