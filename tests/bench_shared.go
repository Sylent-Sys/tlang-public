//go:build e2e

// Shared benchmark machinery (tests-suites design §5.6). The runtime-neutral
// pieces that both TestBenchmark (single-runtime, TLANG_BENCH=1) and
// TestBenchmarkCompare (comparative, TLANG_BENCH_COMPARE=1) consume live here so
// there is exactly ONE load generator, ONE histogram, ONE pinning/threads
// derivation, and ONE gate across every runtime leg (TLang, Node, Bun). Moving
// this code out of benchmark_test.go into the same package is behavior-
// preserving; the single-runtime TestBenchmark keeps byte-for-byte identical
// measurements and gate behavior.
//
// Everything here is behind //go:build e2e so it stays out of the default host
// build, and it uses only net/http + stdlib plus the existing e2e harness
// (buildFixture, serverHandle, newHTTPDriver, parseListening, the timeout
// constants).

package tests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// benchDefaults are the recorded default knob values (design §6.2). They are
// part of the reproducibility record printed in the report.
const (
	defaultBenchRate     = 20000           // req/s
	defaultBenchConns    = 50              // persistent keep-alive connections
	defaultBenchWarmup   = 3 * time.Second // discarded warm-up window
	defaultBenchDuration = 10 * time.Second
)

// p99Threshold is the SLO the gate enforces under pinning (design §6.1/§6.4).
const p99Threshold = 2500 * time.Microsecond // 2.5 ms

// Defaults for the new FEAT-002 knobs (design §A.3/§B.5). These are chosen so
// the default run (no new knobs set) is byte-identical to today.
var (
	defaultSweepSLOs        = []time.Duration{2500 * time.Microsecond, 5 * time.Millisecond, 10 * time.Millisecond}
	defaultSweepWarmup      = 1 * time.Second
	defaultSweepDuration    = 5 * time.Second
	defaultResourceInterval = 100 * time.Millisecond
)

// knownLegs / knownRoutes bound the leg/route-order knobs (design §C.3/§C.4).
var knownLegs = map[string]bool{"tlang": true, "node": true, "bun": true}

// defaultServerCores / defaultGenCores are the §2 observed-topology layout for
// this box (Ryzen 5 5600 under Docker Desktop): server on one SMT lane of each
// of four distinct physical cores, generator on two further distinct cores.
const (
	defaultServerCores = "0,2,4,6"
	defaultGenCores    = "8,10"
)

// knownClockHz is the §B.3 sanity-bound set of Linux CONFIG_HZ values. A probed
// candidate outside this set marks CPU-s unavailable rather than silently
// corrupting the CPU-s/Mreq headline.
var knownClockHz = map[int]bool{100: true, 250: true, 300: true, 1000: true}

// benchConfig is the parsed, validated knob set for one benchmark run.
type benchConfig struct {
	rate        int           // R, requests/second
	conns       int           // C, persistent keep-alive connections
	warmup      time.Duration // discarded warm-up window
	duration    time.Duration // measured window
	serverCores string        // TLANG_BENCH_SERVER_CORES, "" if unset
	path        string        // request path the generator drives
	refBody     string        // expected response body for the parity gate on path

	// FEAT-002 additive knobs. All default so the fixed-rate run is unchanged.
	rateLadderSet   []int           // TLANG_BENCH_RATE_LADDER (default [rate])
	rateLadderGiven bool            // true when the ladder knob was explicitly set
	sweepSLOs       []time.Duration // TLANG_BENCH_SWEEP_SLO
	sweepConns      int             // TLANG_BENCH_SWEEP_CONNS; 0 == scale with rate
	sweepWarmup     time.Duration   // TLANG_BENCH_SWEEP_WARMUP
	sweepDuration   time.Duration   // TLANG_BENCH_SWEEP_DURATION
	resource        bool            // TLANG_BENCH_RESOURCE
	resourceEvery   time.Duration   // TLANG_BENCH_RESOURCE_INTERVAL
	clockHzCalib    bool            // TLANG_BENCH_CLOCKHZ_CALIBRATE
	perreq          bool            // TLANG_BENCH_PERREQ
	allowSharedCore bool            // TLANG_BENCH_ALLOW_SHARED_CORES
	p99Advisory     bool            // TLANG_BENCH_P99_ADVISORY: downgrade ONLY the TLang p99 breach to advisory
	legOrder        []string        // TLANG_BENCH_LEG_ORDER
	routeOrder      []string        // TLANG_BENCH_ROUTE_ORDER
	summaryOut      string          // TLANG_BENCH_SUMMARY_OUT
}

// parseBenchConfig reads the TLANG_BENCH_* knobs with validated defaults. A
// malformed value fails the test loudly rather than silently using a default
// (design §6.2).
func parseBenchConfig(t *testing.T) benchConfig {
	t.Helper()
	cfg := benchConfig{
		rate:        defaultBenchRate,
		conns:       defaultBenchConns,
		warmup:      defaultBenchWarmup,
		duration:    defaultBenchDuration,
		serverCores: strings.TrimSpace(os.Getenv("TLANG_BENCH_SERVER_CORES")),
	}
	cfg.rate = envInt(t, "TLANG_BENCH_RATE", cfg.rate)
	cfg.conns = envInt(t, "TLANG_BENCH_CONNS", cfg.conns)
	cfg.warmup = envDuration(t, "TLANG_BENCH_WARMUP", cfg.warmup)
	cfg.duration = envDuration(t, "TLANG_BENCH_DURATION", cfg.duration)

	// TLANG_BENCH_PATH is an optional single-route override. When unset,
	// cfg.path stays "" (the "unset" sentinel): TestBenchmark pins it to /bench
	// and TestBenchmarkCompare drives the full route list. When set, it must be
	// a known benchmarkable route, else fail loudly like any other malformed
	// knob.
	if raw := strings.TrimSpace(os.Getenv("TLANG_BENCH_PATH")); raw != "" {
		body, ok := resolveBenchRoute(raw)
		if !ok {
			t.Fatalf("TLANG_BENCH_PATH=%q: unknown route (want one of /bench, /json, /work)", raw)
		}
		cfg.path = raw
		cfg.refBody = body
	}

	// --- FEAT-002 additive knobs (design §A.3/§B.5/§C.3/§C.5/§11) ---

	// Rate ladder: comma ints, strictly ascending, each >0. Default [cfg.rate].
	if raw := strings.TrimSpace(os.Getenv("TLANG_BENCH_RATE_LADDER")); raw != "" {
		cfg.rateLadderSet = envIntList(t, "TLANG_BENCH_RATE_LADDER", raw, true)
		cfg.rateLadderGiven = true
	} else {
		cfg.rateLadderSet = []int{cfg.rate}
	}

	cfg.sweepSLOs = defaultSweepSLOs
	if raw := strings.TrimSpace(os.Getenv("TLANG_BENCH_SWEEP_SLO")); raw != "" {
		cfg.sweepSLOs = envDurationList(t, "TLANG_BENCH_SWEEP_SLO", raw)
	}

	// Sweep conns: 0/unset == scale with rate; a positive int holds it fixed.
	// Unlike envInt (which rejects <=0), 0 is a meaningful "scale" sentinel.
	cfg.sweepConns = envNonNegInt(t, "TLANG_BENCH_SWEEP_CONNS", 0)

	cfg.sweepWarmup = envDuration(t, "TLANG_BENCH_SWEEP_WARMUP", defaultSweepWarmup)
	cfg.sweepDuration = envDuration(t, "TLANG_BENCH_SWEEP_DURATION", defaultSweepDuration)
	cfg.resource = envBool(t, "TLANG_BENCH_RESOURCE")
	cfg.resourceEvery = envDuration(t, "TLANG_BENCH_RESOURCE_INTERVAL", defaultResourceInterval)
	cfg.clockHzCalib = envBool(t, "TLANG_BENCH_CLOCKHZ_CALIBRATE")
	cfg.perreq = envBool(t, "TLANG_BENCH_PERREQ")
	cfg.allowSharedCore = envBool(t, "TLANG_BENCH_ALLOW_SHARED_CORES")
	cfg.p99Advisory = envBool(t, "TLANG_BENCH_P99_ADVISORY")
	cfg.legOrder = envNameList(t, "TLANG_BENCH_LEG_ORDER", knownLegs, "leg")
	cfg.routeOrder = envNameList(t, "TLANG_BENCH_ROUTE_ORDER", knownRoutes(), "route")
	cfg.summaryOut = strings.TrimSpace(os.Getenv("TLANG_BENCH_SUMMARY_OUT"))

	return cfg
}

