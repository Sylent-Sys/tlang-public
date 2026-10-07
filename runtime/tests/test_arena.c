/*
 * test_arena.c - unit tests for src/arena.c: chunk growth, overflow-safe sizing,
 * reset lifetimes, the global allocator, and the OOM-abort path (an allocation
 * that fails inside an armed TLANG_ABORT_POINT longjmps out and runs cleanups).
 * Each check exercises real behavior and would fail if the code were reverted.
 */
#include "tlang_internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

static void test_init_destroy(void) {
    MemoryArena a;
    CHECK(tlang_arena_init(&a) == 0);
    CHECK(a.first != NULL && a.current == a.first);
    CHECK(a.first->capacity == (size_t)ARENA_CHUNK_SIZE);
    CHECK(a.first->offset == 0 && a.first->next == NULL);
    tlang_arena_destroy(&a);
    CHECK(a.first == NULL && a.current == NULL);
    /* Safe on a {NULL, NULL} arena. */
    tlang_arena_destroy(&a);
}

static void test_alloc_within_first_chunk(void) {
    MemoryArena a;
    void* p1;
    void* p2;
    CHECK(tlang_arena_init(&a) == 0);
    p1 = arena_alloc(&a, 1);
    p2 = arena_alloc(&a, 1);
    CHECK(p1 != NULL && p2 != NULL);
    /* 8-byte aligned and spaced (ARENA_ALIGNMENT rounding). */
    CHECK(((uintptr_t)p1 % ARENA_ALIGNMENT) == 0);
    CHECK((char*)p2 - (char*)p1 == ARENA_ALIGNMENT);
    CHECK(a.current == a.first);  /* no growth */
    tlang_arena_destroy(&a);
}

static void test_grow_appends_chunk(void) {
    MemoryArena a;
    void* big;
    ArenaChunk* first;
    CHECK(tlang_arena_init(&a) == 0);
    first = a.first;
    /* An allocation larger than the first chunk forces a new chunk whose
     * capacity is at least the request. */
    big = arena_alloc(&a, (size_t)ARENA_CHUNK_SIZE + 4096);
    CHECK(big != NULL);
    CHECK(a.current != first && a.first == first);       /* appended after first */
    CHECK(first->next == a.current);                      /* linked in order */
    CHECK(a.current->capacity >= (size_t)ARENA_CHUNK_SIZE + 4096);
    tlang_arena_destroy(&a);
}

static void test_grow_overflow_safe(void) {
    MemoryArena a;
    ArenaChunk* before_first;
    ArenaChunk* before_current;
    CHECK(tlang_arena_init(&a) == 0);
    before_first = a.first;
    before_current = a.current;
    /* n > SIZE_MAX - sizeof(ArenaChunk): arena_grow returns NULL, arena
     * unchanged. (decision 18 / RUNTIME.md §6) */
    CHECK(arena_grow(&a, SIZE_MAX - 8) == NULL);
    CHECK(a.first == before_first && a.current == before_current);
    /* arena_alloc of an overflowing rounded size returns NULL too. */
    CHECK(arena_alloc(&a, SIZE_MAX) == NULL);
    tlang_arena_destroy(&a);
}

static void test_reset(void) {
    MemoryArena a;
    void* p_first;
    ArenaChunk* first;
    CHECK(tlang_arena_init(&a) == 0);
    first = a.first;
    p_first = arena_alloc(&a, 16);
    CHECK(p_first != NULL);
    /* Force several extra chunks. */
    (void)arena_alloc(&a, (size_t)ARENA_CHUNK_SIZE);
    (void)arena_alloc(&a, (size_t)ARENA_CHUNK_SIZE);
    CHECK(a.first->next != NULL);
    arena_reset(&a);
    CHECK(a.current == first && a.first == first);
    CHECK(first->next == NULL && first->offset == 0);
    /* After reset the first allocation reuses the first chunk's start. */
    CHECK(arena_alloc(&a, 16) == p_first);
    tlang_arena_destroy(&a);
}

static void test_global_zeroed(void) {
    unsigned char* p = (unsigned char*)tlang_alloc_global_zeroed(NULL, 64);
    int i;
    int all_zero = 1;
    CHECK(p != NULL);
    CHECK(((uintptr_t)p % 16) == 0);  /* at least 16-byte aligned */
    for (i = 0; i < 64; i++) if (p[i] != 0) all_zero = 0;
    CHECK(all_zero);
    free(p);
    /* size 0 still returns a usable (freeable) pointer. */
    p = (unsigned char*)tlang_alloc_global_zeroed(NULL, 0);
    CHECK(p != NULL);
    free(p);
}

/* ---- OOM-abort end to end ---- */

static int g_cleanup_ran;
static void cleanup_marker(Fiber* f, void* arg) {
    (void)f;
    (void)arg;
    g_cleanup_ran++;
}

static void test_oom_abort_runs_cleanups(void) {
    /* A real Fiber with an armed abort point. An arena allocation that fails
     * (overflowing size) must longjmp to the abort point and the loop then
     * runs the cleanup hooks. */
    Fiber f;
    MemoryArena a;
    tlang_cleanup node;
    volatile int reached_abort = 0;

    memset(&f, 0, sizeof f);
    CHECK(tlang_arena_init(&a) == 0);
    f.pub.arena = &a;
    f.cleanups = NULL;

    node.fn = cleanup_marker;
    node.arg = NULL;
    node.next = NULL;
    tlang_cleanup_push(&f, &node);
    g_cleanup_ran = 0;

    f.abort_armed = true;
    if (TLANG_ABORT_POINT(&f) == 0) {
        /* Overflowing allocation -> arena_alloc returns NULL ->
         * tlang_alloc_failed -> tlang_request_abort -> longjmp here. */
        (void)tlang_alloc_raw(&f.pub, SIZE_MAX);
        CHECK(0 && "expected OOM abort to longjmp");
    } else {
        reached_abort = 1;
        f.abort_armed = false;
        CHECK(f.abort_status == TLANG_STATUS_UNAVAILABLE);  /* 503 */
        tlang_cleanup_run_all(&f);
    }
    CHECK(reached_abort);
    CHECK(g_cleanup_ran == 1);
    CHECK(f.cleanups == NULL);  /* hooks drained */
    tlang_arena_destroy(&a);
}

int main(void) {
    test_init_destroy();
    test_alloc_within_first_chunk();
    test_grow_appends_chunk();
    test_grow_overflow_safe();
    test_reset();
    test_global_zeroed();
    test_oom_abort_runs_cleanups();

    if (failures != 0) {
        printf("FAIL test_arena (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_arena\n");
    return 0;
}
