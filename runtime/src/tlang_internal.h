/*
 * tlang_internal.h - private API shared by the runtime modules (the .c files
 * in src/).
 *
 * Generated code never includes this header; runtime sources and runtime
 * tests (tests/test_NAME.c) do. docs/RUNTIME.md assigns every function declared here
 * to the one source file that implements it. The conventions of tlang.h
 * (fiber parameter, "may fail", OOM abort, lifetimes) apply here too.
 *
 * Rules for runtime code:
 *  - Include this header first in every runtime .c file: it defines _GNU_SOURCE
 *    before any system header (the Makefile and the driver also pass
 *    -D_GNU_SOURCE).
 *  - C11 plus POSIX/Linux, pthreads, and libpq (pg.c only). No thread-local
 *    storage, no <stdatomic.h>, no GNU statement expressions: TCC 0.9.27
 *    builds the runtime in dev mode. Inline assembly only in ctxswitch.c.
 *  - Share nothing between schedulers. State reachable from a tlang_sched is
 *    only touched by that scheduler's thread; the one cross-thread entry
 *    point is tlang_sched_request_stop.
 *  - Abort safety: inside a request every allocation may longjmp out
 *    (tlang_alloc_failed -> tlang_request_abort). Keep data structures
 *    consistent at every allocation point, and never hold a resource (pooled
 *    connection, malloc'd temporary) across an allocation unless a cleanup
 *    hook (tlang_cleanup_push) releases it.
 *  - Never log secrets (the database URL) or request bodies.
 */
#ifndef TLANG_INTERNAL_H
#define TLANG_INTERNAL_H

#ifndef _GNU_SOURCE
#define _GNU_SOURCE 1
#endif

#include "tlang.h"

#include <pthread.h>
#include <setjmp.h>
#include <stdarg.h>
#include <sys/epoll.h>
#include <sys/types.h>
#include <sys/uio.h>

/* ========================================================================
 * 1. Build configuration
 * ======================================================================== */

/* Context-switch implementation (spec §7.4). Define TLANG_USE_UCONTEXT (any
 * value) to use getcontext/makecontext/swapcontext. It is defined
 * automatically under TCC and on architectures other than x86_64 and aarch64;
 * everywhere else ctxswitch.c uses its assembly switch. */
#if !defined(TLANG_USE_UCONTEXT)
#  if defined(__TINYC__) || !(defined(__x86_64__) || defined(__aarch64__))
#    define TLANG_USE_UCONTEXT 1
#  endif
#endif
#if defined(TLANG_USE_UCONTEXT)
#  include <ucontext.h>
#endif

/* TLANG_ASAN is 1 when compiling with AddressSanitizer (gcc or clang), else 0.
 * Fiber switches must then be annotated (see tlang_fctx_switch). */
#if !defined(TLANG_ASAN)
#  if defined(__SANITIZE_ADDRESS__)
#    define TLANG_ASAN 1
#  elif defined(__has_feature)
#    if __has_feature(address_sanitizer)
#      define TLANG_ASAN 1
#    endif
#  endif
#endif
#if !defined(TLANG_ASAN)
#  define TLANG_ASAN 0
#endif

/* printf-style format checking where the compiler supports it. */
#if (defined(__GNUC__) || defined(__clang__)) && !defined(__TINYC__)
#  define TLANG_PRINTF(fmt_idx, args_idx) __attribute__((format(printf, fmt_idx, args_idx)))
#else
#  define TLANG_PRINTF(fmt_idx, args_idx)
#endif

/* Default stack size of a fiber in bytes (spec §7.1). TLANG_STACK_SIZE in the
 * environment overrides it at run time (tlang_config.stack_size). */
#ifndef TLANG_STACK_SIZE
#define TLANG_STACK_SIZE (256 * 1024)
#endif

/* Default maximum number of fibers per scheduler (spec §7.1). Overridden at
 * run time by TLANG_MAX_FIBERS (tlang_config.max_fibers). */
#ifndef TLANG_MAX_FIBERS
#define TLANG_MAX_FIBERS 10000
#endif

typedef struct Fiber Fiber;
typedef struct tlang_sched tlang_sched;

/* Opaque per-scheduler PostgreSQL pool and pooled connection (pg.c). */
struct tlang_pg_pool;
struct tlang_pg_conn;

/* ========================================================================
 * 2. Configuration (config.c, DESIGN.md §4.2)
 * ======================================================================== */

/* Process configuration, read once from the environment by
 * tlang_config_load before any scheduler starts and read-only afterwards
 * (shared by all scheduler threads without locking). Environment variable,
 * default and accepted range of each field: */
