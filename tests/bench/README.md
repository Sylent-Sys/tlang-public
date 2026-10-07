# Comparative HTTP benchmark: TLang vs Node.js vs Bun

This directory holds the Node.js and Bun servers used by the comparative
benchmark `TestBenchmarkCompare` (in `tests/benchmark_compare_test.go`). The
whole suite is behind the `//go:build e2e` tag and the comparison is opt-in via
`TLANG_BENCH_COMPARE=1`.

## What this is

One load generator, three runtimes. `TestBenchmarkCompare` drives a single
open-loop, coordinated-omission-corrected generator (the same code
`TestBenchmark` uses) against each available runtime leg:

- **TLang** — the DB-free `echo_server.tl` fixture built on the clang-release
  leg (always runs).
- **Node.js** — `node/server.js` (runs when `node` is on `PATH`).
- **Bun** — `bun/server.ts` (runs when `bun` is on `PATH`).

Every leg is measured by the identical generator, HDR-style histogram,
warm-up/measured windows, keep-alive connection pool, CPU pinning, and gate.
Only the launched process differs.

### Fairness contract

- **Identical response bytes per route.** All three legs serve byte-identical
  bodies for every benchmarked route (`Content-Type: application/json`). This is
  enforced at runtime: before each route's warm-up on each leg, the generator
  issues one `GET <route>` and fails that leg unless the status is 200 and the
  body equals the shared reference constant for that route. A drift in any
  runtime's payload fails fast rather than silently skewing the comparison.
- **One generator.** No runtime brings its own load tool.
- **Same pinning and gate machinery** across all legs (see below).

### Real-work routes

`TestBenchmarkCompare` drives three routes per leg: the trivial baseline
`/bench` plus two real-work routes that do genuine per-request work, so the
comparison can surface where TLang's GC-free native execution separates from
Node's and Bun's GC/JIT runtimes. The two real-work routes favor
allocation + serialization (object/array churn, string building) over pure
arithmetic, because a mature JIT can match native on tight integer loops but
struggles to match a GC-free arena on allocation-heavy serialization.

- **`/bench`** — baseline. `200 {"id":1,"name":"bench"}` (23 bytes). Pure I/O,
  no per-request work; this is where Bun's HTTP layer ties TLang. Unchanged.
- **`/json`** — a nested object with 16 `WorkItem` elements plus three tag
  strings, built per request (object + array writer path + nested-struct
  allocation, ~20 allocations/request). **716 bytes.** Exercises the
  object/array serialization path with meaningful allocation churn.
- **`/work`** — a top-level array of 32 rows, each an object with a built string
  (`"row-<i>-of-32"`), built per request via a loop + `.push` onto a freshly
  allocated slice. **1407 bytes.** This is the heavier route and the primary
  real-work signal: allocation churn + string building + array serialization,
  the alloc-heavy path where the native arena should separate from Node/Bun.

On the TLang leg each real-work route does its loop + allocation + serialization
on the request hot path (that is how a TLang handler is written). Node and Bun
build their constant bodies **once at startup** and serve the cached string — the
most favorable possible implementation of the same response. The fairness
contract is byte-identity of the *response*, not identical internals; if TLang
still wins while Node/Bun are allowed to cache, the result is conservative.

A real-work route may legitimately trip the **enforced** TLang p99 gate under
pinning (it does real work, so p99 can exceed 2.5 ms) — that is a true signal,
not a bug. The measurement step may run real-work routes advisory (unpinned) or
with a raised threshold.

## Env vars

Opt-in gate:

- `TLANG_BENCH_COMPARE=1` — run the comparative benchmark (without it, the test
  skips). This is independent of `TLANG_BENCH=1`, which gates the single-runtime
  `TestBenchmark`.

Route selection:

- `TLANG_BENCH_PATH` — optional single-route override. When unset (default),
  `TestBenchmarkCompare` drives all three routes per leg (`/bench`, `/json`,
  `/work`) and prints a per-route table. When set to one of `/bench`, `/json`,
  or `/work`, only that route runs. An unknown value fails the test loudly, like
  any other malformed knob. `TestBenchmark` (single-runtime) ignores this and
  always drives `/bench`.

Shared load/pinning knobs (apply to both benchmarks so the two runs are
configured identically):

- `TLANG_BENCH_RATE` — target request rate R in req/s (default `20000`).
- `TLANG_BENCH_CONNS` — persistent keep-alive connections C (default `50`).
- `TLANG_BENCH_WARMUP` — discarded warm-up window (Go duration, default `3s`).
- `TLANG_BENCH_DURATION` — measured window (Go duration, default `10s`).
- `TLANG_BENCH_SERVER_CORES` — taskset core list for the server child, e.g.
  `2-5` (empty = unpinned).
- `TLANG_THREADS` — worker/thread count N; when unset it defaults to the number
  of server cores under pinning, else the online CPU count.

