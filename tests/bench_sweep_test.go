//go:build e2e

// Pure-helper unit tests for the FEAT-002 benchmark mechanisms added to
// bench_shared.go: the rate-ladder / leg / route list parsers, the header
// byte-count arithmetic, the JSONL summary line marshalling, the cpuTopology
// pairing/disjointness check, and the /proc tree walk. No server is launched
// and no opt-in env is required, so these run under the normal
// `go test -tags e2e -run 'TestBench' ./tests/`. The /proc-dependent tests
// self-skip on non-Linux hosts (the real exercise is deferred to the container).

package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestParseIntListLadder covers rateLadder's pure backing parser: ascending OK,
// malformed/non-ascending/non-positive fatal, and the empty-defaults path.
func TestBenchParseIntListLadder(t *testing.T) {
	t.Run("ascending ok", func(t *testing.T) {
		got, err := parseIntList("20000,40000,60000", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []int{20000, 40000, 60000}
		if len(got) != len(want) {
			t.Fatalf("len=%d, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got[%d]=%d, want %d", i, got[i], want[i])
			}
		}
	})
	t.Run("not ascending", func(t *testing.T) {
		if _, err := parseIntList("40000,20000", true); err == nil {
			t.Fatalf("expected error for descending list")
		}
	})
	t.Run("non-positive", func(t *testing.T) {
		if _, err := parseIntList("0,100", true); err == nil {
			t.Fatalf("expected error for zero value")
		}
	})
	t.Run("malformed", func(t *testing.T) {
		if _, err := parseIntList("20000,abc", true); err == nil {
			t.Fatalf("expected error for non-integer")
		}
	})
	t.Run("empty", func(t *testing.T) {
		if _, err := parseIntList("", true); err == nil {
			t.Fatalf("expected error for empty list")
		}
	})
}

// TestRateLadderDefaults asserts rateLadder returns [cfg.rate] when the knob was
// not given, and the parsed ladder otherwise.
func TestBenchRateLadderDefaults(t *testing.T) {
	t.Run("defaults to single rate", func(t *testing.T) {
		cfg := benchConfig{rate: 20000, rateLadderSet: []int{20000}}
		got := rateLadder(t, cfg)
		if len(got) != 1 || got[0] != 20000 {
			t.Fatalf("rateLadder=%v, want [20000]", got)
		}
	})
	t.Run("uses explicit ladder", func(t *testing.T) {
		cfg := benchConfig{rate: 20000, rateLadderSet: []int{20000, 40000}, rateLadderGiven: true}
		got := rateLadder(t, cfg)
		if len(got) != 2 || got[0] != 20000 || got[1] != 40000 {
			t.Fatalf("rateLadder=%v, want [20000 40000]", got)
		}
	})
}

