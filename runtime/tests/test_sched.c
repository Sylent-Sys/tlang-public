/*
 * test_sched.c - unit tests for src/sched.c using real fds. Covers:
 *   - tlang_wait_fd on a pipe wakes with EPOLLIN when data arrives;
 *   - tlang_wait_fd times out (TLANG_WAIT_TIMEOUT) when nothing happens;
 *   - tlang_sleep_until parks until the deadline;
 *   - tlang_wait_fd2 reports TLANG_WAIT_PEER_GONE when the watched fd hangs up;
 *   - a wait queue hands a value from wake_one directly to the oldest waiter;
 *   - tlang_waitq_cancel_all wakes waiters with TLANG_WAIT_CANCELLED;
 *   - tlang_now_ns is monotonic.
 * Fibers are driven with tlang_sched_run; cooperating fibers trigger events.
 */
#include "tlang_internal.h"

#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/epoll.h>
#include <sys/socket.h>

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
static tlang_program g_prog;

static void sched_up(void) {
    char err[128];
    tlang_config_defaults(&g_cfg);
    g_cfg.stack_size = 256 * 1024;
    memset(&g_prog, 0, sizeof g_prog);
    if (tlang_sched_init(&g_sched, 0, &g_cfg, &g_prog, -1, err, sizeof err) != 0) {
        fprintf(stderr, "sched init failed: %s\n", err);
        exit(2);
    }
    tlang_fctx_init_thread(&g_sched.loop_ctx);
}

static void sched_down(void) {
    tlang_sched_destroy(&g_sched);
}

static void set_nonblock(int fd) {
    int fl = fcntl(fd, F_GETFL, 0);
    fcntl(fd, F_SETFL, fl | O_NONBLOCK);
}

/* ---- now_ns monotonic ---- */
static void test_now_ns_monotonic(void) {
    uint64_t a = tlang_now_ns();
    uint64_t b = tlang_now_ns();
    CHECK(b >= a);
    CHECK(a != 0);
}

/* ---- wait_fd on a pipe ---- */
static int g_pipe[2];
static int g_wait_result;

static void waiter_fiber(Fiber* self, void* arg) {
    (void)arg;
    g_wait_result = tlang_wait_fd(self, g_pipe[0], EPOLLIN, 0);
}

static void writer_fiber(Fiber* self, void* arg) {
    (void)self;
    (void)arg;
    /* Write a byte so the waiter's fd becomes readable. */
    ssize_t n = write(g_pipe[1], "x", 1);
    (void)n;
}

static void test_wait_fd_readable(void) {
    sched_up();
    CHECK(pipe(g_pipe) == 0);
    set_nonblock(g_pipe[0]);
    set_nonblock(g_pipe[1]);
    g_wait_result = -999;

    /* Spawn the waiter first (parks on the pipe), then the writer. */
    CHECK(tlang_spawn(&g_sched, waiter_fiber, NULL) != NULL);
    CHECK(tlang_spawn(&g_sched, writer_fiber, NULL) != NULL);
    tlang_sched_run(&g_sched);

    CHECK(g_wait_result > 0);               /* a positive epoll mask */
    CHECK(g_wait_result & EPOLLIN);         /* the requested readiness */
    close(g_pipe[0]);
    close(g_pipe[1]);
    sched_down();
}

/* ---- wait_fd timeout ---- */
static int g_timeout_result;
static void timeout_waiter(Fiber* self, void* arg) {
    (void)arg;
    /* A pipe with no writer: only the deadline can wake us. */
    uint64_t deadline = tlang_now_ns() + 20ull * 1000000ull;  /* 20 ms */
    g_timeout_result = tlang_wait_fd(self, g_pipe[0], EPOLLIN, deadline);
}

static void test_wait_fd_timeout(void) {
    sched_up();
    CHECK(pipe(g_pipe) == 0);
    set_nonblock(g_pipe[0]);
    set_nonblock(g_pipe[1]);
    g_timeout_result = -999;
    CHECK(tlang_spawn(&g_sched, timeout_waiter, NULL) != NULL);
    tlang_sched_run(&g_sched);
    CHECK(g_timeout_result == TLANG_WAIT_TIMEOUT);
    close(g_pipe[0]);
    close(g_pipe[1]);
    sched_down();
}

/* ---- sleep_until ---- */
static int g_sleep_result;
static uint64_t g_sleep_elapsed;
static void sleeper(Fiber* self, void* arg) {
    (void)arg;
    uint64_t t0 = tlang_now_ns();
    uint64_t deadline = t0 + 15ull * 1000000ull;  /* 15 ms */
    g_sleep_result = tlang_sleep_until(self, deadline);
    g_sleep_elapsed = tlang_now_ns() - t0;
}

static void test_sleep_until(void) {
    sched_up();
    g_sleep_result = -999;
    CHECK(tlang_spawn(&g_sched, sleeper, NULL) != NULL);
    tlang_sched_run(&g_sched);
    CHECK(g_sleep_result == TLANG_WAIT_TIMEOUT);
    CHECK(g_sleep_elapsed >= 10ull * 1000000ull);  /* actually waited */
    sched_down();
}

/* ---- wait_fd2 peer-gone ---- */
static int g_peer_sock[2];      /* socketpair: [0] watched client, [1] its peer */
static int g_data_pipe[2];      /* a pipe the primary wait never becomes ready on */
static int g_peer_result;

