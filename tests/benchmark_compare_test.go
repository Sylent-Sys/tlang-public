//go:build e2e

// Comparative benchmark suite (tests-suites design §5.7). TestBenchmarkCompare
// drives the ONE shared load generator (bench_shared.go) against up to three
// runtime legs — TLang (always), Node.js, and Bun — so all three are measured
// by the identical open-loop generator, HDR histogram, warm-up/measured split,
// keep-alive pool, pinning, and gate. The only thing that differs per leg is the
// launched process. It is behind //go:build e2e and additionally opt-in via
// TLANG_BENCH_COMPARE=1 so neither the default `go test` nor the existing
// TLANG_BENCH=1 run pays its cost.
//
// Fairness contract (design NFR-1): identical /bench response bytes (enforced by
// assertBenchParity before each leg's warm-up), one generator, the same
// pinning/gate machinery. The p99 SLO gate is enforced only for the TLang leg
// under disjoint CPU pinning; for Node/Bun p99 is advisory. A failed request
// fails the whole run for every leg. Absent runtimes (node/bun not on PATH)
// self-skip with a log line and the run continues.

package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legSummary is one row of the final side-by-side comparison table (FR-5.2).
type legSummary struct {
	name    string
	version string
	route   string
	p50     time.Duration
	p99     time.Duration
	max     time.Duration
	failed  int64
}

