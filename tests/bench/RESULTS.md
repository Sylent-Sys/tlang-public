# Comparative benchmark results: TLang vs Node.js vs Bun

These are MEASURED results from the reproducible benchmark image
(`docker/Dockerfile.bench`) driving the one shared load generator
(`TestBenchmarkCompare`) against all three runtime legs across all three routes.
Every number below is copied from the real captured output of the branch-(b)
balanced 6-permutation batch described in "Environment"; nothing here is
estimated or hand-tuned. The raw per-run artifact is
`tests/bench/.sweep-summary.jsonl` (regenerated per run, not committed) and the
aggregator fragment it produced.

> **Read this first — these are directional, not publishable.** The runs were
> taken inside a Docker Desktop Linux VM on a desktop CPU (AMD Ryzen 5 5600),
> not on an isolated, tickless, pinned bare-metal host. Absolute tail latencies
> (p99, p99.9, max) carry VM scheduling jitter and co-tenant noise that is large
> relative to the differences between runtimes — large enough that **every leg
> (TLang, Node, and Bun) suffered at least one catastrophic multi-hundred-ms
> single-run blow-up** on this box (see "Tail behavior"). The durable, portable
> part of this work is the **methodology** — one reproducible image, one load
> generator, byte-identical payloads per route, per-request work on all three
> legs, coordinated-omission correction, rotated leg/route order across 6
> repeats, and a `/proc`-based RSS/CPU sampler — plus the two **resource**
> results (RSS and CPU-per-request), which are large and consistent. The
> millisecond latency tails are not. Re-run on isolated hardware with the
> enforced recipe for a defensible tail claim.

## Headline

On this Docker Desktop VM, with per-request work on all three legs and leg/route
order rotated across 6 repeats:

- **Memory (peak RSS): TLang wins decisively and consistently.** TLang ~2.75 MiB
  vs Node ~326 MiB vs Bun ~146–154 MiB — TLang is roughly **50×** smaller than
  Bun and **~120×** smaller than Node. This is the clearest result in the whole
  experiment and it is stable across every route and repeat. (Caveat: TLang runs
  one process / four threads in one address space, so its RSS is a single
  `VmRSS`; Node/Bun sum N separate processes that each map the runtime binary, a
  known asymmetry — see "Serving-unit topology" and the RSS honesty note.)
- **CPU per request: TLang wins, ~2× more efficient.** CPU-seconds per million
  requests: TLang ~40–46 vs Node ~83–99 vs Bun ~87–97, across routes.
- **Median latency (p50): a three-way tie with no consistent winner.** The gap
  between the fastest and slowest leg's median is **single-digit to low-tens of
  microseconds** (aggregator medians over 12 rows: `/bench` TLang 0.240 / Node
  0.247 / Bun 0.270 ms; `/json` Node 0.271 / TLang 0.289 / Bun 0.297 ms — Node
  lowest; `/work` Node 0.296 / Bun 0.297 / TLang 0.300 ms — Node lowest). The old
  doc's "TLang always has the lowest p50, ~50–80 µs edge" claim does **not**
  survive per-request work plus rotation and is retracted.
- **Max-sustained-throughput-under-SLO: NOT measurable to saturation on this
  box.** The load generator was pinned to 2 cores (`NumCPU=2` in the generator
  process) and becomes the bottleneck at ≥40k req/s, so the server ceiling is a
  **lower bound only** (≥20k req/s held the SLO cleanly for every leg; above that
  the generator, not the server, is the limit). See "Sustained-rate ladder".

The intended discriminating headline (the sustained-rate ladder) could not be
measured here because of the 2-core generator slice; the **resource** figures
(RSS, CPU/req) are the discriminating results this box could actually produce,
and they favor TLang. The latency tails do not separate the runtimes on this VM.

## Environment

| Property | Value |
| --- | --- |
| Host CPU | AMD Ryzen 5 5600 (6 physical cores / 12 SMT threads) |
| Host OS | Windows + Docker Desktop (Linux container VM) |
| Container | `tlang-bench:latest` built from `docker/Dockerfile.bench` |
| Container base | `golang:1.26-trixie` (digest-pinned) |
| `--cpuset-cpus` | `0-11` (whole 12-lane VM exposed to the container) |
| Server pinning | `taskset -c 0,2,4,6` (four distinct physical cores) |
| Generator pinning | `taskset -c 8,10` (two cores, disjoint from the server set) |
| `TLANG_THREADS` | **4**, derived from the four-core server set (NEVER exported) |
| `NumCPU` in the generator process | 2 (`taskset -c 8,10`) — the ladder bottleneck |
| CPU topology | **disjoint** (verified at run time, see below) |
| `getconf CLK_TCK` | 100 (within the known kernel `CONFIG_HZ` set → CPU-s available) |

