/*
 * test_fiber.c - unit tests for src/fiber.c: acquire/release pooling, the mmap
 * stack with its PROT_NONE guard page, the spawn trampoline (fn -> cleanups ->
 * release -> exit to loop), cleanup push/pop/run_all ordering, and the
 * request-abort path (armed longjmp vs. fatal exit). A scheduler is created so
 * the fibers have a real owner; fibers are driven with tlang_sched_run.
 */
#include "tlang_internal.h"

#include <setjmp.h>
#include <signal.h>
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

static tlang_config g_cfg;
static tlang_sched g_sched;

static void sched_up(void) {
    char err[128];
    static tlang_program prog;  /* no entry needed for these unit tests */
    tlang_config_defaults(&g_cfg);
    g_cfg.max_fibers = 4;        /* small pool to test exhaustion */
    g_cfg.stack_size = 256 * 1024;
    prog.init_globals = NULL;
    prog.globals_size = 0;
    prog.dispatcher = NULL;
    prog.main = NULL;
    prog.uses_db = false;
    if (tlang_sched_init(&g_sched, 0, &g_cfg, &prog, -1, err, sizeof err) != 0) {
        fprintf(stderr, "sched init failed: %s\n", err);
        exit(2);
    }
    tlang_fctx_init_thread(&g_sched.loop_ctx);
}

static void sched_down(void) {
    tlang_sched_destroy(&g_sched);
}

/* ---- acquire / release pooling ---- */
static void test_acquire_release_pool(void) {
    Fiber* a;
    Fiber* b;
    sched_up();

    a = tlang_fiber_acquire(&g_sched);
    CHECK(a != NULL);
    CHECK(a->pub.err == 0 && a->pub.arena == &a->arena);
    CHECK(a->pub.globals == g_sched.globals);
    CHECK(a->waiting_fd == -1 && a->waiting_fd2 == -1 && a->watch_fd == -1);
    CHECK(a->cleanups == NULL && !a->abort_armed);
    CHECK(a->state == FIBER_DEAD);
    CHECK((tlang_fiber*)a == &a->pub);  /* first-member identity */
    CHECK(g_sched.nlive == 1 && g_sched.nfibers == 1);

    b = tlang_fiber_acquire(&g_sched);
    CHECK(b != NULL && b != a);
    CHECK(g_sched.nlive == 2 && g_sched.nfibers == 2);

    /* Release a: nlive drops, it returns to the free list (reused next). */
    tlang_fiber_release(&g_sched, a);
    CHECK(g_sched.nlive == 1 && g_sched.nfibers == 2);  /* pooled, not destroyed */
    {
        Fiber* c = tlang_fiber_acquire(&g_sched);
        CHECK(c == a);                       /* reused from the free list */
        CHECK(g_sched.nfibers == 2);         /* no new fiber created */
        tlang_fiber_release(&g_sched, c);
    }
    tlang_fiber_release(&g_sched, b);
    sched_down();
}

static void test_pool_exhaustion(void) {
    Fiber* fs[8];
    int i, got = 0;
    sched_up();  /* max_fibers == 4 */
    for (i = 0; i < 8; i++) {
        fs[i] = tlang_fiber_acquire(&g_sched);
        if (fs[i] != NULL) got++;
    }
    CHECK(got == 4);                 /* capped at max_fibers */
    CHECK(fs[4] == NULL);            /* exhaustion returns NULL, never aborts */
    for (i = 0; i < got; i++) tlang_fiber_release(&g_sched, fs[i]);
    sched_down();
}

/* ---- guard page: writing below the stack faults ---- */
static volatile sig_atomic_t g_segv;
static sigjmp_buf g_segv_jmp;
static void segv_handler(int sig) { (void)sig; g_segv = 1; siglongjmp(g_segv_jmp, 1); }

static void test_guard_page(void) {
    Fiber* f;
    struct sigaction sa, old;
    size_t page = 4096;
    sched_up();
    f = tlang_fiber_acquire(&g_sched);
    CHECK(f != NULL && f->stack_base != NULL);

    memset(&sa, 0, sizeof sa);
    sa.sa_handler = segv_handler;
    sigemptyset(&sa.sa_mask);
    sigaction(SIGSEGV, &sa, &old);
    g_segv = 0;
    if (sigsetjmp(g_segv_jmp, 1) == 0) {
        /* The lowest page of stack_base is PROT_NONE: writing it must fault. */
        volatile char* guard = (volatile char*)f->stack_base + (page / 2);
        *guard = 1;
        CHECK(0 && "guard page write should have faulted");
    } else {
        CHECK(g_segv == 1);
    }
    sigaction(SIGSEGV, &old, NULL);

    tlang_fiber_release(&g_sched, f);
    sched_down();
}