// TestBenchmarkCompare runs the shared generator against the TLang, Node, and
// Bun legs (design §5.7). Opt-in via TLANG_BENCH_COMPARE=1.
func TestBenchmarkCompare(t *testing.T) {
	if os.Getenv("TLANG_BENCH_COMPARE") != "1" {
		t.Skip("comparative benchmark opt-in: set TLANG_BENCH_COMPARE=1")
	}

	cfg := parseBenchConfig(t)

	// Pinning + threads via the SHARED helpers (design §5.6), the SAME code
	// path TestBenchmark uses — no re-derivation, so N and the gate mode cannot
	// drift between legs (finding 5).
	pinned, serverSet, genSet, hasTaskset := benchPinning(cfg)
	threads := benchThreads(pinned, serverSet)
	gateMode := "advisory"
	if pinned {
		gateMode = "enforced (TLang only)"
		if cfg.p99Advisory {
			gateMode = "advisory (TLANG_BENCH_P99_ADVISORY=1; failed==0 still enforced, TLang p99 downgraded to warning)"
		}
	}

	// Build the leg list. TLang always runs: build echo_server.tl on the
	// clang-release leg under its own build budget. Node/Bun run only when the
	// binary is on PATH, else they self-skip (design §5.4).
	var specs []legSpec

	// TLang leg: build the fixture (clang-release). The build failing is a real
	// error, unlike an absent node/bun. The version field records the fixture +
	// leg label (there is no `tlang --version` for the fixture binary).
	buildCtx, buildCancel := context.WithTimeout(context.Background(), buildTimeout)
	src := e2eFixturePath(t, echoServerFixture)
	bin, err := buildFixture(t, buildCtx, legs[0], src, false) // legs[0] == clang-release
	buildCancel()
	if err != nil {
		t.Fatalf("build %s (clang-release): %v", echoServerFixture, err)
	}
	specs = append(specs, legSpec{
		name:    "tlang",
		argv:    []string{bin},
		version: "clang-release " + echoServerFixture,
	})

	// Node leg (if node on PATH).
	if nodePath, lerr := exec.LookPath("node"); lerr != nil {
		t.Logf("skipping node leg: %q not found on PATH", "node")
	} else {
		script, aerr := filepath.Abs(filepath.Join("bench", "node", "server.js"))
		if aerr != nil {
			t.Fatalf("resolve node server.js path: %v", aerr)
		}
		specs = append(specs, legSpec{name: "node", argv: []string{nodePath, script}})
	}

	// Bun leg (if bun on PATH).
	if bunPath, lerr := exec.LookPath("bun"); lerr != nil {
		t.Logf("skipping bun leg: %q not found on PATH", "bun")
	} else {
		script, aerr := filepath.Abs(filepath.Join("bench", "bun", "server.ts"))
		if aerr != nil {
			t.Fatalf("resolve bun server.ts path: %v", aerr)
		}
		specs = append(specs, legSpec{name: "bun", argv: []string{bunPath, script}})
	}

	// Leg-order rotation (design §C.3): reorder specs by TLANG_BENCH_LEG_ORDER
	// when set (default = today's order). The TLang build already happened above
	// regardless of position, so rotating TLang out of position 1 is free.
	specs = orderLegs(specs, cfg.legOrder)

	// Route set: the full ordered list (/bench, /json, /work) by default, or a
	// single route when TLANG_BENCH_PATH was set (cfg.path non-empty). /bench
	// is first as the baseline (plan item 6).
	routes := benchRouteOrder
	if cfg.path != "" {
		routes = []string{cfg.path}
	}
	// Route-order rotation (design §C.4): reorder by TLANG_BENCH_ROUTE_ORDER
	// when set. Skipped for a single-route run (nothing to reorder).
	if cfg.path == "" {
		routes = orderRoutes(routes, cfg.routeOrder)
	}

	// newKnobsActive is true when any FEAT-002 re-measurement knob is set. The
	// new behavior (topology assertion, resource sampler, rate ladder, JSONL
	// summary) is gated on it so a no-new-knob fixed-rate run stays byte-
	// identical to before (FEAT-002 acceptance criterion 3).
	newKnobsActive := cfg.resource || cfg.rateLadderGiven || cfg.summaryOut != "" ||
		len(cfg.legOrder) > 0 || len(cfg.routeOrder) > 0 || cfg.perreq || cfg.allowSharedCore

	// D2 topology check (design §2): verify the server set lands on distinct
	// physical cores and is disjoint from the generator set. Only meaningful
	// when pinned with both sets known; gated behind the new knobs so the plain
	// fixed-rate run's gate behavior is unchanged.
	topoRes := topologyResult{status: "unknown"}
	if pinned && newKnobsActive {
		topoRes = assertCPUTopology(t, cfg, serverSet, genSet)
	}

	// clockHz for the CPU-s divisor (design §B.3): probe + sanity-bound +
	// optional calibration. Only needed when the resource sampler runs.
	clockRes := clockHzResult{hz: 100, available: false}
	if cfg.resource {
		clockRes = determineClockHz(t, cfg.clockHzCalib)
	}

	// Run bookkeeping for the JSONL summary (design §C.5).
	runNo := envNonNegInt(t, "TLANG_BENCH_RUN", 1)
	legOrderStr := legOrderString(specs)
	summary := newSummaryWriter(cfg.summaryOut)

	// Rate ladder (design §A). When the knob is set (len>1 or explicitly given)
	// the ladder pass runs in addition to the fixed-rate pass.
	ladder := rateLadder(t, cfg)
	runLadder := cfg.rateLadderGiven && len(ladder) >= 1

	// legBudget must cover warm-up + measured for EVERY route in the set, since
	// all routes run under one per-leg context. Scale the per-route base window
	// contribution by the number of routes.
	nRoutes := len(routes)

	var summaryRows []legSummary
	for _, leg := range specs {
		// Fresh per-leg ctx (its own cancel) so a long comparative run is never
		// cancelled mid-measurement on leg 2/3 (design §5.7a). The budget covers
		// all routes for this leg.
		ctx, cancel := context.WithTimeout(context.Background(), legBudgetRoutes(leg.name, cfg, nRoutes))

		// Version capture for the non-TLang legs (design §5.7a); record
		// "unknown" on any error.
		version := leg.version
		if leg.name != "tlang" {
			version = probeVersion(ctx, leg.argv[0])
		}

		// One server + driver per leg, reused across every route (do not restart
		// the server per route).
		tg := startBenchTarget(t, ctx, leg.name, leg.argv, cfg.serverCores, threads, pinned)
		tg.version = version

		d := newHTTPDriver(&serverHandle{addr: tg.addr}, cfg.conns)

		for _, route := range routes {
			body, ok := resolveBenchRoute(route)
			if !ok {
				tg.stop()
				cancel()
				t.Fatalf("internal: route %q has no reference body", route)
			}
			routeCfg := cfg
			routeCfg.path = route
			routeCfg.refBody = body

			// Parity gate (design §5.8): a REAL GET <route> call before this
			// route's warm-up, the single enforcement point for byte parity.
			assertBenchParity(t, d, leg.name, routeCfg)

			// Header diagnostic (design §3) + readiness (N threads) cross-check
			// (design §5), gated behind the new knobs so a plain fixed-rate run
			// emits the same log/report output and issues no extra requests.
			if newKnobsActive {
				if hdr, hbytes, herr := captureHeaders(ctx, d, route); herr != nil {
					t.Logf("header capture failed for %s %s: %v", leg.name, route, herr)
				} else {
					t.Logf("Headers %s %s: %d on-wire header bytes; set=%v", leg.name, route, hbytes, hdr)
				}
				if n, ok := parseReadinessThreads(tg.readyLine); ok {
					t.Logf("Readiness cross-check %s: reported (%d threads); requested TLANG_THREADS=%s", leg.name, n, tg.threads)
				} else {
					t.Logf("Readiness cross-check %s: (N threads) unparsed from %q", leg.name, tg.readyLine)
				}
			}

			// Resource sampler (design §B): fixed-rate pass only, gated by the
			// knob. Started around the measured window so a 100ms /proc walk
			// never perturbs the server cores (§B.6).
			var mon *resourceMonitor
			if cfg.resource {
				mon = startResourceMonitor(ctx, tg.cmd.Process.Pid, cfg.resourceEvery)
			}

			// Warm-up window discarded, then the measured window.
			_ = runGenerator(ctx, d, routeCfg, cfg.warmup, false)
			res := runGenerator(ctx, d, routeCfg, cfg.duration, true)
			var rstats resourceStats
			if mon != nil {
				rstats = mon.stop()
			}
			if res == nil {
				tg.stop()
				cancel()
				t.Fatalf("%s leg: route %s measured window produced no result", leg.name, route)
			}

			respBytes := res.respBytes
			if respBytes == 0 {
				if r, gerr := d.get(ctx, routeCfg.path); gerr == nil {
					respBytes = len(r.body)
				}
			}

			p50 := time.Duration(res.hist.ValueAtQuantile(0.50))
			p90 := time.Duration(res.hist.ValueAtQuantile(0.90))
			p99 := time.Duration(res.hist.ValueAtQuantile(0.99))
			p999 := time.Duration(res.hist.ValueAtQuantile(0.999))
			maxL := time.Duration(res.hist.Max())

			reportLeg(t, tg, routeCfg, res, gateMode, pinned, hasTaskset, serverSet, genSet, respBytes)
			// p99 enforced only for the TLang leg under disjoint pinning;
			// advisory for Node/Bun always (design §5.3). failed>0 stays fatal
			// for every leg regardless. TLANG_BENCH_P99_ADVISORY (opt-in,
			// default off) downgrades ONLY the TLang p99 breach to the same
			// ADVISORY warning Node/Bun get, so a complete three-way table can
			// be captured on a VM where cold-start/jitter trips the 2.5ms SLO
			// (design §2 advisory fallback). The pinned 4-thread derivation and
			// the disjoint-topology proof are untouched by this flag.
			p99Enforced := tg.name == "tlang" && pinned && !cfg.p99Advisory
			gateLeg(t, tg.name, res, p99, p99Enforced)

			// JSONL fixed-rate summary line (design §C.5), when configured.
			if summary != nil {
				line := newFixedSummaryLine(runNo, legOrderStr, route, tg, cfg, res,
					respBytes, p50, p90, p99, p999, maxL, topoRes, clockRes)
				if cfg.resource {
					applyResourceToLine(&line, leg.name, tg, cfg, res, rstats, clockRes)
				}
				if werr := summary.write(line); werr != nil {
					t.Logf("summary write failed (%s %s): %v", leg.name, route, werr)
				}
			}

			// Ladder pass (design §A): a SEPARATE measurement after the fixed
			// pass for this route. Pure measurement+report (no t.Fatalf except
			// its own parity gate); its latencies belong to the loaded regime
			// and are tagged pass="ladder" so the aggregator never mixes them
			// into the headline fixed-rate medians.
			if runLadder {
				sweepD := newHTTPDriver(&serverHandle{addr: tg.addr}, cfg.conns)
				steps := runRateSweep(ctx, t, sweepD, cfg, leg.name, route, body, ladder)
				if summary != nil && len(steps) > 0 {
					if werr := summary.write(newLadderSummaryLine(runNo, legOrderStr, route, tg, cfg, steps, topoRes, clockRes)); werr != nil {
						t.Logf("summary write failed (ladder %s %s): %v", leg.name, route, werr)
					}
				}
			}

			summaryRows = append(summaryRows, legSummary{
				name:    tg.name,
				version: tg.version,
				route:   route,
				p50:     p50,
				p99:     p99,
				max:     maxL,
				failed:  res.failed,
			})
		}

		tg.stop()
		cancel() // release this leg's ctx before the next leg
	}

	printComparison(t, summaryRows, routes)
}

