#!/usr/bin/env bash
# Benchmark entrypoint for the tlang-bench image.
#
# With no arguments it runs the comparative benchmark (TestBenchmarkCompare:
# TLang vs Node vs Bun) over the repo mounted at /src. Any arguments are run
# verbatim instead, so the image can also run the single-runtime TestBenchmark
# or an arbitrary command.
#
# Two mutually-exclusive branches (design §C.7):
#   (a) Default single run. BENCH_REPEATS unset/1 AND BENCH_ROTATE unset/0.
#       Byte-identical to the historical entrypoint: build the same `go test`
#       cmd and `exec` it (optionally wrapped in the generator-side taskset).
#       Exports NONE of the new TLANG_BENCH_* task knobs — the operator supplies
#       them via `docker run -e …` exactly as tests/bench/README.md documents.
#   (b) Repeat/rotate batch. BENCH_REPEATS>1 OR BENCH_ROTATE=1. Cannot `exec`
#       (that would replace the shell and stop the loop). Truncates the JSONL
#       summary, scopes TLANG_BENCH_PERREQ=1 + the rate ladder INSIDE this
#       branch, runs two sub-passes (a ladder pass with TLANG_BENCH_RESOURCE=0,
#       then a fixed-rate resource pass with TLANG_BENCH_RESOURCE=1), loops the
#       balanced 6-permutation leg order (and matching route order when
#       BENCH_ROTATE=1) modulo 6, wraps each iteration in `taskset` (not
#       `exec`), records a failing repeat and continues, and runs the aggregator
#       at the end.
#
# HARD RULE: neither branch ever exports TLANG_THREADS. benchThreads derives it
# (4 = len(server set) under the 0,2,4,6 pinning); exporting it reintroduces the
# Xeon over-subscription bug (design §D2).
#
# Pinning: when BENCH_GEN_CORES is set, the whole `go test` is wrapped in
# `taskset -c <BENCH_GEN_CORES>` and TLANG_BENCH_SERVER_CORES must be a disjoint
# set so the p99 gate is enforced (for the TLang leg). Both are passed through
# from the environment; this script only wires the generator-side taskset.
#
# All TLANG_BENCH_* knobs documented in tests/bench/README.md are honored by
# being inherited from the container environment.
set -euo pipefail

# Escape hatch: run whatever was passed instead of the default benchmark.
if [ "$#" -gt 0 ]; then
  exec "$@"
fi

# Default the opt-in gate on (this image exists to run the comparison).
export TLANG_BENCH_COMPARE="${TLANG_BENCH_COMPARE:-1}"

# Record the pinned runtime versions in the log for reproducibility.
echo "=== tlang-bench runtimes ==="
echo "go:   $(go version)"
echo "node: $(node --version)"
echo "bun:  $(bun --version)"
command -v clang >/dev/null && echo "clang: $(clang --version | head -n1)"
echo "============================"

cmd=(go test -tags e2e -run 'TestBenchmarkCompare$' -v -count=1 -timeout 30m ./tests/)

BENCH_REPEATS="${BENCH_REPEATS:-1}"
BENCH_ROTATE="${BENCH_ROTATE:-0}"

# ----------------------------------------------------------------------------
# Branch (a) — default, byte-identical single run.
# ----------------------------------------------------------------------------
if [ "${BENCH_REPEATS}" = "1" ] && [ "${BENCH_ROTATE}" != "1" ]; then
  # Generator-side CPU pinning (optional). The server-side pinning is handled by
  # the Go harness via TLANG_BENCH_SERVER_CORES (also passed through the env).
  if [ -n "${BENCH_GEN_CORES:-}" ]; then
    if ! command -v taskset >/dev/null 2>&1; then
      echo "BENCH_GEN_CORES set but taskset not found" >&2
      exit 1
    fi
    echo "Pinning generator to cores: ${BENCH_GEN_CORES} (server cores: ${TLANG_BENCH_SERVER_CORES:-unset})"
    exec taskset -c "${BENCH_GEN_CORES}" "${cmd[@]}"
  fi

  exec "${cmd[@]}"
fi

# ----------------------------------------------------------------------------
# Branch (b) — repeat/rotate batch.
#
# NOTE: all TLANG_BENCH_* task-knob exports below are scoped to this branch so
# branch (a) stays byte-identical. We NEVER export TLANG_THREADS here.
# ----------------------------------------------------------------------------
if ! command -v taskset >/dev/null 2>&1; then
  echo "Branch (b) repeat/rotate requires taskset for generator pinning" >&2
  exit 1