static void peer_waiter(Fiber* self, void* arg) {
    (void)arg;
    uint64_t deadline = tlang_now_ns() + 1000ull * 1000000ull;  /* 1 s safety */
    /* Wait for the (never-ready) data pipe while watching the client socket. */
    g_peer_result = tlang_wait_fd2(self, g_data_pipe[0], EPOLLIN,
                                   g_peer_sock[0], deadline);
}

static void peer_closer(Fiber* self, void* arg) {
    (void)self;
    (void)arg;
    /* Close the peer end: the watched socket reports hangup. */
    close(g_peer_sock[1]);
    g_peer_sock[1] = -1;
}

static void test_wait_fd2_peer_gone(void) {
    sched_up();
    CHECK(socketpair(AF_UNIX, SOCK_STREAM, 0, g_peer_sock) == 0);
    CHECK(pipe(g_data_pipe) == 0);
    set_nonblock(g_peer_sock[0]);
    set_nonblock(g_data_pipe[0]);
    g_peer_result = -999;

    CHECK(tlang_spawn(&g_sched, peer_waiter, NULL) != NULL);
    CHECK(tlang_spawn(&g_sched, peer_closer, NULL) != NULL);
    tlang_sched_run(&g_sched);

    CHECK(g_peer_result == TLANG_WAIT_PEER_GONE);
    close(g_peer_sock[0]);
    if (g_peer_sock[1] >= 0) close(g_peer_sock[1]);
    close(g_data_pipe[0]);
    close(g_data_pipe[1]);
    sched_down();
}

/* ---- wait queue direct hand-off ---- */
static tlang_waitq g_wq;
static int g_wq_result;
static void* g_wq_value;
static int g_marker = 777;

static void wq_waiter(Fiber* self, void* arg) {
    (void)arg;
    g_wq_result = tlang_waitq_wait(&g_wq, self, 0, &g_wq_value);
}

static void wq_waker(Fiber* self, void* arg) {
    (void)self;
    (void)arg;
    /* Hand a value directly to the oldest waiter. */
    bool ok = tlang_waitq_wake_one(&g_wq, &g_marker);
    CHECK(ok);
}

static void test_waitq_handoff(void) {
    sched_up();
    tlang_waitq_init(&g_wq);
    g_wq_result = -999;
    g_wq_value = NULL;
    CHECK(tlang_spawn(&g_sched, wq_waiter, NULL) != NULL);
    CHECK(tlang_spawn(&g_sched, wq_waker, NULL) != NULL);
    tlang_sched_run(&g_sched);
    CHECK(g_wq_result == TLANG_WAIT_OK);
    CHECK(g_wq_value == &g_marker);   /* direct hand-off of the value */
    sched_down();
}

/* ---- wait queue cancel_all ---- */
static tlang_waitq g_wq2;
static int g_cancel_result;
static void wq_cancel_waiter(Fiber* self, void* arg) {
    (void)arg;
    g_cancel_result = tlang_waitq_wait(&g_wq2, self, 0, NULL);
}
static void wq_canceller(Fiber* self, void* arg) {
    (void)self;
    (void)arg;
    tlang_waitq_cancel_all(&g_wq2);
}

static void test_waitq_cancel(void) {
    sched_up();
    tlang_waitq_init(&g_wq2);
    g_cancel_result = -999;
    CHECK(tlang_spawn(&g_sched, wq_cancel_waiter, NULL) != NULL);
    CHECK(tlang_spawn(&g_sched, wq_canceller, NULL) != NULL);
    tlang_sched_run(&g_sched);
    CHECK(g_cancel_result == TLANG_WAIT_CANCELLED);
    sched_down();
}

/* ---- fd_forget clears a slot before close ---- */
static int g_forget_result;
static int g_fp[2];
static void forget_waiter(Fiber* self, void* arg) {
    (void)arg;
    uint64_t deadline = tlang_now_ns() + 10ull * 1000000ull;
    g_forget_result = tlang_wait_fd(self, g_fp[0], EPOLLIN, deadline);
    /* After the wait returns, the fd slot must be forgotten (not registered),
     * so re-waiting works and nothing is stale. */
}

static void test_fd_forget_after_wait(void) {
    sched_up();
    CHECK(pipe(g_fp) == 0);
    set_nonblock(g_fp[0]);
    set_nonblock(g_fp[1]);
    CHECK(tlang_spawn(&g_sched, forget_waiter, NULL) != NULL);
    tlang_sched_run(&g_sched);
    CHECK(g_forget_result == TLANG_WAIT_TIMEOUT);
    /* The slot was cleared when the fiber was woken. */
    if ((size_t)g_fp[0] < g_sched.nfds) {
        CHECK(!g_sched.fds[g_fp[0]].registered);
        CHECK(g_sched.fds[g_fp[0]].waiter == NULL);
    }
    close(g_fp[0]);
    close(g_fp[1]);
    sched_down();
}

int main(void) {
    test_now_ns_monotonic();
    test_wait_fd_readable();
    test_wait_fd_timeout();
    test_sleep_until();
    test_wait_fd2_peer_gone();
    test_waitq_handoff();
    test_waitq_cancel();
    test_fd_forget_after_wait();

    if (failures != 0) {
        printf("FAIL test_sched (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_sched\n");
    return 0;
}