/* ---- cleanup stack ordering (LIFO) ---- */
static int g_order[4];
static int g_order_n;
static void record_cleanup(Fiber* f, void* arg) {
    (void)f;
    g_order[g_order_n++] = (int)(intptr_t)arg;
}

static void test_cleanup_lifo_and_pop(void) {
    Fiber f;
    tlang_cleanup c1, c2, c3;
    memset(&f, 0, sizeof f);
    g_order_n = 0;

    c1.fn = record_cleanup; c1.arg = (void*)(intptr_t)1; c1.next = NULL;
    c2.fn = record_cleanup; c2.arg = (void*)(intptr_t)2; c2.next = NULL;
    c3.fn = record_cleanup; c3.arg = (void*)(intptr_t)3; c3.next = NULL;
    tlang_cleanup_push(&f, &c1);
    tlang_cleanup_push(&f, &c2);
    tlang_cleanup_push(&f, &c3);

    /* Pop the middle one without running it. */
    tlang_cleanup_pop(&f, &c2);
    tlang_cleanup_run_all(&f);
    CHECK(g_order_n == 2);
    CHECK(g_order[0] == 3 && g_order[1] == 1);  /* LIFO, c2 skipped */
    CHECK(f.cleanups == NULL);
}

/* ---- spawn trampoline runs fn then releases the fiber ---- */
static int g_fn_ran;
static Fiber* g_fn_fiber;
static void* g_fn_arg;
static int g_tramp_cleanup_ran;
static void tramp_cleanup(Fiber* f, void* arg) { (void)f; (void)arg; g_tramp_cleanup_ran++; }
static tlang_cleanup g_tramp_node;

static void spawn_entry(Fiber* self, void* arg) {
    g_fn_ran++;
    g_fn_fiber = self;
    g_fn_arg = arg;
    /* A cleanup the trampoline must run after fn returns. */
    g_tramp_node.fn = tramp_cleanup;
    g_tramp_node.arg = NULL;
    g_tramp_node.next = NULL;
    tlang_cleanup_push(self, &g_tramp_node);
}

static void test_spawn_trampoline(void) {
    Fiber* f;
    sched_up();
    g_fn_ran = 0;
    g_tramp_cleanup_ran = 0;
    f = tlang_spawn(&g_sched, spawn_entry, (void*)(intptr_t)0xBEEF);
    CHECK(f != NULL);
    CHECK(f->state == FIBER_READY);
    CHECK(g_sched.nlive == 1);

    /* Run the scheduler: the only fiber runs fn, cleanups, releases, exits. */
    tlang_sched_run(&g_sched);
    CHECK(g_fn_ran == 1);
    CHECK(g_fn_fiber == f);
    CHECK(g_fn_arg == (void*)(intptr_t)0xBEEF);
    CHECK(g_tramp_cleanup_ran == 1);     /* trampoline ran cleanups */
    CHECK(g_sched.nlive == 0);           /* fiber returned to the pool */
    sched_down();
}

/* ---- request abort: armed longjmp runs cleanups, un-armed is fatal ---- */
static int g_abort_cleanup_ran;
static void abort_cleanup(Fiber* f, void* arg) { (void)f; (void)arg; g_abort_cleanup_ran++; }
static tlang_cleanup g_abort_node;
static int g_abort_fiber_done;

static void abort_entry(Fiber* self, void* arg) {
    (void)arg;
    g_abort_node.fn = abort_cleanup;
    g_abort_node.arg = NULL;
    g_abort_node.next = NULL;
    tlang_cleanup_push(self, &g_abort_node);

    if (TLANG_ABORT_POINT(self) == 0) {
        self->abort_armed = true;
        tlang_request_abort(self, 503, "simulated abort");
        CHECK(0 && "request_abort must not return");
    } else {
        self->abort_armed = false;
        CHECK(self->abort_status == 503);
        tlang_cleanup_run_all(self);
        g_abort_fiber_done = 1;
    }
}

static void test_request_abort_armed(void) {
    Fiber* f;
    sched_up();
    g_abort_cleanup_ran = 0;
    g_abort_fiber_done = 0;
    f = tlang_spawn(&g_sched, abort_entry, NULL);
    CHECK(f != NULL);
    tlang_sched_run(&g_sched);
    CHECK(g_abort_fiber_done == 1);
    CHECK(g_abort_cleanup_ran == 1);   /* cleanups ran after the longjmp */
    CHECK(g_sched.nlive == 0);
    sched_down();
}

int main(void) {
    test_acquire_release_pool();
    test_pool_exhaustion();
    test_guard_page();
    test_cleanup_lifo_and_pop();
    test_spawn_trampoline();
    test_request_abort_armed();

    if (failures != 0) {
        printf("FAIL test_fiber (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_fiber\n");
    return 0;
}
