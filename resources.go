package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The sandboxes' CPU, memory, GPU and home size (docs/adr/0020). Iwato reads
// them from outside OpenShell's sandbox: an engine exec into the workload
// container as the container's user (root), which OpenShell's Landlock does
// not confine. Only while a page asks (GET /api/resources), or for the
// memory notification when it is on.
const (
	resourceFocusEvery = 4 * time.Second  // the sandbox the page shows
	resourceOtherEvery = 14 * time.Second // the others (the sidebar)
	resourceWait       = 2 * time.Second  // a request waits this long for fresh samples
	resourceExecLimit  = 10 * time.Second
	diskEvery          = 10 * time.Minute
	diskLimit          = 10 * time.Minute
	diskTop            = 8
	resourceAlertEvery = 30 * time.Second // without a page, for the notification
)

// ResourceSettings are the settings' 「リソースの表示」: thresholds in percent
// (0: the default).
type ResourceSettings struct {
	Hide   bool `json:"hide,omitempty"`
	Warn   int  `json:"warn,omitempty"`
	Crit   int  `json:"crit,omitempty"`
	Notify bool `json:"notify,omitempty"`
}

func (r *ResourceSettings) levels() (warn, crit int) {
	warn, crit = 80, 95
	if r != nil && r.Warn > 0 {
		warn = r.Warn
	}
	if r != nil && r.Crit > 0 {
		crit = r.Crit
	}
	return warn, crit
}

func (r *ResourceSettings) Validate() error {
	if r == nil {
		return nil
	}
	warn, crit := r.levels()
	if warn < 10 || crit > 100 || warn >= crit {
		return errCode("resources_invalid", nil, "黄は 10〜99 %、赤は黄より大きく 100 % 以下にしてください")
	}
	return nil
}

// statsSample is one read of a workload's cgroup (bytes, microseconds).
type statsSample struct {
	at                       time.Time
	cpuUsec                  uint64
	cpuLimit                 float64 // cores; 0: none
	memCurrent, memMax       uint64  // memMax 0: none
	anon, file, inactiveFile uint64
	hostMemory               uint64
	hostCPUs                 int
	gpu                      *GPUUsage
}

type GPUUsage struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
	Util  int    `json:"util"`
	// NoPerProcess: nvidia-smi lists the processes without their memory
	// (WSL), so the numbers are the whole GPU's.
	NoPerProcess bool `json:"noPerProcess,omitempty"`
}

// statsScript prints the sections parseStats reads. $1 = gpu adds
// nvidia-smi's. timeout keeps a stuck nvidia-smi from outliving the exec.
const statsScript = `cd /sys/fs/cgroup || exit 1
for f in cpu.stat cpu.max memory.current memory.max; do echo "== $f"; cat "$f" 2>/dev/null; done
echo "== memory.stat"; grep -E '^(anon|file|inactive_file) ' memory.stat 2>/dev/null
echo "== meminfo"; grep '^MemTotal:' /proc/meminfo
echo "== cpus"; grep -c '^cpu[0-9]' /proc/stat
[ "$1" = gpu ] || exit 0
echo "== gpu"; timeout 10 nvidia-smi --query-gpu=memory.used,memory.total,utilization.gpu --format=csv,noheader,nounits 2>/dev/null
echo "== gpu-apps"; timeout 10 nvidia-smi --query-compute-apps=pid,used_memory --format=csv,noheader,nounits 2>/dev/null
exit 0`

// duScript prints the home's folders down to 3 levels, largest first, in
// KiB (du -k), at the lowest CPU and I/O priority; run as the home's owner.
const duScript = `p=""; command -v ionice >/dev/null 2>&1 && p="ionice -c3 -t"
$p timeout 590 nice -n 19 du -x -k -d 3 "$1" 2>/dev/null | sort -rn | head -n 400`

