/*
 * test_ctxswitch.c - unit tests for src/ctxswitch.c. Verifies that a switch
 * into a fresh context runs entry(arg), that control round-trips back to the
 * loop, that a context can be re-initialised and run again (pooled reuse), and
 * that callee-saved registers survive a switch (the fiber mutates state across
 * a switch-out/switch-in). Each check would fail if the switch were reverted.
 */
#include "tlang_internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

/* The scheduler loop's context and the fiber's context. */
static FiberContext g_loop;
static FiberContext g_fiber;

static int g_entry_ran;
static void* g_entry_arg;
static int g_resume_count;

/* Completes the ASan fiber-switch at the top of a fresh run (ctxswitch.c;
 * a no-op without ASan). A hand-written entry must call it, exactly as the
 * fiber trampoline does. */
void tlang_fctx_finish_switch(FiberContext* self);

/* A fiber entry that: records that it ran and its argument, switches back to
 * the loop (first suspension), then after being resumed increments a counter,
 * switches back again, and finally exits for good. Never returns. */
static void entry_roundtrip(void* arg) {
    tlang_fctx_finish_switch(&g_fiber);
    g_entry_ran++;
    g_entry_arg = arg;

    /* First suspension: back to the loop. */
    tlang_fctx_switch(&g_fiber, &g_loop);

    /* Resumed: do work that must survive the earlier switch-out. */
    g_resume_count++;

    /* Exit the run for good (never resumed again). */
    tlang_fctx_exit(&g_fiber, &g_loop);
}

static int g_sum_entry_ran;
/* An entry that computes a value using many locals (exercising callee-saved
 * registers) across a switch, then exits. */
static void entry_sum(void* arg) {
    volatile long base;
    long a, b, c, d, e;
    tlang_fctx_finish_switch(&g_fiber);
    base = (long)(intptr_t)arg;
    a = base + 1; b = base + 2; c = base + 3; d = base + 4; e = base + 5;
    /* Suspend mid-computation. */
    tlang_fctx_switch(&g_fiber, &g_loop);
    /* After resume, the locals must still hold their values. */
    g_sum_entry_ran = (int)(a + b + c + d + e);
    tlang_fctx_exit(&g_fiber, &g_loop);
}

static void* alloc_stack(size_t* out_size) {
    size_t page = (size_t)sysconf(_SC_PAGESIZE);
    size_t size = 256 * 1024;
    void* base;
    if (page == 0) page = 4096;
    base = mmap(NULL, size + page, PROT_READ | PROT_WRITE,
                MAP_PRIVATE | MAP_ANONYMOUS | MAP_STACK, -1, 0);
    if (base == MAP_FAILED) { perror("mmap"); exit(2); }
    mprotect(base, page, PROT_NONE);
    *out_size = size + page;
    /* usable stack starts above the guard page */
    return (char*)base + page;
}

static void test_switch_roundtrip(void) {
    size_t map_size;
    void* stack_lo = alloc_stack(&map_size);
    void* base = (char*)stack_lo - (size_t)sysconf(_SC_PAGESIZE);

    tlang_fctx_init_thread(&g_loop);
    g_entry_ran = 0;
    g_entry_arg = NULL;
    g_resume_count = 0;

    tlang_fctx_init(&g_fiber, stack_lo, 256 * 1024, entry_roundtrip, (void*)(intptr_t)0x1234);

    /* First switch: runs entry up to its first suspension. */
    tlang_fctx_switch(&g_loop, &g_fiber);
    CHECK(g_entry_ran == 1);
    CHECK(g_entry_arg == (void*)(intptr_t)0x1234);
    CHECK(g_resume_count == 0);

    /* Resume: entry does its post-suspension work and exits. */
    tlang_fctx_switch(&g_loop, &g_fiber);
    CHECK(g_resume_count == 1);

    munmap(base, map_size);
}

static void test_reuse_context(void) {
    size_t map_size;
    void* stack_lo = alloc_stack(&map_size);
    void* base = (char*)stack_lo - (size_t)sysconf(_SC_PAGESIZE);
    int i;

    tlang_fctx_init_thread(&g_loop);

    /* Re-initialise and run the same context several times (pooled reuse). */
    for (i = 0; i < 3; i++) {
        g_entry_ran = 0;
        g_resume_count = 0;
        tlang_fctx_init(&g_fiber, stack_lo, 256 * 1024, entry_roundtrip,
                        (void*)(intptr_t)(0x100 + i));
        tlang_fctx_switch(&g_loop, &g_fiber);   /* run to first suspend */
        CHECK(g_entry_ran == 1);
        CHECK(g_entry_arg == (void*)(intptr_t)(0x100 + i));
        tlang_fctx_switch(&g_loop, &g_fiber);   /* resume and exit */
        CHECK(g_resume_count == 1);
    }

    munmap(base, map_size);
}

static void test_callee_saved_survive(void) {
    size_t map_size;
    void* stack_lo = alloc_stack(&map_size);
    void* base = (char*)stack_lo - (size_t)sysconf(_SC_PAGESIZE);

    tlang_fctx_init_thread(&g_loop);
    g_sum_entry_ran = 0;
    tlang_fctx_init(&g_fiber, stack_lo, 256 * 1024, entry_sum, (void*)(intptr_t)10);
    tlang_fctx_switch(&g_loop, &g_fiber);  /* run to suspend */
    tlang_fctx_switch(&g_loop, &g_fiber);  /* resume: locals must survive */
    /* base=10 -> (11+12+13+14+15) = 65 */
    CHECK(g_sum_entry_ran == 65);

    munmap(base, map_size);
}

int main(void) {
    test_switch_roundtrip();
    test_reuse_context();
    test_callee_saved_survive();

    if (failures != 0) {
        printf("FAIL test_ctxswitch (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_ctxswitch\n");
    return 0;
}