Server-contract vars (set by the harness for every leg):

- `TLANG_PORT` — kept at `0` (ephemeral) for all legs for parity; the actual
  bound port is read from the readiness line.
- `TLANG_HOST` — always pinned to `127.0.0.1`.

### Re-measurement knobs (additive; all default so the fixed-rate run is unchanged)

These knobs drive the methodologically-sound re-measurement (per-request work,
a rate ladder, RSS/CPU sampling, and repeated runs with rotated order). Every
one is off/neutral by default, so a plain `go test` or a `docker run` without
them is byte-identical to the historical fixed-rate run. Malformed values fail
loudly (`t.Fatalf`) like any other knob.

Per-request work (D1):

- `TLANG_BENCH_PERREQ` — `1` makes the Node and Bun legs build each `/json` and
  `/work` body **per request** (`JSON.stringify` on the hot path) instead of
  serving a startup-cached string, matching how a TLang handler is written.
  Default off (cached). The response bytes stay byte-identical either way; this
  only changes *how* a leg produces them.

Rate ladder / max-sustained-throughput-under-SLO (Mechanism A):

- `TLANG_BENCH_RATE_LADDER` — comma list of ascending req/s (e.g.
  `20000,40000,60000,80000,100000,120000`). When set, a separate ladder pass
  drives each rung against the already-running leg and reports the highest rate
  that still holds the SLO with `failed==0`. Unset = single `[TLANG_BENCH_RATE]`
  (today's behavior). Values must be positive and strictly ascending.
- `TLANG_BENCH_SWEEP_SLO` — comma list of p99 SLO durations the ceiling is
  reported at (default `2500us,5ms,10ms`; the first equals the production p99
  threshold). The ceiling is reported per threshold so the reader sees how it
  moves with the SLO.
- `TLANG_BENCH_SWEEP_CONNS` — `0`/unset scales the connection pool with the rate
  (`max(50, rate/400)`, driver rebuilt on change) so the pool never caps the
  ladder; a positive int holds the pool fixed at that value for every rung
  (reported as "sustained rate at C=`<conns>` connections").
- `TLANG_BENCH_SWEEP_WARMUP` — per-rung discarded warm-up window (default `1s`).
- `TLANG_BENCH_SWEEP_DURATION` — per-rung measured window (default `5s`).

RSS + CPU-per-request sampling (Mechanism B):

- `TLANG_BENCH_RESOURCE` — `1` enables the `/proc` process-tree sampler (peak
  RSS, CPU-seconds, CPU-s/Mreq, tree/serving-unit counts). Default off. Run it in
  a **separate** fixed-rate pass from the ladder: the sampler shares the
  generator's cores, so sampling during the saturating rungs would depress the
  observed ceiling.
- `TLANG_BENCH_RESOURCE_INTERVAL` — sample period (default `100ms`).
- `TLANG_BENCH_CLOCKHZ_CALIBRATE` — `1` runs an optional ~500 ms CPU-burn
  calibration to corroborate the `getconf CLK_TCK` probe (default off; the
  `{100,250,300,1000}` sanity-bound is sufficient on this box). CPU-s is marked
  `unavailable` and omitted from the tables if the probe is outside that set or
  the calibration disagrees by more than ±20%.

Repeat / rotated-order runs (Mechanism C):

- `TLANG_BENCH_LEG_ORDER` — comma list naming the leg order (e.g.
  `node,bun,tlang`); subset of `tlang,node,bun`. Unknown name fails loudly.
  Unset = default `tlang,node,bun`. Rotating the order across runs breaks the
  "the leg that runs first eats cold start" confound.
- `TLANG_BENCH_ROUTE_ORDER` — comma list permuting `/bench,/json,/work`; subset
  of the known route set, unknown route fails loudly. Unset = default order.
- `TLANG_BENCH_SUMMARY_OUT` — path to a JSONL file; when set, each (leg,route)
  measurement appends one JSON line (schema in design §C.5), consumed by the
  aggregator (`go run ./tests/bench/aggregate -in <file>`). Open error → warn
  and skip, never fatal.
- `TLANG_BENCH_ALLOW_SHARED_CORES` — `1` downgrades the fatal "server/generator
  share a physical core" check to an advisory warning (default off = fatal under
  pinning).
- `TLANG_BENCH_P99_ADVISORY` — `1` downgrades **only** the TLang p99-SLO breach
  from a fatal enforced-gate failure to the same `ADVISORY` warning Node/Bun
  already get, so a complete three-way table can be captured on a VM where
  cold-start / scheduling jitter pushes TLang's first-touched-route p99 over the
  2.5 ms SLO. Default off = enforced. **`failed > 0` stays fatal for every leg**,
  and the pinned four-thread derivation (`0,2,4,6` → `TLANG_THREADS=4`) and the
  disjoint-core topology proof are untouched. Use it only when the enforced gate
  trips on this VM's jitter; the breach is itself a result and must be reported
  (see `RESULTS.md`), never laundered. The 2.5 ms SLO is meant to be *enforced*
  on isolated bare-metal.