typedef struct tlang_config {
    char host[256];                    /* TLANG_HOST, "0.0.0.0"; IPv4/IPv6 literal or host name */
    int port;                          /* TLANG_PORT, 8080; 0..65535, 0 = an ephemeral port
                                          shared by all schedulers (reported at startup) */
    int threads;                       /* TLANG_THREADS, online CPUs; 1..1024 */
    int max_fibers;                    /* TLANG_MAX_FIBERS, TLANG_MAX_FIBERS macro; 1..1000000 */
    size_t stack_size;                 /* TLANG_STACK_SIZE, TLANG_STACK_SIZE macro; 65536..67108864,
                                          rounded up to the page size */
    size_t max_header_bytes;           /* TLANG_MAX_HEADER_BYTES, 8192; 1024..1048576 (431 above) */
    int max_headers;                   /* TLANG_MAX_HEADERS, 32; 1..TLANG_CTX_MAX_HEADERS (431 above) */
    size_t max_uri_bytes;              /* TLANG_MAX_URI_BYTES, 2048; 16..65536 (414 above) */
    size_t max_body_bytes;             /* TLANG_MAX_BODY_BYTES, 1048576; 0..1073741824 (413 above) */
    uint32_t header_timeout_ms;        /* TLANG_HEADER_TIMEOUT_MS, 5000; 1..3600000 */
    uint32_t idle_timeout_ms;          /* TLANG_IDLE_TIMEOUT_MS, 60000; 1..86400000 */
    int json_max_depth;                /* TLANG_JSON_MAX_DEPTH, 32; 1..256 */
    const char* database_url;          /* TLANG_DATABASE_URL, else DATABASE_URL, else NULL. Points
                                          into the environment. SECRET: never log it. */
    int db_pool_size;                  /* TLANG_DB_POOL_SIZE, 8; 1..1024 (per scheduler) */
    uint32_t db_pool_timeout_ms;       /* TLANG_DB_POOL_TIMEOUT_MS, 2000; 1..600000 */
    uint32_t db_statement_timeout_ms;  /* TLANG_DB_STATEMENT_TIMEOUT_MS, 5000; 0..86400000 (0: off) */
    uint32_t shutdown_timeout_ms;      /* TLANG_SHUTDOWN_TIMEOUT_MS, 10000; 0..600000: how long a
                                          graceful stop waits for in-flight requests */
} tlang_config;

/* Fills *cfg with the defaults listed above (threads = online CPUs,
 * database_url = NULL). Never fails. */
void tlang_config_defaults(tlang_config* cfg);

/* tlang_config_defaults, then applies every variable that is set and
 * non-empty (an empty value counts as unset). Numbers are plain decimal
 * digits. Returns 0 on success. Returns -1 when a value is malformed or out
 * of range, writing a one-line message that names the variable (never its
 * value for TLANG_DATABASE_URL / DATABASE_URL) into err (NUL-terminated,
 * truncated to errlen); tlang_main then exits with status 2. */
int tlang_config_load(tlang_config* cfg, char* err, size_t errlen);

/* ========================================================================
 * 3. Arena lifecycle (arena.c)
 * ======================================================================== */

/* Allocates the first chunk (ARENA_CHUNK_SIZE bytes) and makes it current.
 * Returns 0, or -1 when malloc fails (*a is then {NULL, NULL}). */
int tlang_arena_init(MemoryArena* a);

/* Frees every chunk, including the first; *a becomes {NULL, NULL}. Used when
 * the fiber pool is destroyed. Safe on a {NULL, NULL} arena. */
void tlang_arena_destroy(MemoryArena* a);

/* ========================================================================
 * 4. Context switch (ctxswitch.c, spec §7.4)
 * ======================================================================== */

/* Saved execution state of a fiber or of a scheduler loop.
 * Assembly switch: `sp` is the saved stack pointer; the callee-saved
 * registers are pushed on the suspended stack itself (x86_64: rbx, rbp,
 * r12-r15, MXCSR control bits, x87 control word; aarch64: x19-x29, x30, d8-d15).
 * ASan fields: bounds of the stack this context runs on and the fake-stack
 * handle of __sanitizer_start_switch_fiber. */
typedef struct FiberContext {
#if defined(TLANG_USE_UCONTEXT)
    ucontext_t uc;
#else
    void* sp;
#endif
#if TLANG_ASAN
    void* asan_fake_stack;
    const void* asan_stack_bottom;
    size_t asan_stack_size;
#endif
} FiberContext;