// envInt reads a positive integer env var or fails loudly on a malformed value.
func envInt(t *testing.T, key string, def int) int {
	t.Helper()
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("%s=%q: not an integer: %v", key, s, err)
	}
	if v <= 0 {
		t.Fatalf("%s=%q: must be a positive integer", key, s)
	}
	return v
}

// envDuration reads a positive duration env var or fails loudly on a malformed
// value.
func envDuration(t *testing.T, key string, def time.Duration) time.Duration {
	t.Helper()
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		t.Fatalf("%s=%q: not a duration: %v", key, s, err)
	}
	if v <= 0 {
		t.Fatalf("%s=%q: must be a positive duration", key, s)
	}
	return v
}

// envBool reads a 0/1-style boolean knob: only the literal "1" is true; any
// other value (including unset) is false (design §11 external-input table).
func envBool(t *testing.T, key string) bool {
	t.Helper()
	return strings.TrimSpace(os.Getenv(key)) == "1"
}

// envNonNegInt reads a non-negative integer knob (0 allowed as a sentinel),
// failing loudly on a non-integer or a negative value.
func envNonNegInt(t *testing.T, key string, def int) int {
	t.Helper()
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("%s=%q: not an integer: %v", key, s, err)
	}
	if v < 0 {
		t.Fatalf("%s=%q: must be >= 0", key, s)
	}
	return v
}

// parseIntList parses a comma list of ints. When ascending is true each value
// must be > 0 and the list strictly ascending. It is the pure helper behind
// envIntList so the error cases are unit-testable without catching t.Fatalf.
func parseIntList(raw string, ascending bool) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty list")
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	prev := 0
	for _, p := range parts {
		p = strings.TrimSpace(p)
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", p)
		}
		if ascending {
			if v <= 0 {
				return nil, fmt.Errorf("%d: must be a positive integer", v)
			}
			if len(out) > 0 && v <= prev {
				return nil, fmt.Errorf("%d not strictly greater than %d (list must be ascending)", v, prev)
			}
		}
		out = append(out, v)
		prev = v
	}
	return out, nil
}

// envIntList reads a comma list of ints, failing loudly on any malformed or
// (when ascending) non-ascending value (design §11, same contract as envInt).
func envIntList(t *testing.T, key, raw string, ascending bool) []int {
	t.Helper()
	list, err := parseIntList(raw, ascending)
	if err != nil {
		t.Fatalf("%s=%q: %v", key, raw, err)
	}
	return list
}

// parseDurationList parses a comma list of positive durations. Pure helper
// behind envDurationList.
func parseDurationList(raw string) ([]time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty list")
	}
	parts := strings.Split(raw, ",")
	out := make([]time.Duration, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		v, err := time.ParseDuration(p)
		if err != nil {
			return nil, fmt.Errorf("%q is not a duration", p)
		}
		if v <= 0 {
			return nil, fmt.Errorf("%s: must be a positive duration", v)
		}
		out = append(out, v)
	}
	return out, nil
}

// envDurationList reads a comma list of positive durations, failing loudly on a
// malformed value (design §11, same contract as envDuration).
func envDurationList(t *testing.T, key, raw string) []time.Duration {
	t.Helper()
	list, err := parseDurationList(raw)
	if err != nil {
		t.Fatalf("%s=%q: %v", key, raw, err)
	}
	return list
}

// parseNameList parses a comma list of names, validating each against known and
// returning the ordered subset. Pure helper behind envNameList; an unknown name
// yields an error naming it.
func parseNameList(raw string, known map[string]bool) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("empty name in list")
		}
		if !known[p] {
			return nil, fmt.Errorf("unknown name %q", p)
		}
		out = append(out, p)
	}
	return out, nil
}

// envNameList reads a comma list of names validated against the known set,
// failing loudly on an unknown name (design §C.3/§C.4/§11). kind labels the knob
// ("leg"/"route") in the error. Unset returns nil (today's order).
func envNameList(t *testing.T, key string, known map[string]bool, kind string) []string {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	list, err := parseNameList(raw, known)
	if err != nil {
		t.Fatalf("%s=%q: %v (want a subset of the known %s set)", key, raw, err, kind)
	}
	return list
}

// intendedSendTime is the pure open-loop schedule helper (design §6.2): the
// intended send time of request i is start + i/R. Latency is measured from this
// time (not the actual send time) so a late dispatch counts its lateness as
// latency — the wrk2 coordinated-omission correction. Factored out so it is
// unit-testable without a server.
func intendedSendTime(start time.Time, i int64, rate int) time.Time {
	// i/R seconds, expressed in nanoseconds without accumulating float error:
	// offset = i * 1e9 / R.
	offsetNS := i * int64(time.Second) / int64(rate)
	return start.Add(time.Duration(offsetNS))
}

// cpuModel reads the CPU model string from /proc/cpuinfo best-effort (container
// Linux); it returns "" when unavailable (design §6.4).
func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "model name") {
			if idx := strings.IndexByte(line, ':'); idx >= 0 {
				return strings.TrimSpace(line[idx+1:])
			}
		}
	}
	return ""
}

// tasksetAvailable reports whether taskset is on PATH (required for enforced
// pinning, design §6.1).
func tasksetAvailable() bool {
	_, err := exec.LookPath("taskset")
	return err == nil
}

// parseCoreSet parses a taskset -c list ("0,1" or "2-3" or "0,2-3") into the set
// of core numbers. It returns ok=false for an empty or malformed spec.
func parseCoreSet(spec string) (map[int]bool, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, false
	}
	set := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			a, err1 := strconv.Atoi(strings.TrimSpace(lo))
			b, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil || a < 0 || b < a {
				return nil, false
			}
			for c := a; c <= b; c++ {
				set[c] = true
			}
			continue
		}
		c, err := strconv.Atoi(part)
		if err != nil || c < 0 {
			return nil, false
		}
		set[c] = true
	}
	if len(set) == 0 {
		return nil, false
	}
	return set, true
}

// generatorCores returns the generator's pinned core set by reading this
// process's CPU affinity (set by the outer `taskset -c <genCores> go test ...`),
// or ok=false if it cannot be determined. Read from /proc/self/status
// Cpus_allowed_list (stdlib only, no x/sys dependency). The enforced gate
// requires this to be a non-empty set disjoint from the server's.
func generatorCores() (map[int]bool, bool) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return nil, false
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "Cpus_allowed_list:"); ok {
			return parseCoreSet(strings.TrimSpace(v))
		}
	}
	return nil, false
}

// disjointNonEmpty reports whether a and b are both non-empty and share no core.
func disjointNonEmpty(a, b map[int]bool) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for c := range a {
		if b[c] {
			return false
		}
	}
	return true
}

// coreSetString renders a core set as a sorted comma list for the report.
func coreSetString(set map[int]bool) string {
	cores := make([]int, 0, len(set))
	for c := range set {
		cores = append(cores, c)
	}
	// simple insertion sort (small sets)
	for i := 1; i < len(cores); i++ {
		for j := i; j > 0 && cores[j-1] > cores[j]; j-- {
			cores[j-1], cores[j] = cores[j], cores[j-1]
		}
	}
	parts := make([]string, len(cores))
	for i, c := range cores {
		parts[i] = strconv.Itoa(c)
	}
	return strings.Join(parts, ",")
}