func parseStats(out string, at time.Time) (statsSample, error) {
	s := statsSample{at: at}
	sections := map[string][]string{}
	cur := ""
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if name, ok := strings.CutPrefix(line, "== "); ok {
			cur = name
			sections[cur] = []string{}
			continue
		}
		if cur != "" && line != "" {
			sections[cur] = append(sections[cur], line)
		}
	}
	num := func(v string) uint64 { n, _ := strconv.ParseUint(v, 10, 64); return n }
	usage := false
	for _, l := range sections["cpu.stat"] {
		if k, v, ok := strings.Cut(l, " "); ok && k == "usage_usec" {
			s.cpuUsec, usage = num(v), true
		}
	}
	if !usage || len(sections["memory.current"]) == 0 {
		return s, errors.New("no cgroup v2 statistics")
	}
	if f := strings.Fields(strings.Join(sections["cpu.max"], " ")); len(f) == 2 && f[0] != "max" {
		if q, p := num(f[0]), num(f[1]); q > 0 && p > 0 {
			s.cpuLimit = float64(q) / float64(p)
		}
	}
	s.memCurrent = num(sections["memory.current"][0])
	if m := sections["memory.max"]; len(m) > 0 && m[0] != "max" {
		s.memMax = num(m[0])
	}
	for _, l := range sections["memory.stat"] {
		k, v, _ := strings.Cut(l, " ")
		switch k {
		case "anon":
			s.anon = num(v)
		case "file":
			s.file = num(v)
		case "inactive_file":
			s.inactiveFile = num(v)
		}
	}
	for _, l := range sections["meminfo"] {
		if f := strings.Fields(l); len(f) >= 2 {
			s.hostMemory = num(f[1]) << 10
		}
	}
	if c := sections["cpus"]; len(c) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(c[0])); err == nil && n > 0 {
			s.hostCPUs = n
		}
	}
	if g, ok := sections["gpu"]; ok {
		s.gpu = parseGPU(g, sections["gpu-apps"])
	}
	return s, nil
}

// parseGPU reads nvidia-smi's CSV (MiB, no units), adding up the GPUs.
func parseGPU(lines, apps []string) *GPUUsage {
	var g GPUUsage
	n := 0
	for _, l := range lines {
		f := strings.Split(l, ",")
		if len(f) < 3 {
			continue
		}
		used, e1 := strconv.ParseFloat(strings.TrimSpace(f[0]), 64)
		total, e2 := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
		if e1 != nil || e2 != nil {
			continue
		}
		util, _ := strconv.Atoi(strings.TrimSpace(f[2]))
		g.Used += uint64(used) << 20
		g.Total += uint64(total) << 20
		g.Util += util
		n++
	}
	if n == 0 {
		return nil
	}
	g.Util /= n
	for _, l := range apps {
		if strings.Contains(l, "N/A") {
			g.NoPerProcess = true
		}
	}
	return &g
}

// cpuUse is the CPU used between two samples, in cores, and as a percent of
// the limit (the host's cores without one); false when it cannot tell.
func cpuUse(prev, cur statsSample) (cores, percent float64, ok bool) {
	dt := cur.at.Sub(prev.at).Seconds()
	if prev.at.IsZero() || dt <= 0 || cur.cpuUsec < prev.cpuUsec {
		return 0, 0, false
	}
	cores = float64(cur.cpuUsec-prev.cpuUsec) / 1e6 / dt
	limit := cur.cpuLimit
	if limit == 0 {
		limit = float64(cur.hostCPUs)
	}
	if limit <= 0 {
		return cores, 0, false
	}
	return cores, min(100, cores/limit*100), true
}

// MemoryUsage: Used is what docker stats shows (memory.current less the
// inactive file cache); Cache is the file cache; Limit is memory.max or the
// host's memory.
type MemoryUsage struct {
	Used    uint64 `json:"used"`
	Current uint64 `json:"current"`
	Anon    uint64 `json:"anon"`
	Cache   uint64 `json:"cache"`
	Limit   uint64 `json:"limit"`
}

func memoryOf(s statsSample) MemoryUsage {
	m := MemoryUsage{Current: s.memCurrent, Anon: s.anon, Cache: s.file, Limit: s.memMax}
	m.Used = s.memCurrent
	if s.inactiveFile < m.Used {
		m.Used -= s.inactiveFile
	}
	if m.Limit == 0 {
		m.Limit = s.hostMemory
	}
	return m
}

type FolderSize struct {
	Path string `json:"path"` // ~/…
	Size uint64 `json:"size"`
}

// parseDu reads `du -k` lines (KiB, a tab, the path) into bytes by path.
func parseDu(out string) map[string]uint64 {
	sizes := map[string]uint64{}
	for _, l := range strings.Split(out, "\n") {
		k, p, ok := strings.Cut(l, "\t")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(k), 10, 64)
		if err != nil || !strings.HasPrefix(p, "/") {
			continue
		}
		sizes[path.Clean(p)] = n << 10
	}
	return sizes
}