Entrypoint-only shell knobs (read by `docker/bench-entrypoint.sh`, not the Go
harness):

- `BENCH_REPEATS` — number of repeats in the branch-(b) batch (default `1` =
  the byte-identical single run). This task uses `6` so the balanced
  6-permutation leg-order set runs each ordering exactly once.
- `BENCH_ROTATE` — `1` to rotate leg (and matching route) order per repeat
  (default `0`).
- `BENCH_GEN_CORES` — generator core list for the per-iteration `taskset`.

### The two run topologies

1. **Default advisory single run** (branch a). No `BENCH_REPEATS`/`BENCH_ROTATE`;
   the operator supplies any task knobs via `docker run -e …`. The entrypoint
   `exec`s one `go test`, byte-identical to the historical image.
2. **Pinned branch-(b) batch** (`BENCH_REPEATS=6 BENCH_ROTATE=1`). The entrypoint
   truncates the JSONL, scopes `TLANG_BENCH_PERREQ=1` + the rate ladder, and runs
   **two sub-passes**: a ladder pass with `TLANG_BENCH_RESOURCE=0` (honest server
   ceiling), then a fixed-rate resource pass with `TLANG_BENCH_RESOURCE=1` (RSS /
   CPU at a sub-saturation rate). Each repeat walks the balanced 6-permutation
   leg order (and matching route order) and is pinned per iteration; the
   aggregator runs at the end. Neither branch ever exports `TLANG_THREADS` — the
   harness derives `4` from the four-core server set `0,2,4,6`.

## Installing Node and Bun

These runtimes are not in the tlang-dev container; install them to run their
legs (otherwise each absent leg self-skips with a log line and the run
continues).

- **Node.js — minimum version 23.1.** The N-process model binds N independent
  sockets with `server.listen({ ..., reusePort: true })`, and the `reusePort`
  listen option requires Node 23.1+. Install via `nvm`
  (`nvm install 23`) or your distro package, then confirm:

  ```
  node --version   # must be >= v23.1
  ```

- **Bun** — any current release:

  ```
  curl -fsSL https://bun.sh/install | bash
  bun --version
  ```

## taskset pinning recipe

The p99 SLO gate is enforced only under **disjoint** CPU pinning: the whole
`go test` runs pinned to one core set (the generator) and the server child is
pinned to a disjoint set via `TLANG_BENCH_SERVER_CORES`. Pick disjoint
generator and server cores — e.g. generator on `0-1`, server on `2-5`:

```
TLANG_BENCH_COMPARE=1 TLANG_BENCH_SERVER_CORES=2-5 \
  taskset -c 0-1 go test -tags e2e -run 'TestBenchmarkCompare$' ./tests/
```

Pinning (`taskset`) and the `/proc` affinity read are Linux-only. On a
non-pinned or non-Linux host the comparison still runs with an advisory gate for
every leg.

## Example invocations

Low-rate smoke run (fast, validates the servers' contract and shutdown):

```
TLANG_BENCH_COMPARE=1 TLANG_BENCH_RATE=2000 TLANG_BENCH_WARMUP=1s TLANG_BENCH_DURATION=2s \
  go test -tags e2e -run 'TestBenchmarkCompare$' -v ./tests/
```

Full pinned run:

```
TLANG_BENCH_COMPARE=1 TLANG_BENCH_SERVER_CORES=2-5 \
  taskset -c 0-1 go test -tags e2e -run 'TestBenchmarkCompare$' -v ./tests/
```

## Reading the output

Each leg prints a report block per route (`=== TLang benchmark report ===`,
`=== Node benchmark report ===`, `=== Bun benchmark report ===`) with the CPU
info, gate mode, pinning, `TLANG_THREADS`, load knobs (C/R), the measured
route's payload sizes, request totals/failures, warm-up/duration/wall time, and
the latency percentiles (p50/p90/p99/p99.9/max). After all legs, a per-route
comparison table is printed: for each route (`/bench` first) a route header
followed by one row per leg (name, version, p50/p99/max in ms, and failures), so
you can read each runtime side by side on the same route.

Gate semantics:

- **`failures != 0` fails the whole run** for every leg (TLang, Node, Bun),
  pinned or not.
- **p99 above the 2.5 ms threshold** is fatal only for the **TLang** leg under
  disjoint pinning. For Node and Bun it is always **advisory** (reported and
  compared, never fails the test) — we compare them to TLang, we do not hold
  them to TLang's SLO.
- A **real-work route** (`/json`, `/work`) may legitimately push the enforced
  TLang p99 past 2.5 ms since it does genuine per-request work — a true signal,
  not a bug. Run real-work routes advisory (unpinned) or with a raised threshold
  when you want the measurement without the gate tripping.