/* Prepares *ctx so that the first tlang_fctx_switch into it calls
 * entry(arg) on the stack [stack_lo, stack_lo + stack_size), whose top is
 * aligned down to 16 bytes. entry must never return (it ends with
 * tlang_fctx_exit). Calling it again on the same ctx starts a fresh run, which
 * is how pooled fibers are reused. Never fails. */
void tlang_fctx_init(FiberContext* ctx, void* stack_lo, size_t stack_size,
                     void (*entry)(void* arg), void* arg);

/* Makes *ctx represent the calling thread's own stack (the scheduler loop).
 * Under ASan it records the thread's stack bounds. Never fails. */
void tlang_fctx_init_thread(FiberContext* ctx);

/* Saves the current state in *from and resumes *to; returns when a later
 * switch resumes *from. Under ASan (TLANG_ASAN) it calls
 * __sanitizer_start_switch_fiber(&from->asan_fake_stack, to->asan_stack_bottom,
 * to->asan_stack_size) before the switch and
 * __sanitizer_finish_switch_fiber(from->asan_fake_stack, ...) once resumed.
 * The ucontext variant does the same around swapcontext. */
void tlang_fctx_switch(FiberContext* from, FiberContext* to);

/* The last switch of a finished fiber run: like tlang_fctx_switch, but it
 * never returns and, under ASan, passes NULL as fake_stack_save so that the
 * fake stack of the finished run is released. */
_Noreturn void tlang_fctx_exit(FiberContext* from, FiberContext* to);

/* ========================================================================
 * 5. Fibers (fiber.c, spec §7.1)
 * ======================================================================== */

/* FIBER_DEAD: in the pool (free list) or never started.
 * FIBER_READY: in its scheduler's run queue.
 * FIBER_RUNNING: the scheduler's current fiber.
 * FIBER_WAITING_IO: parked until an fd event, a deadline, a wait-queue wake
 * or a stop request (the name is the spec's; it covers every kind of wait). */
typedef enum FiberState {
    FIBER_DEAD = 0,
    FIBER_READY,
    FIBER_RUNNING,
    FIBER_WAITING_IO
} FiberState;

/* Per-fiber cleanup hook, run LIFO by tlang_cleanup_run_all. The node is
 * owned by the registering module and must stay valid until it is popped or
 * has run: keep it in a runtime-owned object (a pooled connection, for
 * example) or in the arena, never in a stack frame that a request abort can
 * unwind. */
typedef struct tlang_cleanup {
    void (*fn)(Fiber* f, void* arg);
    void* arg;
    struct tlang_cleanup* next;
} tlang_cleanup;

/* FIFO wait queue of parked fibers (sched.c), used by the DB pool. One
 * scheduler only. Zero-initialised or tlang_waitq_init'ed. */
typedef struct tlang_waitq {
    Fiber* head;
    Fiber* tail;
    size_t len;
} tlang_waitq;

/* The private fiber. Spec §7.1 fields first (after the public view), then
 * what this runtime adds. The spec field `int err` is pub.err (with the
 * payload pub.error): one source of truth shared with generated code. */
struct Fiber {
    tlang_fiber pub;              /* MUST stay the first member (static assert below):
                                     (Fiber*)fib == fib for every fib the runtime hands out */
    int id;                       /* unique within its scheduler, for logs */
    FiberState state;
    FiberContext ctx;             /* saved context while not running */
    void* stack_base;             /* mmap base: [guard page][stack] */
    size_t stack_size;            /* usable bytes above the guard page */
    MemoryArena arena;            /* per-request arena; pub.arena normally points here */
    void (*fn)(Fiber* self, void* arg);   /* entry of the current run. Unlike spec §7.1 it
                                             receives its fiber: there is no TLS to find it */
    void* arg;
    int waiting_fd;               /* fd waited on, -1 when none */
    uint64_t deadline_ns;         /* absolute tlang_now_ns() deadline of the wait, 0 = none */
    Fiber* next_free;             /* pool free list link */