// msf converts a duration to milliseconds as a float for reporting.
func msf(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// benchPinning reproduces TestBenchmark's pinning decision: it reads the
// server/generator core sets from cfg, whether taskset is available, and
// whether the two sets are disjoint-and-nonempty (the "pinned" gate mode).
// Extracted so TestBenchmark and TestBenchmarkCompare derive the gate mode via
// one code path and cannot drift (design §5.6, finding 5).
func benchPinning(cfg benchConfig) (pinned bool, serverSet, genSet map[int]bool, hasTaskset bool) {
	hasTaskset = tasksetAvailable()
	serverSet, serverOK := parseCoreSet(cfg.serverCores)
	genSet, genOK := generatorCores()
	pinned = hasTaskset && serverOK && genOK && disjointNonEmpty(serverSet, genSet)
	return pinned, serverSet, genSet, hasTaskset
}

// benchThreads reproduces TestBenchmark's exact TLANG_THREADS precedence:
//
//	runtime.NumCPU() by default
//	-> len(serverSet) when pinned
//	-> an explicit $TLANG_THREADS env value WINS over both.
//
// Both benchmarks call this so N is identical across the three runtimes
// (design §5.6, finding 5).
func benchThreads(pinned bool, serverSet map[int]bool) string {
	threads := strconv.Itoa(runtime.NumCPU())
	if pinned {
		threads = strconv.Itoa(len(serverSet))
	}
	if v := strings.TrimSpace(os.Getenv("TLANG_THREADS")); v != "" {
		threads = v
	}
	return threads
}

// benchTarget is a launched benchmark server of any runtime (TLang, Node, Bun).
// It owns the child process, the parsed listen address, the single cmd.Wait()
// result channel, and the stderr buffer kept for failure diagnostics. It is the
// runtime-neutral generalization of the former benchServer (design §5.6).
type benchTarget struct {
	name       string // "tlang" | "node" | "bun"
	cmd        *exec.Cmd
	addr       string // "127.0.0.1:<port>"
	port       int
	threads    string
	version    string // runtime version string for the report
	waitResult chan error
	stderr     *strings.Builder
	stderrMu   sync.Mutex
	stopOnce   sync.Once
	readyLine  string // the readiness line used for the (N threads) cross-check (D5)
}

// startBenchTarget launches argv (optionally taskset-pinned), sets the TLANG_*
// env, parses the readiness line via parseListening, runs the /healthz probe,
// and returns a ready target. It is the former startBenchServer generalized to
// an arbitrary argv instead of a hard-coded single binary. When pinned, the
// taskset prefix is prepended with NO "--" separator, byte-identical to the
// code it generalizes (design §5.6): the effective command is
// `taskset -c <cores> <argv...>`, which execs the target in place and keeps it
// the direct child whose stderr the harness captures.
func startBenchTarget(t *testing.T, ctx context.Context, name string, argv []string, serverCores, threads string, pinned bool) *benchTarget {
	t.Helper()

	if pinned && serverCores != "" {
		argv = append([]string{"taskset", "-c", serverCores}, argv...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(),
		"TLANG_PORT=0",
		"TLANG_HOST=127.0.0.1",
		"TLANG_THREADS="+threads,
	)

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	tg := &benchTarget{
		name:       name,
		cmd:        cmd,
		threads:    threads,
		waitResult: make(chan error, 1),
		stderr:     &strings.Builder{},
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start bench server: %v", err)
	}

	scannerDone := make(chan struct{})
	portCh := make(chan int, 1)
	go func() {
		sc := bufio.NewScanner(stderrPipe)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		sent := false
		for sc.Scan() {
			line := sc.Text()
			tg.stderrMu.Lock()
			tg.stderr.WriteString(line)
			tg.stderr.WriteByte('\n')
			tg.stderrMu.Unlock()
			if !sent {
				if _, port, ok := parseListening(line); ok {
					sent = true
					tg.readyLine = line
					portCh <- port
				}
			}
		}
		close(scannerDone)
	}()
	go func() {
		<-scannerDone
		tg.waitResult <- cmd.Wait()
	}()

	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()

	select {
	case port := <-portCh:
		tg.port = port
		tg.addr = fmt.Sprintf("127.0.0.1:%d", port)
	case werr := <-tg.waitResult:
		t.Fatalf("bench server exited before listening: %v\n--- stderr ---\n%s", werr, tg.stderrSnapshot())
	case <-deadline.C:
		tg.kill()
		t.Fatalf("bench server timed out waiting for listening line\n--- stderr ---\n%s", tg.stderrSnapshot())
	}

	// Readiness probe: GET /healthz until it answers, tolerating a brief
	// connect race but not the child exiting.
	client := &http.Client{Timeout: requestTimeout}
	url := "http://" + tg.addr + "/healthz"
	for {
		select {
		case werr := <-tg.waitResult:
			t.Fatalf("bench server exited during readiness probe: %v\n--- stderr ---\n%s", werr, tg.stderrSnapshot())
		default:
		}
		resp, perr := client.Get(url)
		if perr == nil {
			resp.Body.Close()
			break
		}
		select {
		case werr := <-tg.waitResult:
			t.Fatalf("bench server exited during readiness probe: %v\n--- stderr ---\n%s", werr, tg.stderrSnapshot())
		case <-deadline.C:
			tg.kill()
			t.Fatalf("bench server timed out on /healthz probe\n--- stderr ---\n%s", tg.stderrSnapshot())
		case <-time.After(25 * time.Millisecond):
		}
	}
	return tg
}

func (tg *benchTarget) stderrSnapshot() string {
	tg.stderrMu.Lock()
	defer tg.stderrMu.Unlock()
	return tg.stderr.String()
}

// stop shuts the server down gracefully (SIGINT -> SIGTERM -> Kill), reading the
// single waitResult the watcher goroutine publishes.
func (tg *benchTarget) stop() {
	tg.stopOnce.Do(func() {
		if tg.cmd.Process == nil {
			return
		}
		select {
		case <-tg.waitResult:
			return
		default:
		}
		_ = tg.cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-tg.waitResult:
			return
		case <-time.After(shutdownTimeout):
		}
		_ = tg.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-tg.waitResult:
			return
		case <-time.After(shutdownTimeout):
		}
		tg.kill()
	})
}

func (tg *benchTarget) kill() {
	if tg.cmd.Process != nil {
		_ = tg.cmd.Process.Kill()
	}
	select {
	case <-tg.waitResult:
	case <-time.After(shutdownTimeout):
	}
}

// benchResult holds the aggregated outcome of the measured window.
type benchResult struct {
	hist        *histogram
	total       int64
	failed      int64
	reqBytes    int
	respBytes   int
	wallElapsed time.Duration
}

// runGenerator runs the open-loop constant-rate generator for the given window.
// It schedules request i at its intended send time (start + i/R), dispatches on
// a pool of C persistent keep-alive connections, and records latency from the
// INTENDED send time (coordinated-omission correction, design §6.2). When
// record is false (warm-up) samples are discarded. It returns the histogram and
// request/failure counts (nil histogram during warm-up).
func runGenerator(ctx context.Context, d *httpDriver, cfg benchConfig, window time.Duration, record bool) *benchResult {
	res := &benchResult{hist: newHistogram()}

	// The schedule has rate*window requests. A bounded worker set (one per
	// connection) pulls due requests from the schedule channel.
	type job struct {
		i        int64
		intended time.Time
	}
	jobs := make(chan job, cfg.conns*2)

	start := time.Now()
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Workers: C persistent keep-alive connections. Each worker issues GET
	// /bench for every due job and records the coordinated-omission-corrected
	// latency into the shared histogram.
	for w := 0; w < cfg.conns; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for jb := range jobs {
				reqCtx, cancel := context.WithTimeout(context.Background(), requestTimeout)
				r, err := d.get(reqCtx, cfg.path)
				cancel()
				done := time.Now()
				// Coordinated-omission correction: latency from the INTENDED
				// send time, not the actual dispatch time.
				latency := done.Sub(jb.intended)
				mu.Lock()
				if err != nil || r.status != http.StatusOK {
					res.failed++
					res.hist.RecordError()
				} else {
					res.hist.Record(int64(latency))
					if res.respBytes == 0 {
						res.respBytes = len(r.body)
					}
				}
				res.total++
				mu.Unlock()
			}
		}()
	}

	// Scheduler: emit jobs at their intended send time. A ticker-free sleep to
	// each intended time keeps the open-loop property: dispatch does not wait on
	// prior responses. Jobs due in the past (because the generator fell behind)
	// are emitted immediately, and their lateness shows up as latency.
	var i int64
	for {
		intended := intendedSendTime(start, i, cfg.rate)
		if intended.Sub(start) >= window {
			break
		}
		now := time.Now()
		if d := intended.Sub(now); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				return res
			}
		}
		select {
		case jobs <- job{i: i, intended: intended}:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return res
		}
		i++
	}
	close(jobs)
	wg.Wait()
	res.wallElapsed = time.Since(start)
	res.reqBytes = 0 // GET cfg.path has no request body
	if !record {
		return nil // warm-up samples are discarded
	}
	return res
}