fi
if [ -z "${BENCH_GEN_CORES:-}" ]; then
  echo "Branch (b) repeat/rotate requires BENCH_GEN_CORES (generator cores)" >&2
  exit 1
fi

# (1) JSONL summary target, truncated once so the batch starts clean.
export TLANG_BENCH_SUMMARY_OUT="${TLANG_BENCH_SUMMARY_OUT:-/src/tests/bench/.sweep-summary.jsonl}"
: > "${TLANG_BENCH_SUMMARY_OUT}"

# (2) Task knobs scoped to branch (b): the per-request-work fix (D1) and the
# rate ladder (Mechanism A). TLANG_BENCH_RATE_LADDER is passed through from the
# environment when the operator sets it; defaulted here only so the ladder pass
# has something to climb if unset.
export TLANG_BENCH_PERREQ=1
export TLANG_BENCH_RATE_LADDER="${TLANG_BENCH_RATE_LADDER:-20000,40000,60000,80000,100000,120000}"

# Balanced 6-permutation leg-order sequence (design §C.3). Walked modulo 6, so
# BENCH_REPEATS=6 runs each ordering exactly once; other values take the first
# BENCH_REPEATS rows (a stated skew the aggregator surfaces).
leg_orders=(
  "tlang,node,bun"
  "node,bun,tlang"
  "bun,tlang,node"
  "tlang,bun,node"
  "bun,node,tlang"
  "node,tlang,bun"
)
# Matching route-order rotation (design §C.4), used only when BENCH_ROTATE=1.
route_orders=(
  "/bench,/json,/work"
  "/json,/work,/bench"
  "/work,/bench,/json"
  "/bench,/work,/json"
  "/work,/json,/bench"
  "/json,/bench,/work"
)

failures=0

# run_pass <pass-label> : run the whole repeat loop once for one sub-pass.
# TLANG_BENCH_RESOURCE must already be exported by the caller.
run_pass() {
  pass_label="$1"
  echo "=== repeat/rotate ${pass_label} pass (RESOURCE=${TLANG_BENCH_RESOURCE}, repeats=${BENCH_REPEATS}, rotate=${BENCH_ROTATE}) ==="
  i=1
  while [ "${i}" -le "${BENCH_REPEATS}" ]; do
    idx=$(( (i - 1) % 6 ))
    export TLANG_BENCH_LEG_ORDER="${leg_orders[$idx]}"
    if [ "${BENCH_ROTATE}" = "1" ]; then
      export TLANG_BENCH_ROUTE_ORDER="${route_orders[$idx]}"
    else
      unset TLANG_BENCH_ROUTE_ORDER || true
    fi
    echo "--- ${pass_label} repeat ${i}/${BENCH_REPEATS}: legs=${TLANG_BENCH_LEG_ORDER} routes=${TLANG_BENCH_ROUTE_ORDER:-default} ---"
    # Per-iteration generator pin (NOT exec, so the loop can continue). A
    # failing repeat is recorded and the batch proceeds (design §C.8).
    if taskset -c "${BENCH_GEN_CORES}" "${cmd[@]}"; then
      :
    else
      rc=$?
      echo "!!! ${pass_label} repeat ${i}/${BENCH_REPEATS} FAILED (exit ${rc}); continuing batch" >&2
      failures=$(( failures + 1 ))
    fi
    i=$(( i + 1 ))
  done
}

# (3) Two sub-passes, each tagged via the harness `pass` field:
#   - ladder pass: resource sampler OFF so the generator ceiling is honest.
#   - fixed-rate resource pass: no ladder, sampler ON at the sub-saturation rate.
export TLANG_BENCH_RESOURCE=0
run_pass "ladder"

ladder_saved="${TLANG_BENCH_RATE_LADDER}"
unset TLANG_BENCH_RATE_LADDER
export TLANG_BENCH_RESOURCE=1
run_pass "fixed-rate resource"
export TLANG_BENCH_RATE_LADDER="${ladder_saved}"

# (4) Aggregate the JSONL into the Markdown fragment for RESULTS.md.
echo "=== aggregating ${TLANG_BENCH_SUMMARY_OUT} ==="
go run ./tests/bench/aggregate -in "${TLANG_BENCH_SUMMARY_OUT}"

if [ "${failures}" -ne 0 ]; then
  echo "repeat/rotate batch finished with ${failures} failed repeat(s); see log above" >&2
  exit 1
fi