    tlang_sched* sched;           /* owner scheduler, fixed at creation */
    Fiber* next_ready;            /* run queue link */
    int waiting_fd2;              /* watch fd of tlang_wait_fd2, -1 when none */
    uint32_t wait_events;         /* events requested on waiting_fd */
    int wait_result;              /* value the waker hands to the parked fiber */
    size_t heap_index;            /* 1-based slot in the deadline heap, 0 = not in it */
    tlang_waitq* waitq;           /* queue the fiber is parked in, or NULL */
    Fiber* wq_prev;               /* wait queue links */
    Fiber* wq_next;
    void* wake_value;             /* value handed over by tlang_waitq_wake_one */
    bool interruptible;           /* a stop request cancels this fiber's waits
                                     (idle keep-alive connections) */
    int watch_fd;                 /* client socket of the request being served, -1 otherwise:
                                     DB waits watch it to notice a disconnect */
    tlang_ctx* req;               /* request being served, NULL otherwise (logging) */
    tlang_cleanup* cleanups;      /* hook stack, LIFO */
    bool abort_armed;             /* abort_jmp holds a live setjmp of the connection loop */
    int32_t abort_status;         /* status passed to tlang_request_abort */
    jmp_buf abort_jmp;            /* per-request abort target (TLANG_ABORT_POINT) */
};

_Static_assert(offsetof(Fiber, pub) == 0, "tlang_fiber must be the first member of Fiber");

/* Private fiber of a public fiber pointer (valid for every tlang_fiber the
 * runtime hands out, all of which are embedded in a Fiber). */
static inline Fiber* tlang_fiber_of(tlang_fiber* fib) {
    return (Fiber*)(void*)fib;
}

/* Takes a fiber from the scheduler's pool: the free list first, otherwise a
 * new one (stack from mmap with a PROT_NONE guard page at the low end, first
 * arena chunk) while s->nfibers < cfg->max_fibers. The fiber is FIBER_DEAD,
 * with pub.err == 0, pub.arena == &arena, pub.globals == s->globals,
 * waiting_fd / waiting_fd2 / watch_fd == -1, no cleanups, not armed.
 * Increments s->nlive. Returns NULL when the pool is exhausted or mmap/malloc
 * fails (never aborts). */
Fiber* tlang_fiber_acquire(tlang_sched* s);

/* Returns a finished fiber to the pool: arena_reset, clears the error and
 * per-request fields, state FIBER_DEAD, pushes it on the free list (stack and
 * first chunk kept), decrements s->nlive, and resumes a paused accept
 * (tlang_sched_resume_accept). Its cleanup stack must be empty. Called by the
 * fiber trampoline while still running on f's stack; the trampoline then
 * leaves with tlang_fctx_exit before anything can reuse f. */
void tlang_fiber_release(tlang_sched* s, Fiber* f);

/* Acquires a fiber, sets fn/arg, prepares its context with a trampoline that
 * runs fn(f, arg), then tlang_cleanup_run_all, tlang_fiber_release and
 * tlang_fctx_exit to the scheduler loop; makes it FIBER_READY. Returns the
 * fiber, or NULL when tlang_fiber_acquire fails. Callable from the
 * scheduler loop or from any fiber of the same scheduler. */
Fiber* tlang_spawn(tlang_sched* s, void (*fn)(Fiber* self, void* arg), void* arg);

/* Unmaps every stack and destroys every arena of the pool (scheduler
 * shutdown, no fiber may be live). */
void tlang_fiber_pool_destroy(tlang_sched* s);

/* Pushes a cleanup hook on f's stack (O(1)). */
void tlang_cleanup_push(Fiber* f, tlang_cleanup* c);

/* Unlinks c from f's stack without running it (normally c is the top; any
 * position is accepted). No-op when c is not linked. */
void tlang_cleanup_pop(Fiber* f, tlang_cleanup* c);

/* Pops and runs every hook, most recent first. Called by the fiber trampoline
 * when a run ends and by the connection loop after each request (normal end
 * or abort), so a leaked transaction or connection is always released.
 * Hooks must not allocate from the arena in a way that can abort. */
void tlang_cleanup_run_all(Fiber* f);

/* The request-abort target. The connection loop arms it around each
 * dispatcher call:
 *     if (TLANG_ABORT_POINT(f) == 0) {
 *         f->abort_armed = true;
 *         prog->dispatcher(&f->pub, ctx);
 *         f->abort_armed = false;
 *     } else {
 *         f->abort_armed = false;    // aborted: f->abort_status holds the status
 *         tlang_cleanup_run_all(f);  // then answer abort_status if nothing was sent,
 *     }                              // reset the arena and close the connection
 * Locals of the arming function that change between setjmp and longjmp and
 * are read afterwards must be volatile. */
#define TLANG_ABORT_POINT(f) setjmp((f)->abort_jmp)

/* Aborts the current request of f: logs `reason` (a static string) with the
 * status, sets f->abort_status = status and longjmps to f->abort_jmp. When f
 * is not armed (startup, script mode) it logs "fatal: <reason>" and exits the
 * process with status 1. Must be called on f's own stack. */