// benchRefBody is the SINGLE source of truth for the /bench body across all
// three legs: TLang's verified serializer output (design §5.8), exactly 23
// bytes. The Node/Bun BENCH_BODY constants must equal this, and
// assertBenchParity checks it at runtime.
const benchRefBody = "{\"id\":1,\"name\":\"bench\"}"

// jsonRefBody is the byte-exact reference body for GET /json (716 bytes): the
// TLang ctx.json serializer output for the JsonResp built with 16 WorkItems.
// Node/Bun must match these exact bytes.
const jsonRefBody = `{"id":7,"name":"payload","active":true,"count":16,"items":[{"id":0,"label":"item-0","score":0},{"id":1,"label":"item-1","score":100},{"id":2,"label":"item-2","score":200},{"id":3,"label":"item-3","score":300},{"id":4,"label":"item-4","score":400},{"id":5,"label":"item-5","score":500},{"id":6,"label":"item-6","score":600},{"id":7,"label":"item-7","score":700},{"id":8,"label":"item-8","score":800},{"id":9,"label":"item-9","score":900},{"id":10,"label":"item-10","score":1000},{"id":11,"label":"item-11","score":1100},{"id":12,"label":"item-12","score":1200},{"id":13,"label":"item-13","score":1300},{"id":14,"label":"item-14","score":1400},{"id":15,"label":"item-15","score":1500}],"tags":["alpha","beta","gamma"]}`

// workRefBody is the byte-exact reference body for GET /work (1407 bytes): the
// TLang ctx.json serializer output for the top-level array of 32 WorkItems.
const workRefBody = `[{"id":0,"label":"row-0-of-32","score":0},{"id":1,"label":"row-1-of-32","score":1},{"id":2,"label":"row-2-of-32","score":4},{"id":3,"label":"row-3-of-32","score":9},{"id":4,"label":"row-4-of-32","score":16},{"id":5,"label":"row-5-of-32","score":25},{"id":6,"label":"row-6-of-32","score":36},{"id":7,"label":"row-7-of-32","score":49},{"id":8,"label":"row-8-of-32","score":64},{"id":9,"label":"row-9-of-32","score":81},{"id":10,"label":"row-10-of-32","score":100},{"id":11,"label":"row-11-of-32","score":121},{"id":12,"label":"row-12-of-32","score":144},{"id":13,"label":"row-13-of-32","score":169},{"id":14,"label":"row-14-of-32","score":196},{"id":15,"label":"row-15-of-32","score":225},{"id":16,"label":"row-16-of-32","score":256},{"id":17,"label":"row-17-of-32","score":289},{"id":18,"label":"row-18-of-32","score":324},{"id":19,"label":"row-19-of-32","score":361},{"id":20,"label":"row-20-of-32","score":400},{"id":21,"label":"row-21-of-32","score":441},{"id":22,"label":"row-22-of-32","score":484},{"id":23,"label":"row-23-of-32","score":529},{"id":24,"label":"row-24-of-32","score":576},{"id":25,"label":"row-25-of-32","score":625},{"id":26,"label":"row-26-of-32","score":676},{"id":27,"label":"row-27-of-32","score":729},{"id":28,"label":"row-28-of-32","score":784},{"id":29,"label":"row-29-of-32","score":841},{"id":30,"label":"row-30-of-32","score":900},{"id":31,"label":"row-31-of-32","score":961}]`

// benchRouteBodies maps a benchmarkable route to its byte-exact reference body
// (the TLang ctx.json serializer output). "/bench" is the default baseline.
var benchRouteBodies = map[string]string{
	"/bench": benchRefBody,
	"/json":  jsonRefBody,
	"/work":  workRefBody,
}

// benchRouteOrder is the ordered route set TestBenchmarkCompare drives by
// default, baseline /bench first (design §4, plan item 6).
var benchRouteOrder = []string{"/bench", "/json", "/work"}

// resolveBenchRoute returns the reference body for a benchmarkable route and
// whether the route is known. It is a pure function so the error case is
// unit-testable without catching t.Fatalf.
func resolveBenchRoute(path string) (string, bool) {
	body, ok := benchRouteBodies[path]
	return body, ok
}

// knownRoutes returns the set of benchmarkable routes as a bool map, for the
// TLANG_BENCH_ROUTE_ORDER validation (design §C.4).
func knownRoutes() map[string]bool {
	set := make(map[string]bool, len(benchRouteBodies))
	for r := range benchRouteBodies {
		set[r] = true
	}
	return set
}

// legSpec is one comparative leg to run (consumed by TestBenchmarkCompare). It
// lives here (not in the _test.go file) so orderLegs can reference it under a
// plain `go build -tags e2e`.
type legSpec struct {
	name    string   // "tlang" | "node" | "bun"
	argv    []string // launch command; nil means skip
	version string   // runtime version string for the report/summary
}

// orderLegs reorders specs by the names in order; any leg present in specs but
// not named is appended in its original relative order (design §C.3). An empty
// order leaves specs unchanged.
func orderLegs(specs []legSpec, order []string) []legSpec {
	if len(order) == 0 {
		return specs
	}
	out := make([]legSpec, 0, len(specs))
	used := make(map[string]bool, len(specs))
	for _, name := range order {
		for _, s := range specs {
			if s.name == name && !used[s.name] {
				out = append(out, s)
				used[s.name] = true
			}
		}
	}
	for _, s := range specs {
		if !used[s.name] {
			out = append(out, s)
			used[s.name] = true
		}
	}
	return out
}

// orderRoutes reorders routes by the names in order; any route in routes but not
// named is appended in original order (design §C.4). Empty order == unchanged.
func orderRoutes(routes, order []string) []string {
	if len(order) == 0 {
		return routes
	}
	out := make([]string, 0, len(routes))
	used := make(map[string]bool, len(routes))
	for _, name := range order {
		for _, r := range routes {
			if r == name && !used[r] {
				out = append(out, r)
				used[r] = true
			}
		}
	}
	for _, r := range routes {
		if !used[r] {
			out = append(out, r)
			used[r] = true
		}
	}
	return out
}

// assertBenchParity is the single enforcement point for byte parity on the
// configured route (design §5.8). It issues one GET cfg.path via the leg's
// driver and fails the leg (t.Fatalf with got/want) unless the status is 200
// and the body equals cfg.refBody. Called once per leg per route immediately
// before that route's warm-up window.
func assertBenchParity(t *testing.T, d *httpDriver, legName string, cfg benchConfig) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	r, err := d.get(ctx, cfg.path)
	if err != nil {
		t.Fatalf("%s leg: %s parity probe failed: %v", legName, cfg.path, err)
	}
	if r.status != http.StatusOK || string(r.body) != cfg.refBody {
		t.Fatalf("%s leg: %s parity mismatch:\n  got  status=%d body=%q\n  want status=200 body=%q",
			legName, cfg.path, r.status, string(r.body), cfg.refBody)
	}
}

