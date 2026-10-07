/*
 * test_slices.c - unit tests for src/slices.c (slice allocation, growth,
 * out-of-bounds, header init). Each check would fail if the implementation
 * were reverted.
 */
#include "tlang_internal.h"

#include <setjmp.h>
#include <stdio.h>
#include <stdlib.h>

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

/* The arena and allocation/abort primitives are provided by the library now
 * (arena.c / fiber.c). The test fiber is a real Fiber so that the OOM-abort
 * path (tlang_alloc_failed -> tlang_request_abort -> longjmp f->abort_jmp)
 * works; g_fib names its public view. */
static MemoryArena g_arena;
static bool g_arena_live;
static Fiber g_f;
#define g_fib (g_f.pub)

static void arena_free(void) {
    if (g_arena_live) {
        tlang_arena_destroy(&g_arena);
        g_arena_live = false;
    }
}

static void arena_setup(size_t cap) {
    (void)cap;
    arena_free();  /* free any arena from a previous test (no leaks under ASan) */
    if (tlang_arena_init(&g_arena) != 0) { perror("tlang_arena_init"); exit(2); }
    g_arena_live = true;
    memset(&g_f, 0, sizeof g_f);
    g_f.pub.arena = &g_arena;
}

/* ---- tests ---- */

/* Reading header fields the scalar-offset way, like generated code does
 * through its own tlang_slice_<m> type. */
static int64_t hdr_len(void* h) { int64_t v; memcpy(&v, (char*)h + TLANG_SLICE_OFF_LEN, sizeof v); return v; }
static int64_t hdr_cap(void* h) { int64_t v; memcpy(&v, (char*)h + TLANG_SLICE_OFF_CAP, sizeof v); return v; }
static void* hdr_items(void* h) { void* v; memcpy(&v, (char*)h + TLANG_SLICE_OFF_ITEMS, sizeof v); return v; }
static bool hdr_global(void* h) { bool v; memcpy(&v, (char*)h + TLANG_SLICE_OFF_GLOBAL, sizeof v); return v; }

static void test_hdr_init(void) {
    tlang_slice_i64 s;
    int64_t items[3];
    memset(&s, 0xAB, sizeof s);
    tlang_slice_hdr_init(&s, items, 2, 3, true);
    CHECK(s.items == items && s.len == 2 && s.cap == 3 && s.global == true);
    /* Reading the same bytes through a different slice type is well defined. */
    CHECK(hdr_items(&s) == items && hdr_len(&s) == 2 && hdr_cap(&s) == 3 && hdr_global(&s));
}

static void test_slice_new(void) {
    tlang_slice_i64* s;
    arena_setup(4096);
    s = (tlang_slice_i64*)tlang_slice_new(&g_fib, sizeof(tlang_slice_i64), false);
    CHECK(s->items == NULL && s->len == 0 && s->cap == 0 && !s->global);

    s = (tlang_slice_i64*)tlang_slice_new(&g_fib, sizeof(tlang_slice_i64), true);
    CHECK(s->items == NULL && s->len == 0 && s->cap == 0 && s->global);
    free(s);  /* global header is heap memory */
}