_Noreturn void tlang_request_abort(Fiber* f, int32_t status, const char* reason);

/* ========================================================================
 * 6. Scheduler, netpoller and waits (sched.c, spec §7.2, §7.3)
 * ======================================================================== */

/* Results of the wait functions. Positive values are epoll event masks. */
#define TLANG_WAIT_OK          0   /* woken by tlang_waitq_wake_one */
#define TLANG_WAIT_TIMEOUT   (-1)  /* the deadline passed */
#define TLANG_WAIT_HUP       (-2)  /* EPOLLHUP/EPOLLRDHUP without a requested event */
#define TLANG_WAIT_ERR       (-3)  /* EPOLLERR without a requested event, or epoll_ctl failed */
#define TLANG_WAIT_CANCELLED (-4)  /* stop request (interruptible waits, wait queues) */
#define TLANG_WAIT_PEER_GONE (-5)  /* tlang_wait_fd2: the watched fd hung up or failed */
#define TLANG_IO_ERR         (-6)  /* tlang_net_*: a syscall failed, see errno */

/* epoll registration state of one fd, indexed by fd. Used to choose
 * EPOLL_CTL_ADD (first wait) or EPOLL_CTL_MOD (re-arm, spec §7.2 step 2)
 * and to find the waiter of an event. An event is delivered only when
 * waiter is parked on that fd (waiting_fd or waiting_fd2); stale events of a
 * one-shot registration that fired after the waiter moved on are dropped. */
typedef struct tlang_fd_slot {
    Fiber* waiter;          /* fiber parked on this fd, or NULL */
    uint32_t events;        /* events armed by the last ADD/MOD */
    bool registered;        /* added to the epoll set and not forgotten */
} tlang_fd_slot;

/* One scheduler per thread (spec §7.3). Fields are owned by the module named
 * in brackets; other modules read them only. */
struct tlang_sched {
    int id;                          /* 0..threads-1 [main.c] */
    const tlang_config* cfg;         /* [main.c] */
    const tlang_program* prog;       /* [main.c] */
    pthread_t thread;                /* [main.c] */
    int epfd;                        /* epoll instance [sched.c] */
    int listen_fd;                   /* own SO_REUSEPORT listener, -1 in script mode [main.c] */
    int wake_fd;                     /* eventfd in the epoll set: stop requests [sched.c] */
    int ack_fd;                      /* main-owned eventfd for listener-close acknowledgments, -1 outside server threads [main.c] */
    int test_ack_fd;                 /* test-only close notification pipe, -1 in production */
    bool stopping;                   /* a stop request was received [sched.c] */
    bool listener_acknowledged;      /* this scheduler published its close acknowledgment [sched.c] */
    bool accept_armed;               /* listener registered for EPOLLIN [sched.c] */
    bool accept_paused;              /* disarmed because the fiber pool is exhausted [sched.c] */
    int exit_code;                   /* tlang_sched_run result [main.c] */
    Fiber* current;                  /* running fiber, NULL in the loop [sched.c] */
    FiberContext loop_ctx;           /* the loop's context on the thread stack [sched.c] */
    Fiber* runq_head;                /* FIFO run queue via Fiber.next_ready [sched.c] */
    Fiber* runq_tail;
    Fiber** heap;                    /* deadline min-heap on deadline_ns [sched.c] */
    size_t heap_len;
    size_t heap_cap;
    Fiber* free_list;                /* fiber pool [fiber.c] */
    int nfibers;                     /* fibers created (pooled + live) [fiber.c] */
    int nlive;                       /* fibers acquired and not released [fiber.c] */
    int next_fiber_id;               /* [fiber.c] */
    tlang_fd_slot* fds;              /* fd table, grown on demand [sched.c] */
    size_t nfds;
    struct tl_globals* globals;      /* this scheduler's globals [main.c] */
    MemoryArena globals_arena;       /* never-reset arena of global initialisation [main.c] */
    struct tlang_pg_pool* db_pool;   /* NULL unless prog->uses_db [pg.c] */
    uint64_t now_ns;                 /* monotonic time cached once per loop iteration [sched.c] */
    char http_date[32];              /* cached IMF-fixdate for the Date header [http.c] */
    int64_t http_date_sec;           /* second http_date was formatted for [http.c] */
};

/* Initialises *s for scheduler `id`: epoll set, wake eventfd (registered),
 * empty fd table, run queue, heap and pool; listen_fd stored but not armed.
 * Returns 0, or -1 with a message in err (resources released). */