// TestParseNameListParsers covers the leg/route-order parsers: a valid subset is
// returned in order; an unknown name is an error.
func TestBenchParseNameListParsers(t *testing.T) {
	t.Run("valid leg subset", func(t *testing.T) {
		got, err := parseNameList("node,bun,tlang", knownLegs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 3 || got[0] != "node" || got[2] != "tlang" {
			t.Fatalf("got %v, want [node bun tlang]", got)
		}
	})
	t.Run("unknown leg fatal", func(t *testing.T) {
		if _, err := parseNameList("node,ruby", knownLegs); err == nil {
			t.Fatalf("expected error for unknown leg")
		}
	})
	t.Run("valid route subset", func(t *testing.T) {
		got, err := parseNameList("/work,/bench", knownRoutes())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 || got[0] != "/work" || got[1] != "/bench" {
			t.Fatalf("got %v, want [/work /bench]", got)
		}
	})
	t.Run("unknown route fatal", func(t *testing.T) {
		if _, err := parseNameList("/bench,/nope", knownRoutes()); err == nil {
			t.Fatalf("expected error for unknown route")
		}
	})
}

// TestOrderLegsAndRoutes asserts the reordering helpers put named items first in
// the given order and append any unnamed items in their original order.
func TestBenchOrderLegsAndRoutes(t *testing.T) {
	specs := []legSpec{{name: "tlang"}, {name: "node"}, {name: "bun"}}
	got := orderLegs(specs, []string{"bun", "tlang"})
	if got[0].name != "bun" || got[1].name != "tlang" || got[2].name != "node" {
		t.Fatalf("orderLegs=%v, want [bun tlang node]", got)
	}
	if same := orderLegs(specs, nil); len(same) != 3 || same[0].name != "tlang" {
		t.Fatalf("empty order should leave specs unchanged, got %v", same)
	}

	routes := []string{"/bench", "/json", "/work"}
	gr := orderRoutes(routes, []string{"/work"})
	if gr[0] != "/work" || gr[1] != "/bench" || gr[2] != "/json" {
		t.Fatalf("orderRoutes=%v, want [/work /bench /json]", gr)
	}
}

// TestHeaderByteCount asserts the on-wire header byte arithmetic on a synthetic
// header set.
func TestBenchHeaderByteCount(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json") // "Content-Type: application/json\r\n"
	h.Set("Content-Length", "23")             // "Content-Length: 23\r\n"

	// Status line "HTTP/1.1 200 OK\r\n" = 17 bytes.
	status := "HTTP/1.1 200 OK\r\n"
	ct := len("Content-Type") + len(": ") + len("application/json") + len("\r\n")
	cl := len("Content-Length") + len(": ") + len("23") + len("\r\n")
	want := len(status) + ct + cl + len("\r\n")

	got := headerByteCount(http.StatusOK, h)
	if got != want {
		t.Fatalf("headerByteCount=%d, want %d", got, want)
	}
}

// TestServingUnits asserts the leg -> serving-unit mapping (TLang 1, Node N-1,
// Bun N).
func TestBenchServingUnits(t *testing.T) {
	cases := []struct {
		leg   string
		procs int
		want  int
	}{
		{"tlang", 1, 1},
		{"node", 5, 4},
		{"bun", 4, 4},
		{"node", 0, 0},
	}
	for _, c := range cases {
		if got := servingUnits(c.leg, c.procs); got != c.want {
			t.Fatalf("servingUnits(%q,%d)=%d, want %d", c.leg, c.procs, got, c.want)
		}
	}
}

// TestParseReadinessThreads asserts the (N threads) cross-check parser.
func TestBenchParseReadinessThreads(t *testing.T) {
	n, ok := parseReadinessThreads("tlang: listening on http://127.0.0.1:8080 (4 threads)")
	if !ok || n != 4 {
		t.Fatalf("parseReadinessThreads got (%d,%v), want (4,true)", n, ok)
	}
	if _, ok := parseReadinessThreads("not a readiness line"); ok {
		t.Fatalf("expected ok=false for a non-readiness line")
	}
}

// TestSummaryLineMarshalling feeds a known fixed-rate summary line (with
// resource fields) and asserts the JSON contains the schema field names FEAT-003
// must match.
func TestBenchSummaryLineMarshalling(t *testing.T) {
	rate := 20000
	rss := 12.4
	cpuS := 3.1
	perMreq := 15.5
	tree := 1
	su := 1
	threads := 5
	line := summaryLine{
		Run: 2, Pass: "fixed", LegOrder: "node,bun,tlang", Route: "/work", Leg: "tlang",
		Version: "clang-release echo_server.tl", Rate: 20000,
		P50Ms: 0.172, P90Ms: 0.371, P99Ms: 1.147, P999Ms: 1.880, MaxMs: 11.355,
		Failed: 0, Total: 200000, RespBytes: 1407,
		ResourceRate: &rate, PeakRSSMiB: &rss, CPUSeconds: &cpuS, CPUSPerMreq: &perMreq,
		TreeProcs: &tree, ServingUnits: &su, Threads: &threads,
		CPUTopology: "disjoint", Perreq: 1, ClockHz: 100, CPUSAvailable: true,
		SweepSLOMs: []float64{2.5, 5, 10},
		Sweep:      []sweepStepJSON{{Rate: 20000, P99Ms: 1.147, Failed: 0, HoldsSLO: []bool{true, true, true}, GenBound: false}},
	}
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"run", "pass", "leg_order", "route", "leg", "version", "rate",
		"p50_ms", "p90_ms", "p99_ms", "p999_ms", "max_ms", "failed", "total", "resp_bytes",
		"resource_rate", "peak_rss_mib", "cpu_s", "cpu_s_per_mreq", "tree_procs",
		"serving_units", "threads", "cpu_topology", "perreq", "clock_hz",
		"cpu_s_available", "sweep_slo_ms", "sweep",
	} {
		if _, ok := m[key]; !ok {
			t.Fatalf("marshalled summary line missing field %q: %s", key, string(b))
		}
	}
	if m["pass"] != "fixed" {
		t.Fatalf("pass=%v, want fixed", m["pass"])
	}
}