// legOrderString renders the ordered leg names as the comma string recorded in
// each JSONL line's leg_order field (design §C.5).
func legOrderString(specs []legSpec) string {
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.name
	}
	return strings.Join(names, ",")
}

// newFixedSummaryLine builds the base (resource-free) fixed-rate JSONL line.
func newFixedSummaryLine(run int, legOrder, route string, tg *benchTarget, cfg benchConfig,
	res *benchResult, respBytes int, p50, p90, p99, p999, maxL time.Duration,
	topoRes topologyResult, clockRes clockHzResult) summaryLine {
	perreq := 0
	if cfg.perreq {
		perreq = 1
	}
	return summaryLine{
		Run: run, Pass: "fixed", LegOrder: legOrder, Route: route, Leg: tg.name,
		Version: tg.version, Rate: cfg.rate,
		P50Ms: msf(p50), P90Ms: msf(p90), P99Ms: msf(p99), P999Ms: msf(p999), MaxMs: msf(maxL),
		Failed: res.failed, Total: res.total, RespBytes: respBytes,
		CPUTopology: topoRes.status, Perreq: perreq, ClockHz: clockRes.hz,
		CPUSAvailable: clockRes.available, SweepSLOMs: sweepSLOMsList(cfg.sweepSLOs),
	}
}