// topFolders lists the largest folders under root: its children, a folder
// replaced by its own children when one child holds half of it or more (so
// ~/.cache shows as ~/.cache/huggingface), the n largest.
func topFolders(sizes map[string]uint64, root string, n int) (uint64, []FolderSize) {
	root = path.Clean(root)
	children := map[string][]string{}
	for p := range sizes {
		if p != root && strings.HasPrefix(p, root+"/") {
			children[path.Dir(p)] = append(children[path.Dir(p)], p)
		}
	}
	bySize := func(list []string) {
		slices.SortFunc(list, func(a, b string) int {
			if sizes[a] != sizes[b] {
				if sizes[a] > sizes[b] {
					return -1
				}
				return 1
			}
			return strings.Compare(a, b)
		})
	}
	list := slices.Clone(children[root])
	for range 32 {
		bySize(list)
		expanded := false
		for i, p := range list[:min(n, len(list))] {
			kids := slices.Clone(children[p])
			bySize(kids)
			if len(kids) > 0 && sizes[kids[0]]*2 >= sizes[p] && sizes[p] > 0 {
				list = append(append(list[:i:i], kids...), list[i+1:]...)
				expanded = true
				break
			}
		}
		if !expanded {
			break
		}
	}
	bySize(list)
	top := []FolderSize{}
	for _, p := range list[:min(n, len(list))] {
		top = append(top, FolderSize{Path: "~" + strings.TrimPrefix(p, root), Size: sizes[p]})
	}
	return sizes[root], top
}

// DiskUsage is a home's size as last measured (At: Unix seconds).
type DiskUsage struct {
	Total     uint64       `json:"total"`
	Top       []FolderSize `json:"top"`
	At        int64        `json:"at"`
	Measuring bool         `json:"measuring,omitempty"`
}

// diskCache keeps each home's last size, in resources.json beside the
// configuration, so a stopped sandbox still shows one.
type diskCache struct {
	mu      sync.Mutex
	file    string
	sizes   map[string]DiskUsage
	running map[string]bool // measuring now
}

func (d *diskCache) load() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sizes == nil {
		d.sizes, d.running = map[string]DiskUsage{}, map[string]bool{}
		if d.file != "" {
			if b, err := os.ReadFile(d.file); err == nil {
				_ = json.Unmarshal(b, &d.sizes)
			}
		}
	}
}

// due reports whether name's home should be measured again (and marks it
// measuring when so).
func (d *diskCache) due(name string, now time.Time, force bool) bool {
	d.load()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running[name] {
		return false
	}
	if u, ok := d.sizes[name]; ok && !force && now.Sub(time.Unix(u.At, 0)) < diskEvery {
		return false
	}
	d.running[name] = true
	return true
}

func (d *diskCache) done(name string, u *DiskUsage, keep []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.running, name)
	if u == nil {
		return
	}
	d.sizes[name] = *u
	for k := range d.sizes {
		if !slices.Contains(keep, k) {
			delete(d.sizes, k)
		}
	}
	if d.file != "" {
		if b, err := json.Marshal(d.sizes); err == nil {
			_ = os.WriteFile(d.file, b, 0o600)
		}
	}
}

func (d *diskCache) get(name string) *DiskUsage {
	d.load()
	d.mu.Lock()
	defer d.mu.Unlock()
	u, ok := d.sizes[name]
	if !ok && !d.running[name] {
		return nil
	}
	u.Measuring = d.running[name]
	return &u
}

// resourceRuntime reads a running sandbox's statistics (statsScript's
// output) and its home's folders (duScript's). OpenShell and Fake
// implement it.
type resourceRuntime interface {
	ResourceStats(ctx context.Context, sb Sandbox) (string, error)
	HomeUsage(ctx context.Context, sb Sandbox) (string, error)
}

type resourceState struct {
	last, prev statsSample
	tried      time.Time
	busy       bool
	done       chan struct{}
	alerted    bool
}

// resourceMonitor holds the latest samples. Sampling happens only when a
// page asks or for the notification; never for a sandbox not running.
type resourceMonitor struct {
	s        *Server
	now      func() time.Time
	mu       sync.Mutex
	states   map[string]*resourceState
	running  map[string]bool // from the last sandbox list
	observed time.Time
	host     struct {
		cpus   int
		memory uint64
	}
	disk  diskCache
	diskQ chan struct{} // one home walk at a time
}

func newResourceMonitor(s *Server, dir string) *resourceMonitor {
	m := &resourceMonitor{s: s, now: time.Now, states: map[string]*resourceState{}, running: map[string]bool{}, diskQ: make(chan struct{}, 1)}
	if dir != "" {
		m.disk.file = dir + string(os.PathSeparator) + "resources.json"
	}
	return m
}

// observe keeps which sandboxes run, from the list the page reads anyway.
func (m *resourceMonitor) observe(list []Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = map[string]bool{}
	for _, st := range list {
		if st.State == "running" {
			m.running[st.Name] = true
		}
	}
	m.observed = m.now()
	for name := range m.states {
		if !m.running[name] {
			delete(m.states, name)
		}
	}
}

