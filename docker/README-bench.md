# TLang comparative-benchmark image

A single reproducible container image that runs the comparative HTTP benchmark
`TestBenchmarkCompare` — **TLang vs Node.js vs Bun** — on any machine with a
container engine. It extends the [dev image](./README.md) with the two runtimes
the benchmark pits TLang against, every version pinned by digest.

Defined by [`Dockerfile.bench`](./Dockerfile.bench).

## What it adds over the dev image

The dev image deliberately omits Node and Bun. This image carries everything the
benchmark needs, with nothing left to the host:

- **Go 1.26** + **clang** — builds the TLang `echo_server.tl` fixture on the
  `clang-release` leg (the TLang leg of the benchmark).
- **gcc / clang / link-capable tcc** — the full compiler matrix, built exactly
  like the dev image (tcc 0.9.28rc from pinned upstream source; see
  [`README.md`](./README.md) for the rationale).
- **Node.js 24.21.0** (Active LTS "Krypton") — satisfies the benchmark's
  **Node ≥ 23.1** floor (the N-process `SO_REUSEPORT` model uses the `reusePort`
  listen option added in 23.1). Installed from the official nodejs.org tarball,
  SHA-256 pinned.
- **Bun 1.4.2** — installed from the official GitHub release zip, SHA-256
  pinned.
- **taskset** (`util-linux`) — for the disjoint-core CPU pinning the enforced
  p99 gate needs.

The build fails loudly if any runtime is missing or below its floor: a final
build step asserts `node --version >= 23.1` and prints `go`/`clang`/`tcc`/`node`/
`bun` versions.

## Reproducibility pins

| Component | Version | Pin |
| :--- | :--- | :--- |
| Base image | `golang:1.26-trixie` | digest `sha256:af00f232…9c882a` |
| tcc | 0.9.28rc | repo `repo.or.cz/tinycc.git` @ `43c7708b…9b2` |
| Node.js | 24.21.0 | linux-x64 `sha256:fd8e59d5…cb2d6`, arm64 `sha256:6ad1325e…89ad2` |
| Bun | 1.4.2 | linux-x64 `sha256:36368fae…2a913`, aarch64 `sha256:54328bbc…c8fda7` |

All are exposed as `ARG`s at the top of their build stage, so a future bump is a
one-line change plus the matching checksum. x64 and arm64 are both supported;
the x64 digests are the benchmark reference.

## Build

```sh
docker build -f docker/Dockerfile.bench -t tlang-bench:latest .
# or podman:
podman build -f docker/Dockerfile.bench -t tlang-bench:latest .
```

## Run the comparison

Mount the repo at `/src` (add `:Z` on podman/SELinux hosts). The entrypoint runs
`TestBenchmarkCompare` by default and honors every `TLANG_BENCH_*` knob from
[`tests/bench/README.md`](../tests/bench/README.md).

The default run benchmarks **three routes per leg** — the trivial `/bench`
baseline plus the two real-work routes `/json` (716 B nested object) and `/work`
(1407 B array of built-string rows) — and prints a per-route comparison table.
The real-work routes exist to surface where TLang's native execution separates
from Node's and Bun's GC/JIT runtimes on allocation + serialization work; see
[`tests/bench/README.md`](../tests/bench/README.md) for what each route does. No
extra flag is needed: the default entrypoint command already exercises both the
trivial and the real-work routes.

To run a single route, pass `TLANG_BENCH_PATH` (valid values `/bench`, `/json`,
`/work`; an unknown value fails the test):

```sh
docker run --rm -v "$PWD:/src" \
  -e TLANG_BENCH_PATH=/work \
  tlang-bench:latest
```

### Quick smoke run (unpinned, advisory gate)

```sh
docker run --rm -v "$PWD:/src" \
  -e TLANG_BENCH_RATE=2000 \
  -e TLANG_BENCH_WARMUP=1s \
  -e TLANG_BENCH_DURATION=2s \
  tlang-bench:latest
```

### Full pinned run (enforced p99 gate for the TLang leg)

The gate is enforced only under **disjoint** CPU pinning: the generator on one
core set, the server child on a disjoint set. Set `BENCH_GEN_CORES` (generator)
and `TLANG_BENCH_SERVER_CORES` (server) to disjoint lists, and give the
container access to those CPUs:

```sh
docker run --rm -v "$PWD:/src" \
  --cpuset-cpus 0-5 \
  -e BENCH_GEN_CORES=0-1 \
  -e TLANG_BENCH_SERVER_CORES=2-5 \
  tlang-bench:latest
```

`BENCH_GEN_CORES` wraps the `go test` in `taskset -c`; `TLANG_BENCH_SERVER_CORES`
is read by the Go harness to pin each server child. Keep the two sets disjoint
and inside `--cpuset-cpus`, or the pinning is meaningless. On a host that cannot
pin (no `taskset`, or cores not isolated) the run still works with an advisory
gate for every leg.

### Re-measurement layout for this box (AMD Ryzen 5 5600, Docker Desktop)

The methodologically-sound re-measurement targets this machine: 6 physical
cores / 12 SMT threads, where the LinuxKit VM pairs SMT siblings consecutively
(`(0,1),(2,3),…,(10,11)`). Give the container all 12 lanes and split them so the
server and generator never share a physical core:

```sh
docker run --rm -v "$PWD:/src" \
  --cpuset-cpus 0-11 \
  -e TLANG_BENCH_SERVER_CORES=0,2,4,6 \
  -e BENCH_GEN_CORES=8,10 \
  tlang-bench:latest
```

- **Server cores `0,2,4,6`** — four *distinct physical* cores, so the harness
  derives `TLANG_THREADS=4` (one scheduler thread + `SO_REUSEPORT` listener per
  core). **Never set `TLANG_THREADS` yourself** — letting the harness derive it
  from the server set is what avoids the Xeon over-subscription bug.
- **Generator cores `8,10`** — two distinct physical cores for the load
  generator (and the `/proc` resource sampler, which rides the generator lanes).
- `--cpuset-cpus 0-11` must contain both sets. Disjointness here is at the
  **container-topology** level only; Docker Desktop does not guarantee a
  container-lane → host-lane mapping, so this is documented as pinned at the VM
  level, not bare-metal.

### Repeated, rotated-order batch (the real measurement)

The headline numbers come from the branch-(b) batch, which repeats the whole
comparison with rotated leg/route order and aggregates median + range. Set
`BENCH_REPEATS=6` (the balanced 6-permutation set, each leg in each position
twice) and `BENCH_ROTATE=1`:

```sh
docker run --rm -v "$PWD:/src" \
  --cpuset-cpus 0-11 \
  -e TLANG_BENCH_SERVER_CORES=0,2,4,6 \
  -e BENCH_GEN_CORES=8,10 \
  -e BENCH_REPEATS=6 \
  -e BENCH_ROTATE=1 \
  -e TLANG_BENCH_RATE_LADDER=20000,40000,60000,80000,100000,120000 \
  tlang-bench:latest
```

What the batch does (design §C.7):

1. Truncates the JSONL summary (`tests/bench/.sweep-summary.jsonl`) so the batch
   starts clean.
2. Scopes `TLANG_BENCH_PERREQ=1` and the rate ladder inside the batch (branch a
   stays byte-identical — it exports none of these).
3. Runs **two sub-passes**, each JSONL line tagged with its `pass`:
   - **ladder pass** with `TLANG_BENCH_RESOURCE=0` — honest max-sustained-rate
     ceiling, un-depressed by the sampler;
   - **fixed-rate resource pass** with `TLANG_BENCH_RESOURCE=1` (no ladder) —
     peak RSS and CPU-s/Mreq at the sub-saturation `TLANG_BENCH_RATE`.
4. Each repeat `i` exports the next of the balanced 6-permutation leg orders
   (`tlang,node,bun` / `node,bun,tlang` / `bun,tlang,node` / `tlang,bun,node` /
   `bun,node,tlang` / `node,tlang,bun`) walked modulo 6, plus the matching route
   order when `BENCH_ROTATE=1`, and runs the same `go test` wrapped in
   `taskset -c "$BENCH_GEN_CORES"` (not `exec`, so the loop continues). A failing
   repeat is recorded and the batch proceeds.
5. After both passes, runs the aggregator (`go run ./tests/bench/aggregate`) and
   prints the Markdown fragment for `tests/bench/RESULTS.md`.

A smaller `BENCH_REPEATS` (e.g. `2`) runs the first N rows of the permutation
table — handy for a smoke run, but it is a stated skew; use a multiple of 6 for
a balanced measurement. The full list of knobs is in
[`tests/bench/README.md`](../tests/bench/README.md).

### Run something else

Any arguments after the image name replace the default benchmark command:

```sh
# single-runtime TLang benchmark
docker run --rm -v "$PWD:/src" tlang-bench:latest \
  env TLANG_BENCH=1 go test -tags e2e -run 'TestBenchmark$' -v ./tests/

# a shell
docker run --rm -it -v "$PWD:/src" tlang-bench:latest bash
```

## Reading the output

Each leg prints a report block per route (`=== TLang / Node / Bun benchmark
report ===`) with CPU info, gate mode, pinning, `TLANG_THREADS`, load knobs
(C/R), the measured route's payload sizes, totals/failures, and latency
percentiles (p50/p90/p99/p99.9/max). A final per-route comparison table groups
rows by route (`/bench` first): a route header, then one row per leg (name,
version, p50/p99/max in ms, failures). See
[`tests/bench/README.md`](../tests/bench/README.md) for the fairness contract
and gate semantics.

Note that a real-work route (`/json`, `/work`) may legitimately trip the
**enforced** TLang p99 gate under pinning, since it does genuine per-request
work — a true signal, not a bug. For a clean measurement of the real-work
routes, run them advisory (unpinned) or with a raised threshold.

## Notes

- The benchmark is DB-free, so no PostgreSQL setup is needed for the comparison
  (the image still ships libpq/PostgreSQL 17 for parity with the dev image).
- `taskset`, the `/proc` affinity read, and therefore the enforced gate are
  Linux-only. On Docker Desktop (macOS/Windows) the Linux VM runs the container,
  so pinning works only to the extent the VM exposes isolated CPUs; expect the
  advisory gate unless you have pinned a Linux host.
- For a scientifically meaningful comparison, run on a quiet machine with the
  generator and server pinned to disjoint physical cores and enough warm-up.
