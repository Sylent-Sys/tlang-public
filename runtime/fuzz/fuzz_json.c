/*
 * fuzz_json.c - libFuzzer harness for the locked JSON scanner
 * (runtime/src/json.c). Developer opt-in; NOT built or run by the Go suite.
 *
 * It links the real json.c on the command line (it does NOT #include it) and
 * drives every scanner entry point over the fuzzer-supplied bytes:
 *   json_skip_value, json_scan_string, json_scan_key, json_scan_int64,
 *   json_scan_int32, json_scan_float64, json_scan_bool, json_scan_null.
 *
 * Invariant checked by ASan/UBSan: no crash, no out-of-bounds read, and
 * bounded time (the depth limit bounds recursion; `size` bounds iteration).
 * A crash here is a real bug in the locked runtime/src/json.c -> STOP and
 * raise (design §5.4/§8); the harness never works around it.
 *
 * Build/run (inside the tlang-dev container) - see runtime/fuzz/README.md.
 *
 * A minimal arena/fiber context is set up exactly as runtime/tests/test_json.c
 * does: json.c's scanners only need a live arena (json_scan_string allocates
 * through arena_alloc and reports failure by returning false, never aborting),
 * so a zeroed tlang_fiber over a real arena is sufficient. No other runtime
 * stand-in is required because the harness links json.c's real transitive
 * dependencies (arena.c, strings.c, errors.c).
 */
#include "tlang_internal.h"

#include <stddef.h>
#include <stdint.h>

/* ------------------------------------------------------------------ *
 * Link-only stand-ins
 *
 * The documented minimal link set (json.c strings.c arena.c errors.c) leaves
 * two references unresolved: arena.c's out-of-memory handler tlang_alloc_failed
 * calls tlang_log_error (console.c) and tlang_request_abort (fiber.c), neither
 * of which is on the link line. The JSON scanners never hit that path - they
 * report an allocation failure by returning false and never call
 * tlang_alloc_failed, and the harness drives only the scanners (not the
 * tlang_buf writers) - so these stand-ins only need to exist to link. Defining
 * them locally keeps the link set minimal, exactly as design §5.4 allows
 * ("defining any missing stand-ins locally"). If one is ever reached it traps
 * loudly rather than masking a fault.
 * ------------------------------------------------------------------ */

void tlang_log_error(const char* fmt, ...) {
    (void)fmt;
}

_Noreturn void tlang_request_abort(Fiber* f, int32_t status, const char* reason) {
    (void)f; (void)status; (void)reason;
    __builtin_trap();
}

/* One arena/fiber reused across inputs; reset per call so memory stays bounded
 * over a long fuzzing session. Initialised lazily on the first input. */
static MemoryArena g_arena;
static tlang_fiber g_fib;
static int g_ready;

static void ensure_ctx(void) {
    if (g_ready) {
        arena_reset(&g_arena);
        return;
    }
    if (tlang_arena_init(&g_arena) != 0) {
        /* Allocation failure in the harness itself: nothing to fuzz. */
        __builtin_trap();
    }
    memset(&g_fib, 0, sizeof g_fib);
    g_fib.arena = &g_arena;
    g_ready = 1;
}

int LLVMFuzzerTestOneInput(const uint8_t* data, size_t size) {
    ensure_ctx();

    /* Pin the depth limit to the default so recursion in json_skip_value is
     * bounded regardless of any prior iteration's setting. */
    json_set_max_depth(32);

    const char* begin = (const char*)data;
    const char* end = begin + size;

    /* 1) Whole-value skip (containers, strings, numbers, literals; the
     *    depth-bounded recursive path). */
    {
        const char* p = begin;
        (void)json_skip_value(&p, end, 0);
    }

    /* 2) Each leaf scanner from the start of the input, after skipping leading
     *    whitespace exactly as the generated parsers do. Independent cursors so
     *    one scanner's advance never masks another's bounds handling. */
    {
        const char* p = json_skip_ws(begin, end);

        {
            const char* q = p;
            tlang_string s;
            (void)json_scan_string(&q, end, &g_arena, &s);
        }
        {
            const char* q = p;
            tlang_string k;
            (void)json_scan_key(&q, end, &k);
        }
        {
            const char* q = p;
            int64_t v;
            (void)json_scan_int64(&q, end, &v);
        }
        {
            const char* q = p;
            int32_t v;
            (void)json_scan_int32(&q, end, &v);
        }
        {
            const char* q = p;
            double v;
            (void)json_scan_float64(&q, end, &v);
        }
        {
            const char* q = p;
            bool b;
            (void)json_scan_bool(&q, end, &b);
        }
        {
            const char* q = p;
            (void)json_scan_null(&q, end);
        }
    }

    return 0;
}