func (m *resourceMonitor) settings() *ResourceSettings {
	m.s.mu.Lock()
	defer m.s.mu.Unlock()
	if r := m.s.config.Resources; r != nil {
		c := *r
		return &c
	}
	return &ResourceSettings{}
}

// sample starts reading sb when its last try is older than every, and
// returns a channel closed when that read ends (nil: nothing started).
func (m *resourceMonitor) sample(ctx context.Context, sb Sandbox, every time.Duration) chan struct{} {
	rt, ok := m.s.rt.(resourceRuntime)
	if !ok || (m.s.checks != nil && m.s.checks.engine() != nil) {
		return nil
	}
	m.mu.Lock()
	st := m.states[sb.Name]
	if st == nil {
		st = &resourceState{}
		m.states[sb.Name] = st
	}
	if st.busy {
		done := st.done
		m.mu.Unlock()
		return done
	}
	if m.now().Sub(st.tried) < every {
		m.mu.Unlock()
		return nil
	}
	st.busy, st.tried, st.done = true, m.now(), make(chan struct{})
	done := st.done
	m.mu.Unlock()
	go func() {
		defer close(done)
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), resourceExecLimit)
		out, err := rt.ResourceStats(c, sb)
		cancel()
		var s statsSample
		if err == nil {
			s, err = parseStats(out, m.now())
		}
		m.mu.Lock()
		st.busy = false
		if err == nil {
			st.prev, st.last = st.last, s
			if s.hostCPUs > 0 && s.hostMemory > 0 {
				m.host.cpus, m.host.memory = s.hostCPUs, s.hostMemory
			}
		}
		m.mu.Unlock()
		if err == nil {
			m.alert(sb.Name, st)
		}
	}()
	return done
}

// measure walks sb's home in the background when due.
func (m *resourceMonitor) measure(sb Sandbox, force bool) {
	rt, ok := m.s.rt.(resourceRuntime)
	if !ok || !m.disk.due(sb.Name, m.now(), force) {
		return
	}
	go func() {
		m.diskQ <- struct{}{}
		defer func() { <-m.diskQ }()
		ctx, cancel := context.WithTimeout(m.s.ctx, diskLimit)
		defer cancel()
		out, err := rt.HomeUsage(ctx, sb)
		var u *DiskUsage
		if sizes := parseDu(out); err == nil && len(sizes) > 0 {
			total, top := topFolders(sizes, homeDir, diskTop)
			u = &DiskUsage{Total: total, Top: top, At: m.now().Unix()}
		} else if err != nil {
			log.Printf("sandbox %s: measuring the home: %s", sb.Name, safeCLIError(err))
		}
		var keep []string
		for _, w := range m.s.sandboxList() {
			keep = append(keep, w.Name)
		}
		m.disk.done(sb.Name, u, keep)
	}()
}

// alert queues the memory notification when the sandbox's memory crosses
// the red threshold, once until it drops below the amber one.
func (m *resourceMonitor) alert(name string, st *resourceState) {
	set := m.settings()
	warn, crit := set.levels()
	mem := memoryOf(st.last)
	if mem.Limit == 0 {
		return
	}
	pct := int(mem.Used * 100 / mem.Limit)
	m.mu.Lock()
	fire := !st.alerted && pct >= crit
	if pct >= crit {
		st.alerted = true
	} else if pct < warn {
		st.alerted = false
	}
	m.mu.Unlock()
	if fire && set.Notify && m.s.notifier != nil {
		m.s.notifier.queue(PushMessage{Title: name + ": メモリ " + strconv.Itoa(pct) + "%",
			Body: fmt.Sprintf("%.1f / %.1f GB 使っています", float64(mem.Used)/(1<<30), float64(mem.Limit)/(1<<30)),
			Tag:  "memory-" + name, Path: openPath(name, "tab", "resources"), sandbox: name,
			summary: "メモリ " + strconv.Itoa(pct) + "%", memory: true})
	}
}

// loop samples the running sandboxes for the memory notification while it
// is on and pushes can go out, with or without a page.
func (m *resourceMonitor) loop(ctx context.Context) {
	t := time.NewTicker(resourceAlertEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !m.settings().Notify || m.s.notifier == nil || m.s.push == nil || m.s.push.Count() == 0 {
			continue
		}
		m.mu.Lock()
		fresh := m.now().Sub(m.observed) < 2*time.Minute
		running := m.running
		m.mu.Unlock()
		for _, sb := range m.s.sandboxList() {
			// Without a recent list a stopped sandbox's lookup finds no
			// container and runs no exec.
			if !fresh || running[sb.Name] {
				m.sample(ctx, sb, resourceAlertEvery-time.Second)
			}
		}
	}
}