### Observed CPU topology (`/proc/cpuinfo`, verified at run time)

The harness mapped each pinned lane to its `(physical id, core id)` and asserted
the server set is four **distinct physical cores** and disjoint from the
generator set:

```
server[cpu0=(p0,c0) cpu2=(p0,c1) cpu4=(p0,c2) cpu6=(p0,c3)]
gen[cpu8=(p0,c4) cpu10=(p0,c5)]
→ cpu_topology: disjoint
```

The LinuxKit VM pairs SMT siblings consecutively, so the even lanes `0,2,4,6`
land on four separate physical cores and `8,10` on two more, all on package 0.

> **Container-lane → host-lane caveat.** On Docker Desktop the mapping from a
> container CPU lane to a physical host lane is **not** guaranteed; `--cpuset-cpus`
> constrains the container's view, and the LinuxKit VM schedules those onto host
> threads opaquely. The disjointness and four-distinct-physical-cores proof above
> holds at the **container-topology level only** — this is not bare-metal
> pinning, and that is why the gate is advisory here (next section).

### Pinned runtime versions (verified in-container)

| Tool | Version |
| --- | --- |
| Go | go1.26.8 linux/amd64 |
| clang | Debian clang 19.1.7 |
| TLang leg | `clang-release` build of `tests/e2e/echo_server.tl` |
| Node.js | v24.21.0 |
| Bun | 1.4.2 |

### Gate mode: enforced attempted, downgraded to advisory for TLang p99 (a real finding, not a footnote)

The p99 SLO is **2.5 ms**. Under disjoint pinning the harness normally *enforces*
that SLO for the TLang leg (fatal on breach) and keeps it advisory for Node/Bun.
On this VM that enforced run **cannot complete**:

> **Under disjoint pinning on this Docker Desktop VM, TLang's p99 on its
> first-touched route was ~3.9 ms at best and ranged to 24–33 ms (cold-start plus
> VM scheduling jitter), always EXCEEDING the 2.5 ms SLO.** A first attempt with
> the enforced gate (`BENCH_REPEATS=6 BENCH_ROTATE=1`, no advisory flag) tripped
> the fatal TLang gate on the first-touched route in **all 12 repeats** (6 ladder
> + 6 fixed-rate), aborting each `go test` before TLang wrote a single data row —
> yielding zero TLang numbers.

To capture a complete three-way table, the batch was re-run with the opt-in
`TLANG_BENCH_P99_ADVISORY=1` flag, which downgrades **only** the TLang p99 breach
to the same `ADVISORY` warning Node/Bun already receive. `failed > 0` stays fatal
for every leg (and every leg had `failed == 0` on every completed window); the
pinned four-thread derivation and the disjoint-topology proof are untouched. The
flag defaults to **off (enforced)**; it was **ON** for the captured run below and
is documented in `tests/bench/README.md`.

**The breach is itself a result.** The 2.5 ms p99 SLO is **not meetable under
this VM's jitter** by any of the three runtimes (p99 medians: TLang 5.4–7.6 ms,
Node 4.6–4.8 ms, Bun 4.5–4.8 ms; all three had far worse single runs). That is
exactly why the methodology calls for isolated, tickless bare-metal to *enforce*
the gate — on a shared Docker Desktop VM the gate measures the VM, not the
runtime.

## Load configuration

| Knob | Value |
| --- | --- |
| Per-request work (`TLANG_BENCH_PERREQ`) | **1** — Node and Bun build each `/json`/`/work` body per request (`JSON.stringify` on the hot path), matching the TLang handler (D1 fairness fix) |
| Fixed-rate (control) | 20,000 req/s, 200,000 requests per leg per route |
| Rate ladder | 20000, 40000, 60000, 80000, 100000, 120000 req/s (auto-stops at the first failing rung) |
| SLO thresholds reported | 2.5 ms, 5 ms, 10 ms p99 |
| Connections (C) | scaled per rung: `max(50, rate/400)` (50 at 20k) |
| Warm-up / measured (fixed) | 3 s / 10 s |
| Warm-up / measured (per ladder rung) | 1 s / 5 s |
| Generator | open-loop, constant-rate, coordinated-omission corrected |
| Routes (rotated per repeat) | `/bench` (23 B), `/json` (716 B), `/work` (1407 B) |
| Payload parity | byte-identical response bodies per route, gated before each warm-up |

