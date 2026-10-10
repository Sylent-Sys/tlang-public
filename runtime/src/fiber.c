/*
 * fiber.c - fiber pool, spawn, cleanup hooks and the request-abort path
 * (tlang_internal.h §5, spec §7.1). docs/RUNTIME.md §6.
 *
 * Fibers own an mmap'd stack with a PROT_NONE guard page at the low end and a
 * per-request arena whose first chunk is kept across requests. They are
 * pooled: tlang_fiber_acquire takes one from the free list or mmaps a new one
 * (up to cfg->max_fibers), tlang_fiber_release returns it. tlang_spawn wraps a
 * user entry in a trampoline that runs the cleanup hooks, releases the fiber
 * and switches back to the scheduler loop. tlang_request_abort longjmps to the
 * connection loop when armed, else logs "fatal:" and exits.
 */
#include "tlang_internal.h"

#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

/* The page size, cached. */
static size_t tl_page_size(void) {
    long v = sysconf(_SC_PAGESIZE);
    return v > 0 ? (size_t)v : 4096;
}

/* Completes the ASan fiber-switch the spawner began, at the top of a fresh
 * run (defined in ctxswitch.c; a no-op without ASan). */
void tlang_fctx_finish_switch(FiberContext* self);

/* The trampoline that every spawned fiber runs. It calls the user entry, runs
 * the cleanup hooks, returns the fiber to the pool, and switches to the loop
 * for good. It runs on the fiber's own stack. */
static void tl_fiber_trampoline(void* arg) {
    Fiber* f = (Fiber*)arg;
    tlang_sched* s = f->sched;

    tlang_fctx_finish_switch(&f->ctx);
    f->state = FIBER_RUNNING;
    f->fn(f, f->arg);
    tlang_cleanup_run_all(f);
    tlang_fiber_release(s, f);
    tlang_fctx_exit(&f->ctx, &s->loop_ctx);
}

/* Takes a fiber from the pool, creating one on demand up to cfg->max_fibers.
 * Returns NULL on exhaustion or mmap/malloc failure (never aborts). */
Fiber* tlang_fiber_acquire(tlang_sched* s) {
    Fiber* f = s->free_list;
    size_t page, map_len;
    void* base;

    if (f != NULL) {
        s->free_list = f->next_free;
    } else {
        if (s->nfibers >= s->cfg->max_fibers) return NULL;

        f = (Fiber*)calloc(1, sizeof *f);
        if (f == NULL) return NULL;

        page = tl_page_size();
        map_len = s->cfg->stack_size + page;
        base = mmap(NULL, map_len, PROT_READ | PROT_WRITE,
                    MAP_PRIVATE | MAP_ANONYMOUS | MAP_NORESERVE | MAP_STACK, -1, 0);
        if (base == MAP_FAILED) {
            free(f);
            return NULL;
        }
        /* Guard the lowest page (stack grows down toward it). */
        if (mprotect(base, page, PROT_NONE) != 0) {
            munmap(base, map_len);
            free(f);
            return NULL;
        }
        if (tlang_arena_init(&f->arena) != 0) {
            munmap(base, map_len);
            free(f);
            return NULL;
        }
        f->stack_base = base;
        f->stack_size = s->cfg->stack_size;
        f->sched = s;
        f->id = s->next_fiber_id++;
        s->nfibers++;
    }

    /* Fresh per-acquire state. */
    f->state = FIBER_DEAD;
    f->pub.err = 0;
    f->pub.error.status = 0;
    f->pub.error.message.data = "";
    f->pub.error.message.len = 0;
    f->pub.error.category = TLANG_STR("");
    f->pub.error.code = TLANG_STR("");
    f->pub.arena = &f->arena;
    f->pub.globals = s->globals;
    f->fn = NULL;
    f->arg = NULL;
    f->waiting_fd = -1;
    f->waiting_fd2 = -1;
    f->watch_fd = -1;
    f->deadline_ns = 0;
    f->next_ready = NULL;
    f->wait_events = 0;
    f->wait_result = 0;
    f->heap_index = 0;
    f->waitq = NULL;
    f->wq_prev = NULL;
    f->wq_next = NULL;
    f->wake_value = NULL;
    f->interruptible = false;
    f->req = NULL;
    f->cleanups = NULL;
    f->abort_armed = false;
    f->abort_status = 0;

    s->nlive++;
    return f;
}