// reportLeg prints the §6.4/§5.7 reproducibility block for one leg in the
// existing t.Logf style. It emits the identical field set TestBenchmark's former
// inline block emitted, parameterized by leg so the TLang leg keeps the literal
// header "=== TLang benchmark report ===" and Node/Bun get their own header
// (so TLang log-scraping is undisturbed). Called once per leg by both tests.
func reportLeg(t *testing.T, tg *benchTarget, cfg benchConfig, res *benchResult,
	gateMode string, pinned bool, hasTaskset bool, serverSet, genSet map[int]bool, respBytes int) {
	t.Helper()

	p50 := time.Duration(res.hist.ValueAtQuantile(0.50))
	p90 := time.Duration(res.hist.ValueAtQuantile(0.90))
	p99 := time.Duration(res.hist.ValueAtQuantile(0.99))
	p999 := time.Duration(res.hist.ValueAtQuantile(0.999))
	maxL := time.Duration(res.hist.Max())

	t.Logf("=== %s benchmark report ===", reportTitle(tg.name))
	t.Logf("CPU: NumCPU=%d GOARCH=%s GOOS=%s model=%q", runtime.NumCPU(), runtime.GOARCH, runtime.GOOS, cpuModel())
	t.Logf("Gate mode: %s", gateMode)
	if pinned {
		t.Logf("Pinning: taskset applied; server cores=%s generator cores=%s (disjoint)", cfg.serverCores, coreSetString(genSet))
	} else {
		serverSetOK := len(serverSet) > 0
		genSetOK := len(genSet) > 0
		t.Logf("Pinning: unpinned: gate not enforced (taskset=%v serverCores=%q disjoint=%v)", hasTaskset, cfg.serverCores, serverSetOK && genSetOK && disjointNonEmpty(serverSet, genSet))
	}
	t.Logf("Server TLANG_THREADS=%s", tg.threads)
	t.Logf("Load: C(conns)=%d R(rate)=%d req/s", cfg.conns, cfg.rate)
	t.Logf("Payload %s: request=%d bytes response=%d bytes", cfg.path, res.reqBytes, respBytes)
	t.Logf("Requests: total=%d failed=%d", res.total, res.failed)
	t.Logf("Warm-up=%s Duration(measured)=%s wall=%s", cfg.warmup, cfg.duration, res.wallElapsed)
	t.Logf("Latency: p50=%.3fms p90=%.3fms p99=%.3fms p99.9=%.3fms Max=%.3fms",
		msf(p50), msf(p90), msf(p99), msf(p999), msf(maxL))
	t.Logf("Threshold: p99 <= 2.5 ms, failures == 0")
	if res.hist.Overflow() > 0 {
		t.Logf("Note: %d samples exceeded the histogram max (10s) and were clamped", res.hist.Overflow())
	}
}

// reportTitle maps a leg name to the report header runtime label.
func reportTitle(name string) string {
	switch name {
	case "tlang":
		return "TLang"
	case "node":
		return "Node"
	case "bun":
		return "Bun"
	default:
		return name
	}
}

// gateLeg applies the gate (design §5.3/§6.4): failures>0 is always fatal for
// every leg; p99 above the threshold is fatal only when p99Enforceable (the
// TLang leg under disjoint pinning), advisory otherwise.
func gateLeg(t *testing.T, name string, res *benchResult, p99 time.Duration, p99Enforceable bool) {
	t.Helper()
	// failures > 0 ALWAYS fails, pinned or not.
	if res.failed > 0 {
		t.Fatalf("%s gate FAILED: %d failed requests (want 0)", name, res.failed)
	}
	// p99 gate: enforced only for the TLang leg under disjoint pinning;
	// advisory otherwise.
	if p99 > p99Threshold {
		if p99Enforceable {
			t.Fatalf("%s gate FAILED (enforced): p99=%.3fms > 2.5ms", name, msf(p99))
		}
		t.Logf("%s WARNING: p99=%.3fms > 2.5ms, but gate is ADVISORY", name, msf(p99))
	}
}

// versionProbeTimeout bounds the `node --version` / `bun --version` probe.
const versionProbeTimeout = 5 * time.Second

// legBudget returns the per-leg context budget (design §5.7a). The TLang leg
// needs the clang-release build, so its budget is byte-for-byte the former
// TestBenchmark formula; the Node/Bun legs drop buildTimeout and add the small
// version-probe budget.
func legBudget(name string, cfg benchConfig) time.Duration {
	base := startTimeout + cfg.warmup + cfg.duration + shutdownTimeout + 2*requestTimeout
	if name == "tlang" {
		return buildTimeout + base
	}
	return base + versionProbeTimeout
}

// legBudgetRoutes is legBudget scaled for a leg that drives nRoutes routes under
// one per-leg context: the warm-up + measured windows (and their per-route
// request-timeout slack) run once per route, while startup/shutdown/build
// happen once. nRoutes >= 1. With nRoutes == 1 it equals legBudget.
func legBudgetRoutes(name string, cfg benchConfig, nRoutes int) time.Duration {
	if nRoutes < 1 {
		nRoutes = 1
	}
	perRoute := cfg.warmup + cfg.duration + 2*requestTimeout
	base := startTimeout + shutdownTimeout + time.Duration(nRoutes)*perRoute
	if name == "tlang" {
		return buildTimeout + base
	}
	return base + versionProbeTimeout
}

// ============================================================================
// D2 — CPU topology check (design §2)
// ============================================================================

// cpuCore identifies the physical core a logical CPU maps to.
type cpuCore struct {
	physID int
	coreID int
}

// parseCPUInfo parses the (processor, physical id, core id) triples from the
// text of /proc/cpuinfo into a map[logicalCPU]cpuCore. Pure helper so the
// parsing is unit-testable with a synthetic /proc/cpuinfo string.
func parseCPUInfo(data string) map[int]cpuCore {
	out := map[int]cpuCore{}
	cur := -1
	var phys, core int
	var havePhys, haveCore bool
	flush := func() {
		if cur >= 0 {
			c := cpuCore{coreID: cur} // fallback: unique per logical CPU when phys/core absent
			if havePhys {
				c.physID = phys
			}
			if haveCore {
				c.coreID = core
			}
			out[cur] = c
		}
		phys, core, havePhys, haveCore = 0, 0, false, false
	}
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			if strings.TrimSpace(line) == "" {
				flush()
				cur = -1
			}
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "processor":
			flush()
			if n, err := strconv.Atoi(val); err == nil {
				cur = n
			} else {
				cur = -1
			}
		case "physical id":
			if n, err := strconv.Atoi(val); err == nil {
				phys, havePhys = n, true
			}
		case "core id":
			if n, err := strconv.Atoi(val); err == nil {
				core, haveCore = n, true
			}
		}
	}
	flush()
	return out
}

// cpuTopology reads /proc/cpuinfo and returns the logical-CPU -> physical-core
// map. Linux-only; returns (nil, err) off Linux or when /proc is unreadable.
func cpuTopology() (map[int]cpuCore, error) {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return nil, err
	}
	return parseCPUInfo(string(data)), nil
}

// topologyResult records the outcome of the four-distinct-physical-cores /
// disjointness check (design §2).
type topologyResult struct {
	status   string // "disjoint" | "shared" | "unknown"
	mapping  string // human-readable observed mapping for the report
	distinct bool   // server set lands on N distinct physical cores
	disjoint bool   // server and generator sets share no physical core
}

// coreSetSorted returns the sorted members of a core set.
func coreSetSorted(set map[int]bool) []int {
	cores := make([]int, 0, len(set))
	for c := range set {
		cores = append(cores, c)
	}
	sort.Ints(cores)
	return cores
}