### Two-pass, 6-repeat, rotated structure

The branch-(b) batch runs **two sub-passes** so the resource sampler never
perturbs the generator-bound ladder:

1. **Ladder pass** (`TLANG_BENCH_RESOURCE=0`): the rate sweep, sampler off so the
   generator ceiling is honest.
2. **Fixed-rate resource pass** (`TLANG_BENCH_RESOURCE=1`): the 20k control rate
   with the `/proc` RSS+CPU sampler on (sub-saturation, so a 100 ms `/proc` walk
   on the generator lanes cannot move the result).

Each pass runs **6 repeats** walking the balanced 6-permutation leg order
(`tlang,node,bun` / `node,bun,tlang` / `bun,tlang,node` / `tlang,bun,node` /
`bun,node,tlang` / `node,tlang,bun`) with the matching route-order rotation, so
no leg is permanently first and no route permanently first — the ordering
confound that produced the old doc's 58 ms `/bench` "VM artifact" is broken.

Invocation (the exact captured run):

```
docker run --rm -v "${PWD}:/src" --cpuset-cpus 0-11 \
  -e BENCH_REPEATS=6 -e BENCH_ROTATE=1 \
  -e BENCH_GEN_CORES=8,10 -e TLANG_BENCH_SERVER_CORES=0,2,4,6 \
  -e TLANG_BENCH_P99_ADVISORY=1 \
  -e TLANG_BENCH_RATE_LADDER=20000,40000,60000,80000,100000,120000 \
  tlang-bench:latest
```

> **Per-repeat run tag limitation.** The entrypoint rotates the leg/route order
> per repeat but does not set a distinct `TLANG_BENCH_RUN` per repeat, so every
> JSONL line carries `run=1`. The aggregator therefore groups a cell's 12 lines
> (6 ladder-pass + 6 resource-pass fixed rows) by `(route, leg)` and distinguishes
> repeats only by `leg_order`. Medians and ranges below are over those 12 (for
> latency) or 6 (for RSS/CPU, resource pass only) rows.

## Serving-unit topology (D5 — the independent concurrency proof)

Each leg's launched process tree was walked via `/proc` during the measured
window. "Serving units" is the **observed** count that actually serves requests,
derived from the raw process count by leg (the Node supervisor does not serve):

| Leg | intended N | readiness line | raw `tree_procs` | serving units | note |
| --- | --- | --- | --- | --- | --- |
| TLang | 4 | `(4 threads)` | 1 | **1 process / 4 scheduler threads** | `/proc/<pid>/status Threads: 5` (4 scheduler + 1 main); one address space |
| Node | 4 | `(4 threads)` | 5 | **4** (`tree_procs − 1`) | supervisor runs only the readiness barrier, holds no `http.Server` |
| Bun | 4 | `(4 threads)` | 4 | **4** (`tree_procs`) | the primary itself serves (Nth serving unit) |

The readiness `(N threads)` line is a cross-check only (an echo of the requested
`TLANG_THREADS` for Node/Bun; a weak liveness signal for TLang). The `/proc` tree
walk is the independent proof: all three legs ran **4 serving units**, so this is
a worker-matched comparison. TLang parallelizes with **threads in one process**
while Node and Bun use **separate processes** — "4 workers" means different OS
topologies, which the reader must weigh and which also explains the RSS asymmetry
below.

## Header parity (D3 — captured, not forced byte-identical)

Bodies are byte-identical (23 / 716 / 1407 B, gated). Headers are **not** forced
equal; the per-leg wire-header set and estimated on-wire byte count:

| Leg | header set | on-wire header bytes (`/bench`→`/work`) | Δ vs TLang |
| --- | --- | --- | --- |
| TLang | `Date`, `Content-Type`, `Content-Length`, `Connection: keep-alive` | 132 / 133 / 134 | — |
| Node | `Date`, `Content-Type`, `Connection: keep-alive`, `Keep-Alive: timeout=5` | 135 / 135 / 135 | +1 to +3 (adds `Keep-Alive`, omits `Content-Length` on these responses) |
| Bun | `Date`, `Content-Type`, `Content-Length` | 108 / 109 / 110 | −24 (no `Connection`/`Keep-Alive` header) |