int tlang_sched_init(tlang_sched* s, int id, const tlang_config* cfg,
                     const tlang_program* prog, int listen_fd, char* err, size_t errlen);

/* Runs the event loop on the calling thread until it is stopping (or has no
 * listener) and no fiber is live. One iteration: run every READY fiber,
 * compute the epoll timeout from the earliest deadline, epoll_wait, then
 * dispatch: listen_fd readable -> tlang_net_accept_ready, wake_fd -> stop
 * request (control events before listeners), other fds -> wake the waiter
 * recorded in the fd table; then wake the fibers whose deadline passed and
 * refresh now_ns. On a stop request it closes the listener and acknowledges
 * that close, cancels interruptible waits and wait queues, and gives in-flight
 * fibers cfg->shutdown_timeout_ms before returning anyway. Returns s->exit_code. */
int tlang_sched_run(tlang_sched* s);

/* Performs the scheduler-thread-only stop transition, including listener close,
 * cancellation, deadline setup and one close acknowledgment when ack_fd >= 0. */
void tlang_sched_stop(tlang_sched* s, uint64_t* stop_deadline);

/* Closes the epoll set, eventfd, listener and fd table, destroys the fiber
 * pool and the DB pool. No fiber may be live. */
void tlang_sched_destroy(tlang_sched* s);

/* Asks s to stop gracefully. Thread-safe and async-signal-safe: it only
 * write(2)s to s->wake_fd. */
void tlang_sched_request_stop(tlang_sched* s);

/* Makes a parked or new fiber FIBER_READY and appends it to the run queue.
 * Scheduler thread only. */
void tlang_sched_ready(tlang_sched* s, Fiber* f);

/* Registers the listener for EPOLLIN (ADD the first time, MOD later) unless
 * the scheduler is stopping or has no listener; clears accept_paused. Called
 * by main.c once global initialisation is done and by tlang_fiber_release
 * when accept was paused. */
void tlang_sched_resume_accept(tlang_sched* s);

/* Disarms the listener (accept_paused = true) until a fiber is released
 * (DESIGN.md §4.1: pool exhaustion pauses accept). */
void tlang_sched_pause_accept(tlang_sched* s);

/* Current CLOCK_MONOTONIC time in nanoseconds. Thread-safe. */
uint64_t tlang_now_ns(void);

/* Parks the running fiber f until fd is ready for `events` (EPOLLIN and/or
 * EPOLLOUT), the absolute deadline passes (deadline_ns == 0: none), or, if
 * f->interruptible, a stop request. The registration always uses
 * EPOLLONESHOT and adds EPOLLRDHUP when events include EPOLLIN (not for
 * EPOLLOUT alone, so a peer that half-closed can still receive a response).
 * Returns the ready epoll mask (> 0, containing at least one requested event
 * and possibly EPOLLRDHUP/EPOLLHUP/EPOLLERR: do the I/O and let the syscall
 * report EOF or the error), TLANG_WAIT_HUP / TLANG_WAIT_ERR when only those
 * conditions are set, TLANG_WAIT_TIMEOUT, or TLANG_WAIT_CANCELLED. Call it
 * after the non-blocking syscall returned EAGAIN. f must be
 * f->sched->current. */
int tlang_wait_fd(Fiber* f, int fd, uint32_t events, uint64_t deadline_ns);

/* tlang_wait_fd on fd while also watching watch_fd for EPOLLRDHUP, EPOLLHUP
 * and EPOLLERR only (no EPOLLIN, so pipelined request bytes do not wake it).
 * Returns like tlang_wait_fd, or TLANG_WAIT_PEER_GONE when watch_fd reports
 * first: pg.c then cancels the query (the client disconnected). watch_fd < 0
 * behaves exactly like tlang_wait_fd. */
int tlang_wait_fd2(Fiber* f, int fd, uint32_t events, int watch_fd, uint64_t deadline_ns);

/* Drops fd from the epoll set (EPOLL_CTL_DEL when registered) and clears its
 * slot. Must be called before close(fd), because the kernel reuses fd
 * numbers. No fiber may be parked on fd. */
void tlang_fd_forget(tlang_sched* s, int fd);

/* Parks f until the absolute deadline. Returns TLANG_WAIT_TIMEOUT, or
 * TLANG_WAIT_CANCELLED on a stop request if f->interruptible. */
int tlang_sleep_until(Fiber* f, uint64_t deadline_ns);

/* Requeues the running fiber f at the tail of the run queue and switches to
 * the loop (fairness between long requests). */
void tlang_yield(Fiber* f);