// checkCPUTopology maps the server and generator logical-CPU sets to physical
// cores via topo and verifies the server set lands on distinct physical cores
// and is disjoint from the generator set (design §2). It is a pure function
// (topo injected) so it is unit-testable with a synthetic topology.
func checkCPUTopology(topo map[int]cpuCore, serverSet, genSet map[int]bool) topologyResult {
	var b strings.Builder
	serverCores := map[cpuCore]int{} // physical core -> count of server lanes on it
	fmt.Fprintf(&b, "server[")
	for i, cpu := range coreSetSorted(serverSet) {
		cc, ok := topo[cpu]
		if i > 0 {
			b.WriteString(" ")
		}
		if !ok {
			fmt.Fprintf(&b, "cpu%d=?", cpu)
			continue
		}
		fmt.Fprintf(&b, "cpu%d=(p%d,c%d)", cpu, cc.physID, cc.coreID)
		serverCores[cc]++
	}
	b.WriteString("] gen[")
	genCores := map[cpuCore]bool{}
	for i, cpu := range coreSetSorted(genSet) {
		cc, ok := topo[cpu]
		if i > 0 {
			b.WriteString(" ")
		}
		if !ok {
			fmt.Fprintf(&b, "cpu%d=?", cpu)
			continue
		}
		fmt.Fprintf(&b, "cpu%d=(p%d,c%d)", cpu, cc.physID, cc.coreID)
		genCores[cc] = true
	}
	b.WriteString("]")

	distinct := len(serverCores) == len(serverSet) && len(serverSet) > 0
	for cc := range serverCores {
		if serverCores[cc] > 1 {
			distinct = false
		}
	}
	disjoint := len(serverCores) > 0 && len(genCores) > 0
	for cc := range serverCores {
		if genCores[cc] {
			disjoint = false
		}
	}
	res := topologyResult{mapping: b.String(), distinct: distinct, disjoint: disjoint}
	if distinct && disjoint {
		res.status = "disjoint"
	} else {
		res.status = "shared"
	}
	return res
}

// assertCPUTopology runs the §2 run-time check. On a clean split it records
// "disjoint" and logs the mapping. On a shared/over-subscribed split it fails
// loudly unless cfg.allowSharedCore is set, which downgrades to a warning and
// marks the run "shared". Returns the topologyResult for the summary. It is a
// no-op returning "unknown" when the topology cannot be read (non-Linux host).
func assertCPUTopology(t *testing.T, cfg benchConfig, serverSet, genSet map[int]bool) topologyResult {
	t.Helper()
	topo, err := cpuTopology()
	if err != nil {
		t.Logf("cpu topology: /proc/cpuinfo unavailable (%v); topology check skipped", err)
		return topologyResult{status: "unknown"}
	}
	res := checkCPUTopology(topo, serverSet, genSet)
	if res.status == "disjoint" {
		t.Logf("cpu topology: disjoint — %s", res.mapping)
		return res
	}
	msg := fmt.Sprintf("server/generator CPU sets share physical core(s) or server over-subscribes a core: %s; "+
		"adjust TLANG_BENCH_SERVER_CORES / BENCH_GEN_CORES to match the observed topology, "+
		"or set TLANG_BENCH_ALLOW_SHARED_CORES=1 to measure anyway (advisory)", res.mapping)
	if cfg.allowSharedCore {
		t.Logf("cpu topology WARNING (advisory, TLANG_BENCH_ALLOW_SHARED_CORES=1): %s", msg)
		return res
	}
	t.Fatalf("cpu topology FAILED: %s", msg)
	return res
}

// ============================================================================
// D3/D5 — header capture, readiness cross-check, serving-unit mapping
// ============================================================================

// headerByteCount computes the estimated on-wire header byte count: the status
// line + sum over headers of len(key)+len(": ")+len(value)+len("\r\n") + the
// final "\r\n". Pure arithmetic helper, unit-testable with a synthetic header.
func headerByteCount(status int, h http.Header) int {
	// Status line, e.g. "HTTP/1.1 200 OK\r\n". Use the stdlib status text.
	statusLine := fmt.Sprintf("HTTP/1.1 %d %s\r\n", status, http.StatusText(status))
	total := len(statusLine)
	for key, vals := range h {
		for _, v := range vals {
			total += len(key) + len(": ") + len(v) + len("\r\n")
		}
	}
	total += len("\r\n") // final blank line terminating the header block
	return total
}

// captureHeaders issues one GET route via d and returns the response header set
// and the estimated on-wire header byte count (design §3). Diagnostic only; the
// caller logs a warning and continues on error (never fatal).
func captureHeaders(ctx context.Context, d *httpDriver, route string) (http.Header, int, error) {
	r, err := d.get(ctx, route)
	if err != nil {
		return nil, 0, err
	}
	return r.header, headerByteCount(r.status, r.header), nil
}

// parseReadinessThreads extracts N from a readiness line of the form
// "...listening on http://HOST:PORT (N threads)". Returns ok=false when the
// line does not match. Cross-check only (design §5): for Node/Bun N is a pure
// echo of TLANG_THREADS, for TLang a weak liveness signal.
func parseReadinessThreads(line string) (int, bool) {
	_, _, ok := parseListening(line)
	if !ok {
		return 0, false
	}
	open := strings.LastIndex(line, "(")
	if open < 0 {
		return 0, false
	}
	rest := line[open+1:]
	fields := strings.Fields(rest)
	if len(fields) < 1 {
		return 0, false
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false
	}
	return n, true
}

// servingUnits maps a leg's raw tree process count to its serving-unit count
// (design §B.4/§5): TLang 1 process (threads reported separately), Node
// tree_procs-1 (supervisor does not serve), Bun tree_procs (primary serves).
func servingUnits(leg string, treeProcs int) int {
	switch leg {
	case "node":
		if treeProcs <= 0 {
			return 0
		}
		return treeProcs - 1
	case "bun":
		return treeProcs
	case "tlang":
		return 1 // one process; concurrency is threads, reported separately
	default:
		return treeProcs
	}
}

// procThreads reads the Threads: count from /proc/<pid>/status (TLang's serving
// concurrency, design §B.4). Returns 0 off Linux or on any read error.
func procThreads(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "Threads:"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
	}
	return 0
}

// ============================================================================
// Mechanism A — rate ladder / sustained-throughput-under-SLO (design §A)
// ============================================================================

// rateLadder parses TLANG_BENCH_RATE_LADDER into the ascending ladder, defaulting
// to the single fixed rate [cfg.rate] when the knob is unset. Validation (ints,
// >0, strictly ascending) already happened in parseBenchConfig; this returns the
// stored ladder, re-validating defensively so the helper is self-contained and
// unit-testable (design §A.2).
func rateLadder(t *testing.T, cfg benchConfig) []int {
	t.Helper()
	if len(cfg.rateLadderSet) == 0 {
		return []int{cfg.rate}
	}
	// Defensive re-check: must be ascending and positive.
	prev := 0
	for i, v := range cfg.rateLadderSet {
		if v <= 0 {
			t.Fatalf("rate ladder: value %d must be positive", v)
		}
		if i > 0 && v <= prev {
			t.Fatalf("rate ladder: %d not strictly greater than %d", v, prev)
		}
		prev = v
	}
	return cfg.rateLadderSet
}

// sweepStep is one (rate -> measured result) outcome for one leg+route rung
// (design §A.2).
type sweepStep struct {
	rate     int
	res      *benchResult
	p50, p99 time.Duration
	maxL     time.Duration
	conns    int
	genBound bool
	holdsSLO []bool // one per SLO threshold in cfg.sweepSLOs
}

// sweepConnsFor returns the connection-pool size for a rung at the given rate
// (design §A.2 Finding 1): a fixed value when cfg.sweepConns>0, else scaled as
// max(50, rate/400).
func sweepConnsFor(cfg benchConfig, rate int) int {
	if cfg.sweepConns > 0 {
		return cfg.sweepConns
	}
	c := rate / 400
	if c < 50 {
		c = 50
	}
	return c
}

