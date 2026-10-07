//go:build e2e

// Benchmark suite (tests-suites design §6). A single TestBenchmark (a Test, not
// a Go Benchmark, so the gate logic and env-gating are explicit and the e2e
// harness lifecycle is reused) behind //go:build e2e AND opt-in via
// TLANG_BENCH=1. It drives an open-loop, coordinated-omission-corrected load
// generator over a pool of persistent keep-alive connections against the
// DB-free GET /bench route (clang-release leg), with a warm-up phase then a
// measured window recorded into the HDR-style histogram (histogram.go).
//
// The p99 gate is ENFORCED only under disjoint CPU pinning (the whole go test
// runs under taskset -c <genCores> and the server child is pinned to a disjoint
// TLANG_BENCH_SERVER_CORES set); otherwise it is ADVISORY (print, do not fail on
// p99). A failed request ALWAYS fails the test, pinned or not (design §6.1/§6.4).
//
// This test is a thin consumer of the shared benchmark machinery in
// bench_shared.go (design §5.6); the shared helpers reproduce the former inline
// pinning/threads derivation, generator, report, and gate byte-for-byte so the
// observable measurements and gate behavior are unchanged.
//
// Nothing here modifies a locked package: the server binary is built through the
// shared e2e harness (buildFixture), and launched with an optional taskset -c
// prefix for pinning. The generator uses only net/http + stdlib.

package tests

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestBenchmark is the opt-in benchmark + p99 gate (design §6). It is behind the
// e2e tag and additionally requires TLANG_BENCH=1.
func TestBenchmark(t *testing.T) {
	if os.Getenv("TLANG_BENCH") != "1" {
		t.Skip("benchmark opt-in: set TLANG_BENCH=1")
	}

	cfg := parseBenchConfig(t)

	// TestBenchmark is single-runtime and always drives /bench; it does not read
	// the route list. Pin cfg.path/refBody to /bench explicitly so behavior is
	// provably unchanged even when TLANG_BENCH_PATH is unset (cfg.path == "").
	// TLANG_BENCH_PATH, if set, is still honored (parseBenchConfig validated it).
	if cfg.path == "" {
		cfg.path = "/bench"
		cfg.refBody = benchRefBody
	}

	// Gate mode + TLANG_THREADS via the shared helpers (design §5.6), the SAME
	// code path TestBenchmarkCompare uses so N and the gate mode cannot drift.
	// The gate is enforced only under disjoint CPU pinning (design §6.1).
	pinned, serverSet, genSet, hasTaskset := benchPinning(cfg)
	threads := benchThreads(pinned, serverSet)

	ctx, cancel := context.WithTimeout(context.Background(),
		buildTimeout+startTimeout+cfg.warmup+cfg.duration+shutdownTimeout+2*requestTimeout)
	defer cancel()

	src := e2eFixturePath(t, echoServerFixture)
	bin, err := buildFixture(t, ctx, legs[0], src, false) // legs[0] == clang-release
	if err != nil {
		t.Fatalf("build %s (clang-release): %v", echoServerFixture, err)
	}

	tg := startBenchTarget(t, ctx, "tlang", []string{bin}, cfg.serverCores, threads, pinned)
	defer tg.stop()

	d := newHTTPDriver(&serverHandle{addr: tg.addr}, cfg.conns)

	// Warm-up window: run the generator and discard its samples (design §6.2).
	_ = runGenerator(ctx, d, cfg, cfg.warmup, false)

	// Measured window: record into the histogram.
	result := runGenerator(ctx, d, cfg, cfg.duration, true)
	if result == nil {
		t.Fatalf("measured window produced no result")
	}

	// Measure the /bench response payload size once (recorded, design §6.4).
	respBytes := result.respBytes
	if respBytes == 0 {
		if r, err := d.get(ctx, cfg.path); err == nil {
			respBytes = len(r.body)
		}
	}

	gateMode := "advisory"
	if pinned {
		gateMode = "enforced"
	}

	p99 := time.Duration(result.hist.ValueAtQuantile(0.99))

	// §6.4 reporting (always printed under TLANG_BENCH=1).
	reportLeg(t, tg, cfg, result, gateMode, pinned, hasTaskset, serverSet, genSet, respBytes)

	// Gate (design §6.4): failures always fatal; p99 enforced only under
	// disjoint pinning, advisory otherwise.
	gateLeg(t, "tlang", result, p99, pinned)
}