// TestLadderLineOmitsResource asserts a ladder line carries no resource fields.
func TestBenchLadderLineOmitsResource(t *testing.T) {
	line := summaryLine{
		Run: 1, Pass: "ladder", Route: "/bench", Leg: "tlang",
		SweepSLOMs: []float64{2.5}, CPUTopology: "disjoint",
		Sweep: []sweepStepJSON{{Rate: 20000, HoldsSLO: []bool{true}}},
	}
	b, _ := json.Marshal(line)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"resource_rate", "peak_rss_mib", "cpu_s", "cpu_s_per_mreq", "tree_procs", "serving_units", "threads"} {
		if _, ok := m[key]; ok {
			t.Fatalf("ladder line should omit %q: %s", key, string(b))
		}
	}
}

// TestParseCPUInfoTopology feeds a synthetic /proc/cpuinfo and asserts the
// pairing + the four-distinct-cores / disjointness check.
func TestBenchParseCPUInfoTopology(t *testing.T) {
	// Mimic the §2 observed table: lanes (0,1)->core0, (2,3)->core1, ...
	cpuinfo := ""
	for i := 0; i < 12; i++ {
		cpuinfo += "processor\t: " + itoa(i) + "\n"
		cpuinfo += "physical id\t: 0\n"
		cpuinfo += "core id\t: " + itoa(i/2) + "\n"
		cpuinfo += "\n"
	}
	topo := parseCPUInfo(cpuinfo)
	if len(topo) != 12 {
		t.Fatalf("parseCPUInfo found %d logical CPUs, want 12", len(topo))
	}
	if topo[2].coreID != 1 || topo[6].coreID != 3 {
		t.Fatalf("unexpected core mapping: cpu2=%+v cpu6=%+v", topo[2], topo[6])
	}

	serverSet := map[int]bool{0: true, 2: true, 4: true, 6: true}
	genSet := map[int]bool{8: true, 10: true}
	res := checkCPUTopology(topo, serverSet, genSet)
	if res.status != "disjoint" || !res.distinct || !res.disjoint {
		t.Fatalf("expected disjoint 4-distinct-core split, got %+v", res)
	}

	// A flat VM (every lane core id 0) must be flagged shared.
	flat := map[int]cpuCore{}
	for i := 0; i < 12; i++ {
		flat[i] = cpuCore{physID: 0, coreID: 0}
	}
	fres := checkCPUTopology(flat, serverSet, genSet)
	if fres.status != "shared" || fres.distinct {
		t.Fatalf("flat topology should be shared/non-distinct, got %+v", fres)
	}
}

// itoa is a tiny local helper so the test does not import strconv just for this.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestTreePIDsAndSample exercises the /proc tree walk against this process plus
// a short-lived child. Linux-only — self-skips on the Windows host (the real
// exercise is deferred to the container, per FEAT-002).
func TestBenchTreePIDsAndSample(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("treePIDs/sampleProcTree are /proc-based; skipping on %s (deferred to the Linux container)", runtime.GOOS)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, "sleep", "2")
	if err := child.Start(); err != nil {
		t.Skipf("could not start sleep child: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	self := os.Getpid()
	pids := treePIDs(self)
	found := false
	for _, p := range pids {
		if p == child.Process.Pid {
			found = true
		}
	}
	if !found {
		t.Fatalf("treePIDs(%d) did not include child %d; got %v", self, child.Process.Pid, pids)
	}

	s, err := sampleProcTree(self)
	if err != nil {
		t.Fatalf("sampleProcTree: %v", err)
	}
	if s.rssKB <= 0 {
		t.Fatalf("sampleProcTree rssKB=%d, want > 0", s.rssKB)
	}
	if s.procCount < 1 {
		t.Fatalf("sampleProcTree procCount=%d, want >= 1", s.procCount)
	}
}