// runRateSweep drives the ascending ladder against an already-started leg on one
// route (design §A.2). It calls assertBenchParity once for (leg,route) BEFORE the
// first rung, rebuilds the driver when the per-rung connection count changes, runs
// a discarded warm-up then a measured window per rung, flags gen-bound rungs, and
// STOPS ascending at the first rung with failed>0. It NEVER calls t.Fatalf except
// via the parity gate (pure measurement+report otherwise).
func runRateSweep(ctx context.Context, t *testing.T, d *httpDriver, baseCfg benchConfig,
	legName, route, refBody string, ladder []int) []sweepStep {
	t.Helper()

	gateCfg := baseCfg
	gateCfg.path = route
	gateCfg.refBody = refBody
	// Independent per-leg byte gate for the ladder pass (fatal on divergence).
	assertBenchParity(t, d, legName, gateCfg)

	steps := make([]sweepStep, 0, len(ladder))
	prevConns := -1
	for _, rate := range ladder {
		conns := sweepConnsFor(baseCfg, rate)
		if conns != prevConns {
			d = newHTTPDriver(&serverHandle{addr: strings.TrimPrefix(d.base, "http://")}, conns)
			prevConns = conns
		}
		rungCfg := baseCfg
		rungCfg.path = route
		rungCfg.refBody = refBody
		rungCfg.rate = rate
		rungCfg.conns = conns

		// Discarded warm-up, then the measured window.
		_ = runGenerator(ctx, d, rungCfg, baseCfg.sweepWarmup, false)
		res := runGenerator(ctx, d, rungCfg, baseCfg.sweepDuration, true)
		if res == nil {
			t.Logf("sweep %s %s @ %d req/s: measured window produced no result; stopping", legName, route, rate)
			break
		}

		p50 := time.Duration(res.hist.ValueAtQuantile(0.50))
		p99 := time.Duration(res.hist.ValueAtQuantile(0.99))
		maxL := time.Duration(res.hist.Max())

		genBound := false
		if res.wallElapsed > 0 {
			achieved := float64(res.total) / res.wallElapsed.Seconds()
			if achieved < 0.95*float64(rate) {
				genBound = true
			}
		}

		holds := make([]bool, len(baseCfg.sweepSLOs))
		for i, slo := range baseCfg.sweepSLOs {
			holds[i] = res.failed == 0 && p99 <= slo
		}

		steps = append(steps, sweepStep{
			rate: rate, res: res, p50: p50, p99: p99, maxL: maxL,
			conns: conns, genBound: genBound, holdsSLO: holds,
		})
		t.Logf("sweep %s %s @ %d req/s (C=%d): p50=%.3fms p99=%.3fms max=%.3fms failed=%d gen_bound=%v",
			legName, route, rate, conns, msf(p50), msf(p99), msf(maxL), res.failed, genBound)

		if res.failed > 0 {
			t.Logf("sweep %s %s: failed>0 at %d req/s — ceiling exceeded, stopping ascent", legName, route, rate)
			break
		}
	}
	return steps
}

// ============================================================================
// Mechanism B — /proc RSS+CPU process-tree sampler (design §B)
// ============================================================================

// procSample is one point-in-time reading of a process subtree (design §B.3).
type procSample struct {
	rssKB     int64
	cpuTicks  int64
	procCount int
}

// splitStat splits a /proc/<pid>/stat line on the LAST ')', returning the
// whitespace-separated tokens AFTER it. Per design the comm field (field 2) can
// contain spaces and parens, so splitting on the last ')' makes the remaining
// tokens stable: tokens[0]=state, tokens[1]=ppid, ..., tokens[11]=utime,
// tokens[12]=stime. Returns ok=false when there is no ')'.
func splitStat(line string) ([]string, bool) {
	idx := strings.LastIndexByte(line, ')')
	if idx < 0 {
		return nil, false
	}
	return strings.Fields(line[idx+1:]), true
}

// readStatPPID reads /proc/<pid>/stat and returns its parent PID.
func readStatPPID(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	toks, ok := splitStat(string(data))
	if !ok || len(toks) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(toks[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

// enumerateProcPIDs lists the numeric PIDs under /proc. Linux-only.
func enumerateProcPIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// treePIDs returns rootPID plus every transitive descendant via a PPID scan of
// /proc (design §B.3, the load-bearing path on every Linux). Vanished PIDs are
// skipped. The /proc/<pid>/task/<tid>/children fast path is intentionally not
// relied upon.
func treePIDs(rootPID int) []int {
	pids, err := enumerateProcPIDs()
	if err != nil {
		return []int{rootPID}
	}
	children := map[int][]int{}
	for _, pid := range pids {
		ppid, ok := readStatPPID(pid)
		if !ok {
			continue // vanished mid-walk; skip
		}
		children[ppid] = append(children[ppid], pid)
	}
	// BFS transitive closure from rootPID.
	seen := map[int]bool{rootPID: true}
	queue := []int{rootPID}
	out := []int{rootPID}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
				queue = append(queue, c)
			}
		}
	}
	return out
}

// readStatCPUTicks reads (utime+stime) in clock ticks for one PID.
func readStatCPUTicks(pid int) (int64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	toks, ok := splitStat(string(data))
	if !ok || len(toks) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseInt(toks[11], 10, 64)
	stime, err2 := strconv.ParseInt(toks[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return utime + stime, true
}

// readStatusRSSKB reads VmRSS (in kB) from /proc/<pid>/status.
func readStatusRSSKB(pid int) (int64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			fields := strings.Fields(v)
			if len(fields) >= 1 {
				if n, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
					return n, true
				}
			}
		}
	}
	return 0, false
}

// sampleProcTree reads VmRSS and (utime+stime) for every PID in the tree rooted
// at rootPID and sums them (design §B.3). Vanished PIDs are skipped. Returns an
// error only when the root tree cannot be enumerated at all.
func sampleProcTree(rootPID int) (procSample, error) {
	if _, err := os.Stat("/proc"); err != nil {
		return procSample{}, err
	}
	pids := treePIDs(rootPID)
	var s procSample
	for _, pid := range pids {
		if rss, ok := readStatusRSSKB(pid); ok {
			s.rssKB += rss
			s.procCount++ // count a process only if we read it (alive)
		}
		if ticks, ok := readStatCPUTicks(pid); ok {
			s.cpuTicks += ticks
		}
	}
	if s.procCount == 0 {
		return procSample{}, fmt.Errorf("no readable PIDs in tree rooted at %d", rootPID)
	}
	return s, nil
}

// resourceStats is the aggregate of a measured window's samples (design §B.3).
type resourceStats struct {
	peakRSSKB  int64
	startTicks int64
	endTicks   int64
	startWall  time.Time
	endWall    time.Time
	samples    int
	treeProcs  int // process count at the last sample
	available  bool
}

// cpuSeconds returns the window CPU-seconds given a validated clockHz, or 0 when
// unavailable (clockHz<=0 or no samples).
func (r resourceStats) cpuSeconds(clockHz int) float64 {
	if !r.available || clockHz <= 0 {
		return 0
	}
	return float64(r.endTicks-r.startTicks) / float64(clockHz)
}

// resourceMonitor samples the tree on a ticker during a measured window.
type resourceMonitor struct {
	rootPID  int
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}

	mu    sync.Mutex
	stats resourceStats
}

// startResourceMonitor begins sampling the tree rooted at rootPID every interval
// (design §B.3). Sampling runs until stop() is called or ctx is cancelled.
func startResourceMonitor(ctx context.Context, rootPID int, interval time.Duration) *resourceMonitor {
	if interval <= 0 {
		interval = defaultResourceInterval
	}
	mctx, cancel := context.WithCancel(ctx)
	m := &resourceMonitor{rootPID: rootPID, interval: interval, cancel: cancel, done: make(chan struct{})}

	// Take an initial sample synchronously so startTicks is set even for a very
	// short window.
	if s, err := sampleProcTree(rootPID); err == nil {
		m.stats.available = true
		m.stats.peakRSSKB = s.rssKB
		m.stats.startTicks = s.cpuTicks
		m.stats.endTicks = s.cpuTicks
		m.stats.startWall = time.Now()
		m.stats.endWall = m.stats.startWall
		m.stats.samples = 1
		m.stats.treeProcs = s.procCount
	}

	go func() {
		defer close(m.done)
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			select {
			case <-mctx.Done():
				return
			case <-tk.C:
				s, err := sampleProcTree(rootPID)
				if err != nil {
					continue
				}
				m.mu.Lock()
				if !m.stats.available {
					m.stats.available = true
					m.stats.startTicks = s.cpuTicks
					m.stats.startWall = time.Now()
				}
				if s.rssKB > m.stats.peakRSSKB {
					m.stats.peakRSSKB = s.rssKB
				}
				m.stats.endTicks = s.cpuTicks
				m.stats.endWall = time.Now()
				m.stats.treeProcs = s.procCount
				m.stats.samples++
				m.mu.Unlock()
			}
		}
	}()
	return m
}