// applyResourceToLine fills the resource fields of a fixed-rate line from the
// window's resourceStats + serving-unit mapping (design §B.4/§C.5).
func applyResourceToLine(line *summaryLine, leg string, tg *benchTarget, cfg benchConfig,
	res *benchResult, rstats resourceStats, clockRes clockHzResult) {
	rate := cfg.rate
	line.ResourceRate = &rate
	rss := float64(rstats.peakRSSKB) / 1024.0
	line.PeakRSSMiB = &rss
	tree := rstats.treeProcs
	line.TreeProcs = &tree
	su := servingUnits(leg, rstats.treeProcs)
	line.ServingUnits = &su
	if leg == "tlang" {
		th := procThreads(tg.cmd.Process.Pid)
		line.Threads = &th
	}
	if clockRes.available {
		cpuS := rstats.cpuSeconds(clockRes.hz)
		line.CPUSeconds = &cpuS
		if res.total > 0 {
			perMreq := cpuS / (float64(res.total) / 1e6)
			line.CPUSPerMreq = &perMreq
		}
	}
}

// newLadderSummaryLine builds a pass="ladder" JSONL line from the sweep steps.
// Latency scalars reflect the deciding (last recorded) rung; the per-rung detail
// lives in Sweep[]. It carries NO resource fields (sampling is off in the ladder
// pass per §B.6).
func newLadderSummaryLine(run int, legOrder, route string, tg *benchTarget, cfg benchConfig,
	steps []sweepStep, topoRes topologyResult, clockRes clockHzResult) summaryLine {
	perreq := 0
	if cfg.perreq {
		perreq = 1
	}
	last := steps[len(steps)-1]
	var total, failed int64
	var respBytes int
	if last.res != nil {
		total = last.res.total
		failed = last.res.failed
		respBytes = last.res.respBytes
	}
	return summaryLine{
		Run: run, Pass: "ladder", LegOrder: legOrder, Route: route, Leg: tg.name,
		Version: tg.version, Rate: last.rate,
		P50Ms: msf(last.p50), P90Ms: 0, P99Ms: msf(last.p99), P999Ms: 0, MaxMs: msf(last.maxL),
		Failed: failed, Total: total, RespBytes: respBytes,
		CPUTopology: topoRes.status, Perreq: perreq, ClockHz: clockRes.hz,
		CPUSAvailable: clockRes.available, SweepSLOMs: sweepSLOMsList(cfg.sweepSLOs),
		Sweep: sweepStepsJSON(steps),
	}
}

// probeVersion runs `<bin> --version` under ctx and returns the trimmed output,
// or "unknown" on any error (design §5.7a / §5.9).
func probeVersion(ctx context.Context, bin string) string {
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return "unknown"
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "unknown"
	}
	return v
}

// printComparison prints the per-route comparison table (FR-5.2): rows grouped
// by route (a route header line, then one row per leg with name, version,
// p50/p99/max in ms, and failure count). Routes are printed in the order they
// were driven (/bench first).
func printComparison(t *testing.T, summary []legSummary, routes []string) {
	t.Helper()
	t.Logf("=== Comparison (per route) ===")
	for _, route := range routes {
		t.Logf("--- route %s ---", route)
		t.Logf("%-6s %-20s %10s %10s %10s %8s", "leg", "version", "p50(ms)", "p99(ms)", "max(ms)", "failed")
		for _, s := range summary {
			if s.route != route {
				continue
			}
			t.Logf("%-6s %-20s %10.3f %10.3f %10.3f %8d",
				s.name, s.version, msf(s.p50), msf(s.p99), msf(s.max), s.failed)
		}
	}
}