The header-byte spread (~108–135 B) is small relative to the 716/1407 B bodies,
so it does not materially move the latency comparison, but it is recorded rather
than hidden.

## Sustained-rate ladder (Mechanism A) — generator-bound, lower bound only

**The server ceiling could NOT be measured to saturation on this box.** The
generator was pinned to 2 cores (`NumCPU=2`) and its own dispatch ceiling is hit
before any server's: at **20k req/s every rung was clean (0/54 generator-bound)**,
but at **40k and above roughly half of all rungs across all legs went
generator-bound** (28–29 of 54 at 40–60k), with the coordinated-omission
correction then charging the generator's own lateness as "latency" — p99 at
gen-bound rungs explodes to hundreds or thousands of ms for every leg equally.

Per the aggregator, a gen-bound rung is excluded from the sustained ceiling (it
is a lower bound, not a server result). The honest reading:

- **All three legs held the 2.5 ms p99 SLO at 20k req/s** (the clean rung).
- **Above 20k the limiter is the 2-core generator slice, not the server**, so the
  true server ceiling is **≥ 20k req/s and otherwise unknown on this box**. The
  aggregator's "median rung 120000" entries are mostly gen-bound artifacts and
  must not be read as a 120k server ceiling.

This is a limitation of running a load generator on a 2-core slice of a 6-core
desktop under a VM; it is **not** a server property. Measuring
max-sustained-throughput-under-SLO properly needs a generator with more cores
than the server (or a separate machine) — i.e. isolated hardware, the same reason
the gate is advisory here.

## Resource efficiency (Mechanism B) — the discriminating result on this box

Fixed-rate resource pass, 20k req/s, median + min–max across the 6 repeats.
`clock_hz=100` (probe) within the known set, so CPU-s is available.

| Route | Leg | peak RSS (MiB) median | CPU-s / Mreq median |
| --- | --- | --- | --- |
| `/bench` | TLang | **2.75** | **39.85** |
| `/bench` | Node | 325.785 | 83.4 |
| `/bench` | Bun | 145.656 | 86.55 |
| `/json` | TLang | **2.75** | **42.95** |
| `/json` | Node | 326.41 | 94.15 |
| `/json` | Bun | 153.398 | 93.75 |
| `/work` | TLang | **2.875** | **44.35** |
| `/work` | Node | 326.785 | 99.4 |
| `/work` | Bun | 154.102 | 97.1 |

TLang uses ~50× less memory than Bun and ~120× less than Node, and ~2× less CPU
per request than either. These are the figures a GC-free arena is expected to win
on, and they held across every route and repeat.

> **RSS honesty note.** `VmRSS` counts resident pages per process; the Node (5
> procs) and Bun (4 procs) sums count each process's mapped copy of the runtime
> binary, slightly over-counting shared code versus TLang's single-process
> threads. `Pss` (`smaps_rollup`) would be fairer but is far costlier to sample
> every 100 ms, so we report `VmRSS` and state the bias. Even generously
> discounting shared pages, the TLang advantage (single-digit MiB vs hundreds) is
> far larger than the asymmetry.

## Idle baseline / control — fixed-rate 20k median latency (NOT the headline)

At 20k req/s with ~0.25 ms medians the servers are >99% idle, so this table is
the **control**, not the discriminating result. Median + min–max across the 12
rows per cell; p99.9 and max are **single-event statistics** (at 200k requests
p99.9 is ~200 samples and max is one event) and no conclusion is drawn from them
on a single run.

Values below are copied verbatim from the aggregator fragment (median with
min–max in brackets), over the 12 fixed-rate rows per cell:

| Route | Leg | p50 (ms) | p90 (ms) | p99 (ms) median [min–max] |
| --- | --- | --- | --- | --- |
| `/bench` | TLang | 0.240 [0.206–2.2] | 1.745 | 5.366 [3.903–32.997] |
| `/bench` | Node | 0.247 [0.235–1.264] | 1.393 | 4.776 [0.264–6.373] |
| `/bench` | Bun | 0.270 [0–0.45] | 1.921 | 4.817 [0.264–213.647] |
| `/json` | TLang | 0.289 [0.232–848.3] | 2.208 | 7.586 [4.026–1088.422] |
| `/json` | Node | 0.271 [0–0.497] | 1.624 | 4.571 [0–21.496] |
| `/json` | Bun | 0.297 [0–1.823] | 2.040 | 4.817 [0–197.919] |
| `/work` | TLang | 0.300 [0.25–5.865] | 2.118 | 7.004 [3.908–240.386] |
| `/work` | Node | 0.296 [0–0.54] | 1.808 | 4.571 [0–1298.137] |
| `/work` | Bun | 0.291 [0–0.472] | 1.812 | 4.489 [0–8.798] |