/* Empties *q. */
void tlang_waitq_init(tlang_waitq* q);

/* Parks f at the tail of q until woken or the deadline passes. Returns
 * TLANG_WAIT_OK with *value set to the value given to tlang_waitq_wake_one
 * (direct hand-off: a released DB connection goes to the oldest waiter, not to
 * a newcomer), TLANG_WAIT_TIMEOUT (f removed from q, *value untouched), or
 * TLANG_WAIT_CANCELLED (tlang_waitq_cancel_all). value may be NULL. */
int tlang_waitq_wait(tlang_waitq* q, Fiber* f, uint64_t deadline_ns, void** value);

/* Wakes the oldest waiter of q with `value`. Returns false when q is empty
 * (value not consumed). Same scheduler only. */
bool tlang_waitq_wake_one(tlang_waitq* q, void* value);

/* Wakes every waiter of q with TLANG_WAIT_CANCELLED (shutdown). */
void tlang_waitq_cancel_all(tlang_waitq* q);

/* ========================================================================
 * 7. Sockets (net.c)
 * ======================================================================== */

/* Creates a non-blocking, close-on-exec listening socket bound to host:port
 * (host resolved with getaddrinfo, AI_PASSIVE) with SO_REUSEADDR and
 * SO_REUSEPORT, backlog SOMAXCONN: one per scheduler (spec §7.3). Port 0
 * binds an ephemeral port; main.c then reads it with tlang_net_local_port and
 * binds the other schedulers' listeners to that port. Returns the fd, or -1
 * with a message in err. */
int tlang_net_listen(const char* host, int port, char* err, size_t errlen);

/* The local port of a bound socket (getsockname), or -1. */
int tlang_net_local_port(int fd);

/* Called by the loop when s->listen_fd is readable. Accepts (accept4,
 * SOCK_NONBLOCK | SOCK_CLOEXEC, TCP_NODELAY) until EAGAIN, spawning one
 * tlang_http_conn_main fiber per connection with the fd as argument. When no
 * fiber is available it stops before accepting and calls
 * tlang_sched_pause_accept; a connection whose fiber cannot be spawned is
 * closed. */
void tlang_net_accept_ready(tlang_sched* s);

/* Reads up to len bytes from the non-blocking fd, parking f on EAGAIN until
 * data arrives or the deadline passes. Returns the byte count (> 0), 0 at
 * end of stream, or a negative TLANG_WAIT_* / TLANG_IO_ERR code. Retries
 * EINTR. */
ssize_t tlang_net_read(Fiber* f, int fd, void* buf, size_t len, uint64_t deadline_ns);

/* Writes all len bytes, parking f on EAGAIN. Returns len, or a negative
 * TLANG_WAIT_* / TLANG_IO_ERR code (EPIPE and ECONNRESET included: SIGPIPE
 * is ignored process-wide). */
ssize_t tlang_net_write_all(Fiber* f, int fd, const void* buf, size_t len, uint64_t deadline_ns);

/* Gathering version of tlang_net_write_all. iov is modified while partial
 * writes are consumed. Returns the total byte count or a negative code. */
ssize_t tlang_net_writev_all(Fiber* f, int fd, struct iovec* iov, int iovcnt, uint64_t deadline_ns);

/* tlang_fd_forget(s, fd), then close(fd). */
void tlang_net_close(tlang_sched* s, int fd);

/* ========================================================================
 * 8. HTTP connections (http.c, spec §8.1, DESIGN.md §4.3)
 * ======================================================================== */

/* Fiber entry of one client connection; arg is the client fd
 * ((void*)(intptr_t)fd). Runs the keep-alive loop: read and parse requests
 * within the header and idle timeouts (idle waits are interruptible), call
 * the dispatcher under TLANG_ABORT_POINT with self->req / self->watch_fd set,
 * answer 404 when the dispatcher returned normally without responding and the
 * error status when an error escaped, write the response, run the cleanup
 * hooks, arena_reset, then loop or close (tlang_net_close). */
void tlang_http_conn_main(Fiber* self, void* arg);

/* ========================================================================
 * 9. PostgreSQL pool (pg.c, spec §11.3)
 * ======================================================================== */

/* Creates s->db_pool for s->cfg (pool size, timeouts, database URL). Does no
 * I/O: connections are opened on demand inside fibers (PQconnectStart /
 * PQconnectPoll), so tlang_sched_run need not be running yet. Returns 0, or
 * -1 with a message in err (database URL missing; in TLANG_NO_PG builds it
 * succeeds and every database call throws 500). */
