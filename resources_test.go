package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestParseStats(t *testing.T) {
	at := time.Unix(1000, 0)
	out := `== cpu.stat
usage_usec 5000000
user_usec 4000000
== cpu.max
400000 100000
== memory.current
3221225472
== memory.max
max
== memory.stat
anon 2147483648
file 1073741824
inactive_file 536870912
== meminfo
MemTotal:       33554432 kB
== cpus
16
== gpu
13926, 16376, 87
== gpu-apps
4242, [N/A]
`
	s, err := parseStats(out, at)
	if err != nil {
		t.Fatal(err)
	}
	if s.cpuUsec != 5000000 || s.cpuLimit != 4 || s.memMax != 0 || s.hostCPUs != 16 || s.hostMemory != 32<<30 {
		t.Fatalf("%+v", s)
	}
	m := memoryOf(s)
	if m.Used != 2560<<20 || m.Cache != 1<<30 || m.Limit != 32<<30 {
		t.Fatalf("memory %+v", m)
	}
	if g := s.gpu; g == nil || g.Used != 13926<<20 || g.Total != 16376<<20 || g.Util != 87 || !g.NoPerProcess {
		t.Fatalf("gpu %+v", s.gpu)
	}
	// Without a GPU section, and an nvidia-smi that printed nothing usable.
	s, _ = parseStats("== cpu.stat\nusage_usec 1\n== memory.current\n5\n== memory.max\n100\n== gpu\nNVIDIA-SMI has failed\n", at)
	if s.gpu != nil || s.memMax != 100 || s.cpuLimit != 0 {
		t.Fatalf("%+v", s)
	}
	if _, err := parseStats("cat: cpu.stat: Permission denied\n", at); err == nil {
		t.Fatal("no statistics accepted")
	}

	// CPU from two samples: 2 cores' worth over 10 s of a 4-core limit.
	prev := statsSample{at: at, cpuUsec: 1e6, cpuLimit: 4}
	cur := statsSample{at: at.Add(10 * time.Second), cpuUsec: 21e6, cpuLimit: 4}
	if cores, pct, ok := cpuUse(prev, cur); !ok || cores != 2 || pct != 50 {
		t.Fatal(cores, pct, ok)
	}
	cur.cpuLimit, cur.hostCPUs = 0, 16 // no limit: the host's cores
	if _, pct, _ := cpuUse(prev, cur); pct != 12.5 {
		t.Fatal(pct)
	}
	if _, _, ok := cpuUse(cur, prev); ok {
		t.Fatal("a counter that went back (a restart) is no reading")
	}
	if _, _, ok := cpuUse(statsSample{}, cur); ok {
		t.Fatal("one sample is no reading")
	}
}

func TestTopFolders(t *testing.T) {
	out := "10485760\t/home/dev\n" +
		"6291456\t/home/dev/ComfyUI\n5242880\t/home/dev/ComfyUI/models\n1048576\t/home/dev/ComfyUI/custom_nodes\n" +
		"3145728\t/home/dev/.cache\n1048576\t/home/dev/.cache/pip\n1048576\t/home/dev/.cache/uv\n" +
		"1048576\t/home/dev/work\nbad line\n12\trelative\n"
	total, top := topFolders(parseDu(out), homeDir, 3)
	if total != 10<<30 {
		t.Fatal(total)
	}
	want := []FolderSize{{"~/ComfyUI/models", 5 << 30}, {"~/.cache", 3 << 30}, {"~/ComfyUI/custom_nodes", 1 << 30}}
	if len(top) != 3 {
		t.Fatal(top)
	}
	for i := range want {
		if top[i] != want[i] {
			t.Fatalf("%d: %+v, want %+v", i, top[i], want[i])
		}
	}
}

func TestDiskCache(t *testing.T) {
	file := filepath.Join(t.TempDir(), "resources.json")
	d := &diskCache{file: file}
	now := time.Unix(10000, 0)
	if !d.due("dev", now, false) {
		t.Fatal("never measured: due")
	}
	if d.due("dev", now, true) {
		t.Fatal("measuring already: not again")
	}
	if u := d.get("dev"); u == nil || !u.Measuring {
		t.Fatal("measuring shows", u)
	}
	d.done("dev", &DiskUsage{Total: 5, At: now.Unix()}, []string{"dev"})
	if d.due("dev", now.Add(diskEvery-time.Second), false) {
		t.Fatal("fresh: not due")
	}
	if !d.due("dev", now.Add(diskEvery), false) {
		t.Fatal("stale: due")
	}
	d.done("dev", nil, []string{"dev"}) // a failed walk keeps the last size
	// A new Iwato reads the last sizes; a sandbox gone is dropped.
	e := &diskCache{file: file}
	if u := e.get("dev"); u == nil || u.Total != 5 || u.Measuring {
		t.Fatal("kept", u)
	}
	e.due("other", now, false)
	e.done("other", &DiskUsage{Total: 1, At: now.Unix()}, []string{"other"})
	if e.get("dev") != nil {
		t.Fatal("a removed sandbox's size stays")
	}
}