/* Returns a finished fiber to the pool. Keeps the stack and the first arena
 * chunk; clears per-request state. The cleanup stack must be empty. */
void tlang_fiber_release(tlang_sched* s, Fiber* f) {
    arena_reset(&f->arena);
    f->pub.err = 0;
    f->pub.error.status = 0;
    f->pub.error.message.data = "";
    f->pub.error.message.len = 0;
    f->pub.error.category = TLANG_STR("");
    f->pub.error.code = TLANG_STR("");
    f->state = FIBER_DEAD;
    f->fn = NULL;
    f->arg = NULL;
    f->waiting_fd = -1;
    f->waiting_fd2 = -1;
    f->watch_fd = -1;
    f->deadline_ns = 0;
    f->next_ready = NULL;
    f->wait_events = 0;
    f->wait_result = 0;
    f->heap_index = 0;
    f->waitq = NULL;
    f->wq_prev = NULL;
    f->wq_next = NULL;
    f->wake_value = NULL;
    f->interruptible = false;
    f->req = NULL;
    f->cleanups = NULL;
    f->abort_armed = false;
    f->abort_status = 0;

    f->next_free = s->free_list;
    s->free_list = f;
    s->nlive--;

    tlang_sched_resume_accept(s);
}

/* Acquires a fiber, prepares its context with the trampoline, makes it READY. */
Fiber* tlang_spawn(tlang_sched* s, void (*fn)(Fiber* self, void* arg), void* arg) {
    Fiber* f = tlang_fiber_acquire(s);
    if (f == NULL) return NULL;

    f->fn = fn;
    f->arg = arg;
    /* The usable stack starts above the guard page. */
    {
        size_t page = tl_page_size();
        void* stack_lo = (char*)f->stack_base + page;
        tlang_fctx_init(&f->ctx, stack_lo, f->stack_size, tl_fiber_trampoline, f);
    }
    tlang_sched_ready(s, f);
    return f;
}

/* Unmaps every stack and destroys every arena of the pool. */
void tlang_fiber_pool_destroy(tlang_sched* s) {
    Fiber* f = s->free_list;
    size_t page = tl_page_size();
    while (f != NULL) {
        Fiber* next = f->next_free;
        tlang_arena_destroy(&f->arena);
        if (f->stack_base != NULL) munmap(f->stack_base, f->stack_size + page);
        free(f);
        f = next;
    }
    s->free_list = NULL;
    s->nfibers = 0;
}

/* LIFO cleanup hook stack. */
void tlang_cleanup_push(Fiber* f, tlang_cleanup* c) {
    c->next = f->cleanups;
    f->cleanups = c;
}

void tlang_cleanup_pop(Fiber* f, tlang_cleanup* c) {
    tlang_cleanup** pp = &f->cleanups;
    while (*pp != NULL) {
        if (*pp == c) {
            *pp = c->next;
            c->next = NULL;
            return;
        }
        pp = &(*pp)->next;
    }
}

void tlang_cleanup_run_all(Fiber* f) {
    while (f->cleanups != NULL) {
        tlang_cleanup* c = f->cleanups;
        f->cleanups = c->next;
        c->next = NULL;
        c->fn(f, c->arg);
    }
}

/* Aborts the current request of f. When armed, logs and longjmps to the
 * connection loop; otherwise logs "fatal:" and exits(1). Runs on f's stack. */
_Noreturn void tlang_request_abort(Fiber* f, int32_t status, const char* reason) {
    if (f != NULL && f->abort_armed) {
        tlang_log_error("request aborted: %d %s", (int)status, reason != NULL ? reason : "");
        f->abort_status = status;
        longjmp(f->abort_jmp, 1);
    }
    tlang_log_error("fatal: %s", reason != NULL ? reason : "");
    exit(1);
}