> **Degenerate-row note.** Some min–max floors read `0`, `0.264`, or `0.242`:
> those come from the one short readiness/first-touch window per leg that
> recorded `total ≈ 1` request (its percentiles collapse to a single sample).
> They are left in because the aggregator reports every row it was given; read
> the **median** as the representative value and the min–max as the raw envelope,
> not as nine full 200k-request runs.

**Median gap (correct units):** the fastest-to-slowest leg p50 spread is
**~30 µs on `/bench` (0.240→0.270), ~26 µs on `/json` (0.271→0.297), and ~4 µs on
`/work` (0.296→0.300)** — single-digit to low-tens of microseconds, and the
leader changes by route (TLang on `/bench`, Node on `/json` and `/work`). It is a
three-way tie, not a TLang win.

**p50/p90 shape:** p90 is ~6–8× p50 for every leg here. That is a bimodal
signature, but at 20k/idle with ~0.25 ms medians it reflects VM scheduling
wake-up and steal (the p90 tail is dominated by the handful of requests that hit
a scheduler hiccup), not runtime quality — consistent with the D2 reasoning that
the shared VM, not the runtime, governs the sub-millisecond-to-millisecond tail.

## Tail behavior (p99 / p99.9 / max) — VM jitter dominates for every leg

The p99 **range** column above is the story: on this VM **every runtime had at
least one catastrophic single-run blow-up**, and the worst offender is a
different runtime than the median would suggest:

- **TLang:** 3 of 12 fixed-rate runs with p99 > 50 ms, worst **1088 ms** (`/json`).
- **Node:** 1 of 12, worst **1298 ms** (`/work`) — the single worst value measured.
- **Bun:** 2 of 12, worst **213.6 ms** (`/bench`).

These are VM-steal / queueing events, not runtime properties; they land on
whichever leg happens to be running when the hypervisor preempts the VM. The p99
medians (TLang 5.4–7.6 ms, Node 4.6–4.8 ms, Bun 4.5–4.8 ms) are all in the same
class and all above the 2.5 ms SLO.

### Nagle / delayed-ACK rule-out (D9)

TLang sets `TCP_NODELAY` on every accepted socket (confirmed, `runtime/src/net.c`
line 122). The old doc's suspicious ~41.7 ms TLang `/bench` p99.9 (near Linux's
40 ms delayed-ACK floor) **did not survive the rotated repeats**: across all 12
runs no leg showed a persistent cluster at the 40 ms floor — the few near-40 ms
values (Node `/json` max 43.8 ms, Node `/work` max 42.6 ms, TLang `/work` max
40.9 ms) are scattered single-event maxima on different legs, not a repeatable
~40 ms stall on any one leg. That is the signature of the ordering/cold-start
artifact the old doc suspected, **not** a Nagle/coalescing bug. No
`socket.setNoDelay(true)` was needed on Node (its p99.9/max never clustered at
the floor); Bun exposes no per-socket `setNoDelay` from `Bun.serve` and showed no
floor cluster either, so nothing was changed there (documented as a Bun
limitation only if a future run shows one).

## Bottom line

On this Docker Desktop VM, with per-request work and rotated ordering, **the three
runtimes cannot be cleanly separated on latency, and here is precisely why:** the
intended discriminating metric (max-sustained-throughput-under-SLO) is
**generator-bound** above 20k req/s on the 2-core generator slice, and the latency
**tails are jitter-tripped** for all three legs (each had a multi-hundred-ms blow-up;
the 2.5 ms SLO is unmeetable here, which is why the enforced gate was downgraded to
advisory and that downgrade recorded as a result). Where TLang *does* separate on
this box is **resource efficiency**: ~2.75 MiB peak RSS (≈50× under Bun, ≈120× under
Node) and ~2× lower CPU-seconds per request, consistently across every route and
repeat. The p50 medians are a microsecond-scale three-way tie with no fixed winner.

The reproducible-image + single-generator + byte-identical-payload + per-request-work
+ rotated-repeats + `/proc`-RSS/CPU methodology is sound and portable; the resource
numbers are durable; the latency tails and the sustained-rate ceiling need isolated,
pinned hardware (with a generator that out-cores the server) before any of them are
quoted as a runtime comparison.