int tlang_pg_pool_create(tlang_sched* s, char* err, size_t errlen);

/* Closes every pooled connection and frees s->db_pool (sets it to NULL).
 * Safe when s->db_pool is NULL. */
void tlang_pg_pool_destroy(tlang_sched* s);

/* ========================================================================
 * 10. Errors and logging (errors.c, console.c)
 * ======================================================================== */

/* tlang_throw with a printf-formatted message built in the fiber arena
 * (lifetime request). Aborts on OOM. */
void tlang_throw_fmt(tlang_fiber* fib, int32_t status, const char* fmt, ...) TLANG_PRINTF(3, 4);

/* Logs one line "tlang: error: <message>\n" / "tlang: warning: <message>\n"
 * to stderr with a single write(2). The message is formatted into a bounded
 * stack buffer (truncated beyond 4 KiB), so logging never allocates and never
 * aborts. Thread-safe. */
void tlang_log_error(const char* fmt, ...) TLANG_PRINTF(1, 2);
void tlang_log_warn(const char* fmt, ...) TLANG_PRINTF(1, 2);

/* write(2)s the whole buffer to fd, retrying EINTR and short writes; gives up
 * silently on other errors. Thread-safe (no locks; each call is one or more
 * write calls). */
void tlang_log_write(int fd, const char* data, size_t len);

/* ========================================================================
 * 11. Strings, numbers and slices: shared helpers (strings.c, slices.c)
 * ======================================================================== */

/* Buffer sizes for the formatters (including a terminating NUL). */
#define TLANG_FMT_I64_MAX 21    /* "-9223372036854775808" */
#define TLANG_FMT_F64_MAX 32    /* "-1.2345678901234567e-308", "Infinity" */

/* Writes v in decimal to buf (at least TLANG_FMT_I64_MAX bytes) followed by a
 * NUL; returns the length without the NUL. Pure. */
size_t tlang_fmt_i64(char* buf, int64_t v);

/* Writes v to buf (at least TLANG_FMT_F64_MAX bytes) followed by a NUL;
 * returns the length without the NUL. Shortest of %.15g/%.16g/%.17g that
 * round-trips, -0 as "0". Non-finite values: "null" when json is true,
 * otherwise "NaN", "Infinity", "-Infinity". Used by tlang_f64_to_string,
 * json_write_f64 and the console. Pure. */
size_t tlang_fmt_f64(char* buf, double v, bool json);

/* Strict decimal parse of [p, p + n): `[+-]?[0-9]+`, value within int64.
 * Returns false (out untouched) otherwise. Shared by tlang_str_to_int,
 * tlang_ctx_param_int and the configuration loader. Pure. */
bool tlang_parse_i64(const char* p, size_t n, int64_t* out);

/* ASCII case-insensitive equality (header names, DB column names). Pure. */
bool tlang_ascii_ieq(tlang_string a, tlang_string b);

/* A C string as a tlang_string (NULL gives ""). Pure. */
static inline tlang_string tlang_str_cstr(const char* s) {
    tlang_string r;
    r.data = s != NULL ? s : "";
    r.len = s != NULL ? strlen(s) : 0;
    return r;
}

/* Field offsets shared by every tlang_slice_<m> (asserted in tlang.h). */
#define TLANG_SLICE_HDR_SIZE   sizeof(tlang_slice_i64)
#define TLANG_SLICE_OFF_ITEMS  offsetof(tlang_slice_i64, items)
#define TLANG_SLICE_OFF_LEN    offsetof(tlang_slice_i64, len)
#define TLANG_SLICE_OFF_CAP    offsetof(tlang_slice_i64, cap)
#define TLANG_SLICE_OFF_GLOBAL offsetof(tlang_slice_i64, global)

/* Stores items/len/cap/global into a slice header obtained from
 * tlang_slice_new, field by field through scalar types (memcpy at the
 * TLANG_SLICE_OFF_* offsets), so the header can be read through any
 * tlang_slice_<m> type without breaking strict aliasing. Used by runtime
 * code that builds slices for generated code (tlang_db_query). Pure. */
void tlang_slice_hdr_init(void* hdr, void* items, int64_t len, int64_t cap, bool global);

/* ========================================================================
 * 12. JSON configuration (json.c)
 * ======================================================================== */

/* Sets the value json_max_depth() returns (clamped to 1..256). Called once
 * by tlang_main before scheduler threads start; tests may call it between
 * runs. Not thread-safe. */
void json_set_max_depth(int depth);

#endif /* TLANG_INTERNAL_H */