// SandboxResources is one sandbox in GET /api/resources.
type SandboxResources struct {
	Running  bool         `json:"running"`
	At       int64        `json:"at,omitempty"` // Unix ms of the sample
	CPU      *float64     `json:"cpu,omitempty"`
	Cores    float64      `json:"cores,omitempty"`
	CPULimit float64      `json:"cpuLimit,omitempty"`
	Memory   *MemoryUsage `json:"memory,omitempty"`
	GPU      *GPUUsage    `json:"gpu,omitempty"`
	Disk     *DiskUsage   `json:"disk,omitempty"`
}

func (m *resourceMonitor) view(name string) SandboxResources {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := SandboxResources{Running: m.running[name], Disk: m.disk.get(name)}
	st := m.states[name]
	if !r.Running || st == nil || st.last.at.IsZero() {
		return r
	}
	r.At = st.last.at.UnixMilli()
	r.CPULimit = st.last.cpuLimit
	if r.CPULimit == 0 {
		r.CPULimit = float64(st.last.hostCPUs)
	}
	if cores, pct, ok := cpuUse(st.prev, st.last); ok {
		r.Cores, r.CPU = cores, &pct
	}
	mem := memoryOf(st.last)
	r.Memory = &mem
	r.GPU = st.last.gpu
	return r
}

// handleResources: GET /api/resources[?focus=NAME] gives every sandbox's
// latest numbers, reading those older than their interval first (focus:
// the shown one, more often). GET and POST /api/resources/settings read and
// change the settings; POST /api/resources/measure {sandbox} measures a
// home now.
func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	m := s.resources
	switch r.URL.Path {
	case "/api/resources":
		if r.Method != http.MethodGet {
			fail(w, 405, "method not allowed")
			return
		}
		if !s.require(w, r, false) {
			return
		}
		set := m.settings()
		focus := r.URL.Query().Get("focus")
		list := s.sandboxList()
		var waits []chan struct{}
		if !set.Hide {
			m.mu.Lock()
			running := m.running
			m.mu.Unlock()
			for _, sb := range list {
				if !running[sb.Name] {
					continue
				}
				every := resourceOtherEvery
				if sb.Name == focus {
					every = resourceFocusEvery
				}
				if done := m.sample(s.ctx, sb, every); done != nil {
					waits = append(waits, done)
				}
				m.measure(sb, false)
			}
		}
		deadline := time.After(resourceWait)
	wait:
		for _, done := range waits {
			select {
			case <-done:
			case <-deadline:
				break wait
			case <-r.Context().Done():
				return
			}
		}
		out := map[string]SandboxResources{}
		for _, sb := range list {
			out[sb.Name] = m.view(sb.Name)
		}
		m.mu.Lock()
		host := map[string]any{"cpus": m.host.cpus, "memory": m.host.memory}
		m.mu.Unlock()
		warn, crit := set.levels()
		jsonResponse(w, 200, map[string]any{"sandboxes": out, "host": host,
			"settings": map[string]any{"hide": set.Hide, "warn": warn, "crit": crit, "notify": set.Notify}})
	case "/api/resources/settings":
		switch r.Method {
		case http.MethodGet:
			if !s.require(w, r, false) {
				return
			}
			set := m.settings()
			warn, crit := set.levels()
			jsonResponse(w, 200, map[string]any{"hide": set.Hide, "warn": warn, "crit": crit, "notify": set.Notify})
		case http.MethodPost:
			if !s.require(w, r, true) {
				return
			}
			var body ResourceSettings
			if decode(w, r, &body) != nil {
				fail(w, 400, "invalid request")
				return
			}
			if err := s.updateConfig(func(c *Config) error { c.Resources = &body; return nil }); err != nil {
				failErr(w, 400, err)
				return
			}
			jsonResponse(w, 200, map[string]bool{"ok": true})
		default:
			fail(w, 405, "method not allowed")
		}
	case "/api/resources/measure":
		if r.Method != http.MethodPost {
			fail(w, 405, "method not allowed")
			return
		}
		if !s.require(w, r, true) {
			return
		}
		var body struct {
			Sandbox string `json:"sandbox"`
		}
		if decode(w, r, &body) != nil {
			fail(w, 400, "invalid request")
			return
		}
		sb, ok := s.lookupSandbox(body.Sandbox)
		m.mu.Lock()
		running := m.running[body.Sandbox]
		m.mu.Unlock()
		if !ok || !running {
			fail(w, 409, "not running")
			return
		}
		m.measure(sb, true)
		jsonResponse(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 404, "not found")
	}
}