static void test_grow_arena(void) {
    tlang_slice_i64* s;
    int64_t i;
    arena_setup(1 << 20);
    s = (tlang_slice_i64*)tlang_slice_new(&g_fib, sizeof(tlang_slice_i64), false);

    /* First growth: max(min_cap, 2*cap, 4) with cap 0 -> at least 4. */
    s->items = tlang_slice_grow(&g_fib, s->items, s->len, &s->cap, 1, s->global, sizeof(int64_t));
    CHECK(s->cap >= 4);

    for (i = 0; i < 100; i++) {
        if (s->len >= s->cap) {
            s->items = tlang_slice_grow(&g_fib, s->items, s->len, &s->cap,
                                        s->len + 1, s->global, sizeof(int64_t));
        }
        s->items[s->len++] = i * i;
    }
    CHECK(s->len == 100);
    for (i = 0; i < 100; i++) CHECK(s->items[i] == i * i);

    /* Doubling policy: growing past the current cap at least doubles it. */
    {
        int64_t oldcap = s->cap;
        s->items = tlang_slice_grow(&g_fib, s->items, s->len, &s->cap,
                                    oldcap + 1, s->global, sizeof(int64_t));
        CHECK(s->cap >= 2 * oldcap);
    }

    /* No-op when min_cap already fits. */
    {
        int64_t cap = s->cap;
        void* items = s->items;
        void* r = tlang_slice_grow(&g_fib, s->items, s->len, &s->cap, 1, s->global, sizeof(int64_t));
        CHECK(r == items && s->cap == cap);
    }
}

static void test_grow_heap(void) {
    tlang_slice_i64* s;
    int64_t i;
    s = (tlang_slice_i64*)tlang_slice_new(&g_fib, sizeof(tlang_slice_i64), true);
    for (i = 0; i < 50; i++) {
        if (s->len >= s->cap) {
            s->items = tlang_slice_grow(&g_fib, s->items, s->len, &s->cap,
                                        s->len + 1, s->global, sizeof(int64_t));
        }
        s->items[s->len++] = i;
    }
    CHECK(s->len == 50);
    for (i = 0; i < 50; i++) CHECK(s->items[i] == i);
    free(s->items);
    free(s);
}

static void test_grow_overflow(void) {
    int64_t cap = 0;
    arena_setup(4096);
    /* A byte size that overflows size_t is treated as OOM (abort). The abort
     * longjmps to the armed TLANG_ABORT_POINT via the real request-abort path. */
    g_f.abort_armed = true;
    if (TLANG_ABORT_POINT(&g_f) == 0) {
        tlang_slice_grow(&g_fib, NULL, 0, &cap, INT64_MAX, false, sizeof(int64_t));
        CHECK(0 && "expected abort on element-count overflow");
    } else {
        CHECK(g_f.abort_status == TLANG_STATUS_UNAVAILABLE);  /* 503 OOM */
    }
    g_f.abort_armed = false;
}

static void test_oob(void) {
    void* scratch;
    arena_setup(4096);
    g_fib.err = 0;
    scratch = tlang_slice_oob(&g_fib, 5, 3, sizeof(int64_t));
    CHECK(scratch != NULL);
    CHECK(g_fib.err && g_fib.error.status == TLANG_STATUS_INTERNAL);
    /* The scratch block is zeroed so a read yields the zero value. */
    {
        int64_t v;
        memcpy(&v, scratch, sizeof v);
        CHECK(v == 0);
    }
}

static void test_slice_at(void) {
    tlang_slice_i64* s;
    arena_setup(4096);
    s = (tlang_slice_i64*)tlang_slice_new(&g_fib, sizeof(tlang_slice_i64), false);
    s->items = tlang_slice_grow(&g_fib, s->items, s->len, &s->cap, 3, s->global, sizeof(int64_t));
    s->len = 3;
    s->items[0] = 10; s->items[1] = 20; s->items[2] = 30;

    g_fib.err = 0;
    CHECK(TLANG_SLICE_AT(&g_fib, s, 1, int64_t) == 20 && !g_fib.err);
    /* Out of range throws and returns a scratch zero. */
    g_fib.err = 0;
    CHECK(TLANG_SLICE_AT(&g_fib, s, 7, int64_t) == 0 && g_fib.err);
}

int main(void) {
    test_hdr_init();
    test_slice_new();
    test_grow_arena();
    test_grow_heap();
    test_grow_overflow();
    test_oob();
    test_slice_at();
    arena_free();  /* release the stand-in arena so ASan sees no leak */

    if (failures != 0) {
        printf("FAIL test_slices (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_slices\n");
    return 0;
}
