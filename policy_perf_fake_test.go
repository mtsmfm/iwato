//go:build uitest

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// policyEditStep is one request the policy tab makes, and the gateway calls
// it may make at most.
type policyEditStep struct {
	name, method, path, body string
	calls                    int
}

// policyEditCycle are the policy tab's edits, each undone by a later one,
// on dev with a large policy and the GitHub provider attached.
func policyEditCycle() []policyEditStep {
	p := "/api/sandboxes/dev/policy/"
	steps := []policyEditStep{
		{"add (inspected)", "POST", p + "add", `{"rule":"bench_a","host":"a.example.test","ports":[443],"binaries":["/usr/bin/curl","/usr/bin/wget"],"rules":[{"method":"GET","path":"/a/**"}]}`, 5},
		{"add-allow", "POST", p + "add-allow", `{"rule":"bench_a","host":"a.example.test","ports":[443],"rules":[{"method":"POST","path":"/a/b"}]}`, 5},
		{"remove-allow", "POST", p + "remove-allow", `{"rule":"bench_a","host":"a.example.test","ports":[443],"rules":[{"method":"POST","path":"/a/b"}]}`, 3},
		{"add (to a rule)", "POST", p + "add", `{"rule":"bench_a","host":"a2.example.test","ports":[443],"access":"read-only"}`, 5},
		{"remove-endpoint", "POST", p + "remove-endpoint", `{"rule":"bench_a","host":"a2.example.test","ports":[443]}`, 2},
		{"remove-binary", "POST", p + "remove-binary", `{"rule":"bench_a","binary":"/usr/bin/wget"}`, 2},
		{"add (audit)", "POST", p + "add", `{"rule":"bench_b","host":"b.example.test","ports":[443],"binaries":["/usr/bin/curl"],"access":"read-only","audit":true}`, 5},
		{"enforce", "POST", p + "enforce", `{"rule":"bench_b","host":"b.example.test","ports":[443],"keep":true}`, 3},
		{"add (uninspected)", "POST", p + "add", `{"host":"c.example.test","ports":[443],"binaries":["/usr/bin/curl"]}`, 5},
		{"remove", "POST", p + "remove", `{"rule":"allow_c_example_test_443"}`, 2},
		{"remove", "POST", p + "remove", `{"rule":"bench_b"}`, 2},
		{"remove", "POST", p + "remove", `{"rule":"bench_a"}`, 2},
		{"yaml (read)", "GET", p + "yaml", "", 1},
		{"yaml (check)", "POST", p + "yaml", "", 4},
		{"yaml (apply)", "POST", p + "yaml", "", 7},
		{"preset apply", "POST", "/api/sandboxes/dev/presets/apply", `{"id":"user:mirror","params":{"repo":"o/bench"}}`, 7},
		{"preset remove", "POST", "/api/sandboxes/dev/presets/remove", `{"id":"user:mirror","params":{"repo":"o/bench"}}`, 5},
		{"proposals", "GET", "/api/sandboxes/dev/proposals", "", 6},
	}
	return steps
}

type benchServer struct {
	s  *Server
	d  *Fake
	do func(method, path, body string) (int, string)
}

func newBenchServer(tb testing.TB) benchServer {
	d := NewFake()
	d.seedProviders()
	d.seedPolicy = benchPolicy(tb)
	s := newServer(testConfig(), d, true, "127.0.0.1:29999")
	w := reviewRequest(s, "POST", "/api/session", `{"token":"`+s.bootstrap+`"}`, s.origin, "same-origin", s.host, nil, "")
	if w.Code != 200 {
		tb.Fatal(w.Code, w.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		cookie = c
	}
	var login struct{ CSRF string }
	_ = json.Unmarshal(w.Body.Bytes(), &login)
	do := func(method, path, body string) (int, string) {
		w := reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, login.CSRF)
		return w.Code, w.Body.String()
	}
	for _, step := range [][2]string{{"/api/sandboxes/dev/start", `{}`}, {"/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`}} {
		if code, body := do("POST", step[0], step[1]); code != 200 {
			tb.Fatal(step[0], code, body)
		}
	}
	return benchServer{s, d, do}
}

// run makes the cycle's requests: each one's time and gateway calls.
func (b benchServer) run(tb testing.TB) ([]time.Duration, []map[string]int) {
	var times []time.Duration
	var calls []map[string]int
	var version uint32
	var doc string
	for _, s := range policyEditCycle() {
		body := s.body
		if strings.HasPrefix(s.name, "yaml (") && s.method == "POST" {
			y := doc + "  bench_y:\n    endpoints:\n      - {host: y.example.test, port: 443, protocol: rest, enforcement: enforce, access: read-only}\n    binaries: [/usr/bin/curl]\n"
			j, _ := json.Marshal(map[string]any{"version": version, "yaml": y, "apply": s.name == "yaml (apply)"})
			body = string(j)
		}
		b.d.gatewayCalls()
		start := time.Now()
		code, out := b.do(s.method, s.path, body)
		times = append(times, time.Since(start))
		calls = append(calls, b.d.gatewayCalls())
		if code != 200 {
			tb.Fatal(s.name, code, out)
		}
		if s.name == "yaml (read)" {
			var v struct {
				Version uint32
				YAML    string
			}
			_ = json.Unmarshal([]byte(out), &v)
			version, doc = v.Version, v.YAML
		}
	}
	// The YAML's rule out again, so the next cycle starts as this one did.
	if code, out := b.do("POST", "/api/sandboxes/dev/policy/remove", `{"rule":"bench_y"}`); code != 200 {
		tb.Fatal(code, out)
	}
	return times, calls
}

func TestPolicyEditGatewayCalls(t *testing.T) {
	withUserPresets(t, map[string]string{"mirror.yaml": mirrorPreset})
	b := newBenchServer(t)
	times, calls := b.run(t)
	for i, s := range policyEditCycle() {
		n := 0
		for _, c := range calls[i] {
			n += c
		}
		t.Logf("%-18s %8s %2d %v", s.name, times[i].Round(10*time.Microsecond), n, calls[i])
		if n > s.calls {
			t.Errorf("%s: %d gateway calls, want at most %d: %v", s.name, n, s.calls, calls[i])
		}
	}
}

func BenchmarkPolicyEditCycle(b *testing.B) {
	withUserPresets(b, map[string]string{"mirror.yaml": mirrorPreset})
	s := newBenchServer(b)
	steps := policyEditCycle()
	total := make([]time.Duration, len(steps))
	n := 0
	for b.Loop() {
		times, _ := s.run(b)
		for i, t := range times {
			total[i] += t
		}
		n++
	}
	for i, st := range steps {
		b.ReportMetric(float64(total[i].Microseconds())/float64(n)/1000, strings.ReplaceAll(st.name, " ", "_")+"-ms")
	}
}
