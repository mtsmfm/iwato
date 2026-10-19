// fakewslc stands in for wslc.exe in e2e.sh: "containers" are records, cp
// extracts into the real root file system (with sudo), and start runs the
// real openshell-sandbox / openshell-supervisor binaries as local processes.
// There is no network isolation and no mount, and a container's first
// process is not PID 1. Only for a disposable container (CI): it writes
// under / and runs processes as root.
//
// It follows what is known of the real CLI (WSL's
// src/windows/wslc/commands and arguments, and what WSL 3.0.1 did):
// unknown options are refused, --network host is refused, stop honours
// --time and signals only the first process, a container keeps its real
// exit code, logs write the container's stderr to stderr, exec runs as the
// container's user, and images exist only once pulled (or preloaded with
// FAKE_IMAGES="ref=user,ref=user=workdir").
package main

import (
	"archive/tar"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ctr struct {
	ID, Name, Image, User, Entrypoint, Network, Status, Workdir string
	Labels                                                      map[string]string
	Env, Args                                                   []string
	PID                                                         int
	Exit                                                        int
	// GPU records --gpus all (WSLc inspect shows nothing about it).
	GPU bool
}

var state = os.Getenv("FAKE_WSLC_STATE")

// Options of the commands the driver uses: true takes a value. From
// ArgumentDefinitions.h and the Container*Command.cpp argument lists.
var options = map[string]map[string]bool{
	"container create": {"cidfile": true, "cpus": true, "dns": true, "dns-option": true, "dns-search": true, "domainname": true,
		"entrypoint": true, "env": true, "env-file": true, "gpus": true, "health-cmd": true, "health-interval": true,
		"health-retries": true, "health-start-period": true, "health-timeout": true, "hostname": true, "interactive": false,
		"ip": true, "label": true, "memory": true, "mount": true, "name": true, "network": true, "network-alias": true,
		"no-healthcheck": false, "publish": true, "publish-all": false, "pull": true, "rm": false, "shm-size": true,
		"stop-signal": true, "stop-timeout": true, "tmpfs": true, "tty": false, "ulimit": true, "user": true, "volume": true, "workdir": true},
	"container stop":    {"signal": true, "time": true},
	"container remove":  {"force": false, "volumes": false},
	"container exec":    {"detach": false, "env": true, "env-file": true, "interactive": false, "tty": false, "user": true, "workdir": true},
	"container logs":    {"details": false, "follow": false, "tail": true, "timestamps": false, "since": true, "until": true},
	"container list":    {"all": false, "quiet": false, "no-trunc": false, "filter": true, "format": true},
	"container inspect": {"format": true},
	"container start":   {"attach": false, "interactive": false},
	"container cp":      {},
	"image inspect":     {"format": true},
	"image pull":        {},
	"volume create":     {"label": true, "driver": true, "opt": true},
	"volume remove":     {"force": false},
	"volume inspect":    {"format": true},
	"volume list":       {"quiet": false, "filter": true, "format": true},
}

// parse splits the arguments after the command into options (in order)
// and positionals, refusing what the real CLI would not take.
func parse(cmd string, a []string) (opts [][2]string, pos []string) {
	known := options[cmd]
	for i := 0; i < len(a); i++ {
		arg := a[i]
		if len(pos) > 0 && (cmd == "container create" || cmd == "container exec") {
			// The image (or container) ends the options; the rest is the
			// command and its arguments.
			pos = append(pos, a[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") || arg == "-" {
			pos = append(pos, arg)
			continue
		}
		name, value, inline := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		takes, ok := known[name]
		if !ok {
			die("Error: unknown argument --%s", name)
		}
		if takes && !inline {
			if i+1 >= len(a) {
				die("Error: argument --%s needs a value", name)
			}
			i++
			value = a[i]
		}
		opts = append(opts, [2]string{name, value})
	}
	return opts, pos
}

func opt(opts [][2]string, name, def string) string {
	for _, o := range opts {
		if o[0] == name {
			def = o[1]
		}
	}
	return def
}

func has(opts [][2]string, name string) bool {
	for _, o := range opts {
		if o[0] == name {
			return true
		}
	}
	return false
}

type volume struct {
	Labels  map[string]string
	Created string
}

func locked(name string, v any) func() {
	lock, _ := os.OpenFile(filepath.Join(state, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
	if b, err := os.ReadFile(filepath.Join(state, name+".json")); err == nil {
		_ = json.Unmarshal(b, v)
	}
	return func() {
		b, _ := json.MarshalIndent(v, "", " ")
		_ = os.WriteFile(filepath.Join(state, name+".json"), b, 0o600)
		lock.Close()
	}
}

// images maps a pulled (or preloaded) reference to its image user, and
// "=workdir" after it when the image has a WORKDIR.
func images() (map[string]string, func()) {
	m := map[string]string{}
	done := locked("images", &m)
	for _, kv := range strings.Split(os.Getenv("FAKE_IMAGES"), ",") {
		if ref, user, ok := strings.Cut(kv, "="); ok {
			if _, seen := m[ref]; !seen {
				m[ref] = user
			}
		}
	}
	return m, done
}

func labelFilters(opts [][2]string) map[string]string {
	want := map[string]string{}
	for _, o := range opts {
		if o[0] == "filter" {
			k, v, _ := strings.Cut(strings.TrimPrefix(o[1], "label="), "=")
			want[k] = v
		}
	}
	return want
}

func matches(labels, want map[string]string) bool {
	for k, v := range want {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func load() (map[string]*ctr, func()) {
	db := map[string]*ctr{}
	done := locked("db", &db)
	for _, c := range db {
		if c.Status == "running" && !alive(c.PID) {
			c.Status, c.Exit = "exited", exitCode(c)
		}
	}
	return db, done
}

// exitCode is what the container's first process exited with (128+signal
// when a signal ended it), or 137 when it was killed with its wrapper.
func exitCode(c *ctr) int {
	b, err := os.ReadFile(filepath.Join(state, c.ID+".exit"))
	if n, err2 := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && err2 == nil {
		return n
	}
	return 137
}

// alive reports whether any process of the container remains: start runs
// it in a new session, so every process of the container carries the
// session id (the wrapper's pid), as a container runs while any of its
// processes does.
func alive(sid int) bool { return len(pids(sid, false)) > 0 }

// pids lists the session's processes; first, only the wrapper's child (the
// container's first process).
func pids(sid int, first bool) []string {
	if sid <= 0 {
		return nil
	}
	var ps []string
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:]) // state ppid pgrp session ...
		if len(f) < 4 || f[0] == "Z" || f[3] != fmt.Sprint(sid) {
			continue
		}
		if first && f[1] != fmt.Sprint(sid) {
			continue
		}
		ps = append(ps, e.Name())
	}
	return ps
}

var hexID = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

func find(db map[string]*ctr, ref string) *ctr {
	for _, c := range db {
		if c.ID == ref || c.Name == ref || (hexID.MatchString(ref) && strings.HasPrefix(c.ID, ref)) {
			return c
		}
	}
	return nil
}

// missing reports a container that does not exist as WSLc does: `[]` for
// inspect by name, the engine's "No such container" otherwise.
func missing(cmd, ref string) {
	if cmd == "container inspect" && !hexID.MatchString(ref) {
		fmt.Println("[]")
		os.Exit(1)
	}
	die("Error: No such container: %s", ref)
}

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }

func osUser(user string) string {
	switch user {
	case "65534:65534", "nobody":
		return "nobody"
	case "", "0", "0:0", "root":
		return "root"
	}
	return strings.Split(user, ":")[0]
}

func main() {
	a := os.Args[1:]
	logf, _ := os.OpenFile(filepath.Join(state, "calls.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	fmt.Fprintln(logf, strings.Join(a, " "))
	logf.Close()
	if len(a) < 2 {
		die("usage")
	}
	cmd := a[0] + " " + a[1]
	if _, ok := options[cmd]; !ok {
		die("fakewslc: unsupported %v", a)
	}
	opts, pos := parse(cmd, a[2:])
	switch cmd {
	case "image inspect":
		m, done := images()
		v, ok := m[pos[0]]
		done()
		if !ok {
			fmt.Println("[]")
			os.Exit(1)
		}
		user, workdir, _ := strings.Cut(v, "=")
		fmt.Printf(`[{"Id":"sha256:%x","Config":{"User":%q,"WorkingDir":%q}}]`+"\n", []byte(pos[0])[:8], user, workdir)
	case "image pull":
		m, done := images()
		if _, ok := m[pos[0]]; !ok {
			m[pos[0]] = ""
		}
		done()
	case "volume create":
		vs := map[string]*volume{}
		done := locked("volumes", &vs)
		v := &volume{Labels: map[string]string{}, Created: time.Now().UTC().Format(time.RFC3339)}
		for _, o := range opts {
			if o[0] == "label" {
				k, val, _ := strings.Cut(o[1], "=")
				v.Labels[k] = val
			}
		}
		vs[pos[0]] = v
		done()
	case "volume remove":
		vs := map[string]*volume{}
		done := locked("volumes", &vs)
		delete(vs, pos[0])
		done()
	case "volume inspect":
		// Several names: those found, and status 1 when one is missing.
		vs := map[string]*volume{}
		done := locked("volumes", &vs)
		done()
		found, missing := []any{}, false
		for _, n := range pos {
			v := vs[n]
			if v == nil {
				fmt.Fprintf(os.Stderr, "Volume not found: '%s'\n", n)
				missing = true
				continue
			}
			var labels any
			if len(v.Labels) > 0 {
				labels = v.Labels
			}
			found = append(found, map[string]any{"Name": n, "Driver": "guest", "CreatedAt": v.Created, "Mountpoint": "/var/lib/docker/volumes/" + n + "/_data", "Scope": "local", "Labels": labels, "Options": nil})
		}
		out, _ := json.Marshal(found)
		fmt.Println(string(out))
		if missing {
			os.Exit(1)
		}
	case "volume list":
		vs := map[string]*volume{}
		done := locked("volumes", &vs)
		done()
		want := labelFilters(opts)
		var names []string
		for n, v := range vs {
			if matches(v.Labels, want) {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Println(n)
		}
	case "container create":
		c := &ctr{Labels: map[string]string{}, Status: "created", Network: "bridge"}
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		c.ID = hex.EncodeToString(b)
		for _, o := range opts {
			switch o[0] {
			case "name":
				c.Name = o[1]
			case "label":
				k, v, _ := strings.Cut(o[1], "=")
				c.Labels[k] = v
			case "network":
				if strings.EqualFold(o[1], "host") {
					die("Error: host network mode is not supported")
				}
				c.Network = o[1]
			case "user":
				c.User = o[1]
			case "entrypoint":
				c.Entrypoint = o[1]
			case "env":
				c.Env = append(c.Env, o[1])
			case "workdir":
				c.Workdir = o[1]
			case "gpus":
				// WSL: ArgumentValidation.cpp accepts only "all"; a session
				// without GPU support (FAKE_WSLC_GPU=0) refuses it.
				if o[1] != "all" {
					die("Invalid gpus value: '%s'. Only 'all' is supported.", o[1])
				}
				if os.Getenv("FAKE_WSLC_GPU") == "0" {
					die("Error: GPU support not enabled on the session")
				}
				c.GPU = true
			}
		}
		if len(pos) == 0 {
			die("Error: the image is required")
		}
		c.Image, c.Args = pos[0], pos[1:]
		m, idone := images()
		_, present := m[c.Image]
		if !present && strings.HasPrefix(c.Image, "sha256:") {
			for ref := range m {
				if fmt.Sprintf("sha256:%x", []byte(ref)[:8]) == c.Image {
					present = true
				}
			}
		}
		idone()
		if !present && opt(opts, "pull", "missing") == "never" {
			die("Error: No such image: %s", c.Image)
		}
		db, done := load()
		if c.Name != "" && find(db, c.Name) != nil {
			done()
			die("Error: Conflict. The container name %q is already in use", c.Name)
		}
		db[c.ID] = c
		done()
		fmt.Println(c.ID)
	case "container inspect":
		db, done := load()
		c := find(db, pos[0])
		done()
		if c == nil {
			missing(cmd, pos[0])
		}
		nets := map[string]any{c.Network: map[string]any{}}
		if c.Network == "none" {
			nets = map[string]any{}
		}
		out, _ := json.Marshal([]any{map[string]any{
			"Id": c.ID, "Name": "/" + c.Name, "Image": c.Image,
			"State":           map[string]any{"Status": c.Status, "Running": c.Status == "running", "ExitCode": c.Exit},
			"HostConfig":      map[string]any{"NetworkMode": c.Network},
			"Config":          map[string]any{"User": c.User, "Labels": c.Labels, "WorkingDir": c.Workdir},
			"Labels":          c.Labels,
			"NetworkSettings": map[string]any{"Networks": nets},
		}})
		fmt.Println(string(out))
	case "container list":
		db, done := load()
		done()
		want := labelFilters(opts)
		var ids []string
		for _, c := range db {
			if (c.Status == "running" || has(opts, "all")) && matches(c.Labels, want) {
				id := c.ID
				if !has(opts, "no-trunc") {
					id = id[:12]
				}
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Println(id)
		}
	case "container cp":
		src, dst := pos[0], pos[1]
		db, done := load()
		done()
		if src == "-" {
			ref, dir, _ := strings.Cut(dst, ":")
			if find(db, ref) == nil {
				missing(cmd, ref)
			}
			_ = exec.Command("sudo", "mkdir", "-p", dir).Run()
			cmd := exec.Command("sudo", "tar", "-x", "--same-owner", "-p", "-C", dir)
			cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
			if err := cmd.Run(); err != nil {
				die("cp: %v", err)
			}
			return
		}
		// Out of a container only to stdout, as a tar of the path (Lstat:
		// a symlink is a symlink entry).
		if dst != "-" {
			die("cp: fakewslc copies out only to -")
		}
		ref, path, _ := strings.Cut(src, ":")
		if find(db, ref) == nil {
			missing(cmd, ref)
		}
		from := path
		if path == "/openshell-sandbox" {
			from = os.Getenv("FAKE_SANDBOX_BIN")
		}
		info, err := os.Lstat(from)
		if err != nil {
			die("cp: %v", err)
		}
		link, _ := os.Readlink(from)
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			die("cp: %v", err)
		}
		h.Name = filepath.Base(path)
		tw := tar.NewWriter(os.Stdout)
		if err := tw.WriteHeader(h); err != nil {
			die("cp: %v", err)
		}
		if info.Mode().IsRegular() {
			in, err := os.Open(from)
			if err != nil {
				die("cp: %v", err)
			}
			_, _ = io.Copy(tw, in)
			in.Close()
		}
		if err := tw.Close(); err != nil {
			die("cp: %v", err)
		}
	case "container start":
		db, done := load()
		c := find(db, pos[0])
		if c == nil {
			done()
			missing(cmd, pos[0])
		}
		entry := c.Entrypoint
		if entry == "/openshell-supervisor" {
			entry = os.Getenv("FAKE_SUPERVISOR_BIN")
		}
		args := append([]string{"-u", osUser(c.User), "env"}, c.Env...)
		args = append(append(args, entry), c.Args...)
		_ = os.Remove(filepath.Join(state, c.ID+".exit"))
		stdout, _ := os.OpenFile(filepath.Join(state, c.ID+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		stderr, _ := os.OpenFile(filepath.Join(state, c.ID+".err"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		// The wrapper records the first process's exit code, as the
		// engine does; sudo relays signals to its command.
		wrap := append([]string{"-c", `sudo "$@"; echo $? > "$EXIT_FILE"`, "fakewslc-init"}, args...)
		run := exec.Command("sh", wrap...)
		run.Env = append(os.Environ(), "EXIT_FILE="+filepath.Join(state, c.ID+".exit"))
		run.Stdout, run.Stderr = stdout, stderr
		run.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := run.Start(); err != nil {
			done()
			die("start: %v", err)
		}
		c.PID, c.Status = run.Process.Pid, "running"
		done()
	case "container stop", "container remove":
		db, done := load()
		c := find(db, pos[0])
		if c == nil {
			done()
			if cmd == "container remove" && has(opts, "force") {
				return
			}
			missing(cmd, pos[0])
		}
		if cmd == "container remove" && c.Status == "running" && !has(opts, "force") {
			done()
			die("Error: cannot remove a running container; stop it or use --force")
		}
		if cmd == "container stop" && c.Status == "running" {
			// Docker signals only the container's first process and kills
			// what is left after --time seconds.
			wait, _ := strconv.Atoi(opt(opts, "time", "10"))
			for _, p := range pids(c.PID, true) {
				_ = exec.Command("sudo", "kill", "-TERM", p).Run()
			}
			for deadline := time.Now().Add(time.Duration(wait) * time.Second); alive(c.PID) && time.Now().Before(deadline); {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if ps := pids(c.PID, false); len(ps) > 0 {
			_ = exec.Command("sudo", append([]string{"kill", "-KILL"}, ps...)...).Run()
			for i := 0; i < 50 && alive(c.PID); i++ {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if c.Status == "running" {
			c.Status, c.Exit = "exited", exitCode(c)
		}
		if cmd == "container remove" {
			delete(db, c.ID)
		}
		done()
	case "container exec":
		db, done := load()
		c := find(db, pos[0])
		done()
		if c == nil {
			missing(cmd, pos[0])
		}
		if c.Status != "running" {
			die("Error: container %s is not running", pos[0])
		}
		args := pos[1:]
		if args[0] == "/openshell-supervisor" {
			args[0] = os.Getenv("FAKE_SUPERVISOR_BIN")
		}
		run := exec.Command("sudo", append([]string{"-u", osUser(opt(opts, "user", c.User))}, args...)...)
		run.Stdout, run.Stderr = os.Stdout, os.Stderr
		if has(opts, "interactive") {
			run.Stdin = os.Stdin
		}
		if err := run.Run(); err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				os.Exit(e.ExitCode())
			}
			os.Exit(126)
		}
	case "container logs":
		db, done := load()
		c := find(db, pos[0])
		done()
		if c == nil {
			missing(cmd, pos[0])
		}
		// The container's stdout to stdout and its stderr to stderr.
		tail, _ := strconv.Atoi(opt(opts, "tail", "-1"))
		for _, f := range []struct {
			ext string
			w   *os.File
		}{{".log", os.Stdout}, {".err", os.Stderr}} {
			b, _ := os.ReadFile(filepath.Join(state, c.ID+f.ext))
			lines := strings.SplitAfter(string(b), "\n")
			if tail >= 0 && len(lines) > tail {
				lines = lines[len(lines)-tail:]
			}
			f.w.WriteString(strings.Join(lines, ""))
		}
	}
}