// stop ends sampling and returns the aggregated window stats.
func (m *resourceMonitor) stop() resourceStats {
	m.cancel()
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

// clockHzResult records the §B.3 USER_HZ determination for the report.
type clockHzResult struct {
	hz        int
	available bool   // CPU-s metrics usable (hz passed the sanity-bound/calibration)
	probe     string // outcome of the getconf probe
	sanity    string // sanity-bound outcome
	calib     string // calibration outcome ("" when not run)
}

// probeClockHz runs `getconf CLK_TCK` and returns the parsed candidate.
func probeClockHz(ctx context.Context) (int, string) {
	out, err := exec.CommandContext(ctx, "getconf", "CLK_TCK").Output()
	if err != nil {
		return 100, fmt.Sprintf("getconf CLK_TCK unavailable (%v); assuming 100", err)
	}
	v, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil {
		return 100, fmt.Sprintf("getconf CLK_TCK unparsable (%q); assuming 100", strings.TrimSpace(string(out)))
	}
	return v, fmt.Sprintf("getconf CLK_TCK=%d", v)
}

// calibrateClockHz spins one locked busy thread for ~500ms with GOMAXPROCS(1)
// and GC disabled, computing observedHz from whole-process /proc/self/stat ticks
// over the wall window (design §B.3). Returns (observedHz, ok). Linux-only.
func calibrateClockHz() (float64, bool) {
	self := os.Getpid()
	prevGO := runtime.GOMAXPROCS(1)
	prevGC := debug.SetGCPercent(-1)
	defer func() {
		runtime.GOMAXPROCS(prevGO)
		debug.SetGCPercent(prevGC)
	}()

	before, ok := readStatCPUTicks(self)
	if !ok {
		return 0, false
	}
	wallStart := time.Now()

	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		var x uint64
		for time.Since(wallStart) < 500*time.Millisecond {
			for i := 0; i < 1_000_000; i++ {
				x += uint64(i)
			}
		}
		_ = x
		close(done)
	}()
	<-done

	wall := time.Since(wallStart).Seconds()
	after, ok := readStatCPUTicks(self)
	if !ok || wall <= 0 {
		return 0, false
	}
	return float64(after-before) / wall, true
}

// determineClockHz runs the §B.3 probe + sanity-bound + optional calibration and
// returns the result. CPU-s is marked unavailable when the candidate is outside
// {100,250,300,1000} or (if calibrated) disagrees by more than ±20%.
func determineClockHz(t *testing.T, calibrate bool) clockHzResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
	defer cancel()
	cand, probe := probeClockHz(ctx)
	t.Logf("clockHz probe: %s", probe)

	res := clockHzResult{hz: cand, probe: probe}
	if !knownClockHz[cand] {
		res.available = false
		res.sanity = fmt.Sprintf("candidate %d not in {100,250,300,1000}; CPU-s marked unavailable", cand)
		t.Logf("clockHz sanity: %s", res.sanity)
		return res
	}
	res.available = true
	res.sanity = fmt.Sprintf("candidate %d within known kernel CONFIG_HZ set", cand)
	t.Logf("clockHz sanity: %s", res.sanity)

	if calibrate {
		observed, ok := calibrateClockHz()
		if !ok {
			res.calib = "calibration could not read /proc/self/stat; keeping sanity-bounded candidate"
			t.Logf("clockHz calibration: %s", res.calib)
			return res
		}
		lo := 0.8 * float64(cand)
		hi := 1.2 * float64(cand)
		if observed < lo || observed > hi {
			res.available = false
			res.calib = fmt.Sprintf("observed %.1f Hz outside ±20%% of %d; CPU-s marked unavailable", observed, cand)
		} else {
			res.calib = fmt.Sprintf("observed %.1f Hz within ±20%% of %d", observed, cand)
		}
		t.Logf("clockHz calibration: %s", res.calib)
	}
	return res
}

// ============================================================================
// C.5 — JSONL summary writer (design §C.5)
// ============================================================================

// sweepStepJSON is the per-rung object nested in a summary line's sweep[].
type sweepStepJSON struct {
	Rate     int     `json:"rate"`
	P99Ms    float64 `json:"p99_ms"`
	Failed   int64   `json:"failed"`
	HoldsSLO []bool  `json:"holds_slo"`
	GenBound bool    `json:"gen_bound"`
}

// summaryLine is one JSONL object per (leg,route) measurement (design §C.5). The
// resource fields are pointers so they are OMITTED (null) on "ladder" lines where
// no resource sample was taken.
type summaryLine struct {
	Run       int     `json:"run"`
	Pass      string  `json:"pass"` // "fixed" | "ladder"
	LegOrder  string  `json:"leg_order"`
	Route     string  `json:"route"`
	Leg       string  `json:"leg"`
	Version   string  `json:"version"`
	Rate      int     `json:"rate"`
	P50Ms     float64 `json:"p50_ms"`
	P90Ms     float64 `json:"p90_ms"`
	P99Ms     float64 `json:"p99_ms"`
	P999Ms    float64 `json:"p999_ms"`
	MaxMs     float64 `json:"max_ms"`
	Failed    int64   `json:"failed"`
	Total     int64   `json:"total"`
	RespBytes int     `json:"resp_bytes"`

	// Resource fields — present only on "fixed" lines when the sampler ran.
	ResourceRate *int     `json:"resource_rate,omitempty"`
	PeakRSSMiB   *float64 `json:"peak_rss_mib,omitempty"`
	CPUSeconds   *float64 `json:"cpu_s,omitempty"`
	CPUSPerMreq  *float64 `json:"cpu_s_per_mreq,omitempty"`
	TreeProcs    *int     `json:"tree_procs,omitempty"`
	ServingUnits *int     `json:"serving_units,omitempty"`
	Threads      *int     `json:"threads,omitempty"`

	CPUTopology   string          `json:"cpu_topology"`
	Perreq        int             `json:"perreq"`
	ClockHz       int             `json:"clock_hz"`
	CPUSAvailable bool            `json:"cpu_s_available"`
	SweepSLOMs    []float64       `json:"sweep_slo_ms"`
	Sweep         []sweepStepJSON `json:"sweep,omitempty"`
}

// summaryWriter appends summaryLine objects as JSONL to a file (design §C.5),
// create-if-absent, append-mode. Writes are serialized under a mutex. A write
// error is non-fatal (logged by the caller).
type summaryWriter struct {
	mu   sync.Mutex
	path string
}

// newSummaryWriter returns a writer for path, or nil when path is empty (so the
// default run writes nothing).
func newSummaryWriter(path string) *summaryWriter {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return &summaryWriter{path: path}
}

// write marshals one line and appends it + "\n". Safe to call concurrently.
func (w *summaryWriter) write(line summaryLine) error {
	if w == nil {
		return nil
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if dir := filepath.Dir(w.path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// sweepSLOMsList converts the SLO durations to a []float64 of milliseconds for
// the summary line.
func sweepSLOMsList(slos []time.Duration) []float64 {
	out := make([]float64, len(slos))
	for i, s := range slos {
		out[i] = msf(s)
	}
	return out
}

// sweepStepsJSON converts measured sweep steps to their JSONL representation.
func sweepStepsJSON(steps []sweepStep) []sweepStepJSON {
	if len(steps) == 0 {
		return nil
	}
	out := make([]sweepStepJSON, 0, len(steps))
	for _, s := range steps {
		var failed int64
		if s.res != nil {
			failed = s.res.failed
		}
		out = append(out, sweepStepJSON{
			Rate: s.rate, P99Ms: msf(s.p99), Failed: failed,
			HoldsSLO: s.holdsSLO, GenBound: s.genBound,
		})
	}
	return out
}
