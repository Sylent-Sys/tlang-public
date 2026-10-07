/*
 * test_net.c - unit tests for src/net.c socket I/O driven by a real
 * scheduler. Covers: tlang_net_listen on an ephemeral port + local_port;
 * tlang_net_read parking on EAGAIN until data arrives (via socketpair);
 * tlang_net_read returning 0 at end of stream; tlang_net_write_all parking on
 * EAGAIN and completing a large write; tlang_net_writev_all gathering; and
 * tlang_net_write_all reporting TLANG_IO_ERR (EPIPE) when the peer is closed.
 * All sockets are socketpairs or an ephemeral localhost port: hermetic.
 */
#include "tlang_internal.h"

#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/socket.h>
#include <sys/uio.h>

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

/* ---- listen + local_port ---- */
static void test_listen_local_port(void) {
    char err[128];
    int fd = tlang_net_listen("127.0.0.1", 0, err, sizeof err);
    CHECK(fd >= 0);
    if (fd >= 0) {
        int port = tlang_net_local_port(fd);
        CHECK(port > 0);
        /* The socket is non-blocking and close-on-exec. */
        int fl = fcntl(fd, F_GETFL, 0);
        CHECK((fl & O_NONBLOCK) != 0);
        int fdfl = fcntl(fd, F_GETFD, 0);
        CHECK((fdfl & FD_CLOEXEC) != 0);
        close(fd);
    }
}

/* ---- read parks on EAGAIN then wakes with data ---- */
static int g_rd_pair[2];
static ssize_t g_rd_result;
static char g_rd_buf[16];

static void reader_fiber(Fiber* self, void* arg) {
    (void)arg;
    uint64_t deadline = tlang_now_ns() + 1000ull * 1000000ull;  /* 1s safety */
    g_rd_result = tlang_net_read(self, g_rd_pair[0], g_rd_buf, sizeof g_rd_buf, deadline);
}

static void writer_fiber(Fiber* self, void* arg) {
    (void)self;
    (void)arg;
    /* The reader parks first (empty socket); send some bytes to wake it. */
    ssize_t n = write(g_rd_pair[1], "hello", 5);
    (void)n;
}

static void test_read_parks_then_wakes(void) {
    sched_up();
    CHECK(socketpair(AF_UNIX, SOCK_STREAM, 0, g_rd_pair) == 0);
    set_nonblock(g_rd_pair[0]);
    set_nonblock(g_rd_pair[1]);
    g_rd_result = -999;
    memset(g_rd_buf, 0, sizeof g_rd_buf);

    CHECK(tlang_spawn(&g_sched, reader_fiber, NULL) != NULL);
    CHECK(tlang_spawn(&g_sched, writer_fiber, NULL) != NULL);
    tlang_sched_run(&g_sched);

    CHECK(g_rd_result == 5);
    CHECK(memcmp(g_rd_buf, "hello", 5) == 0);
    close(g_rd_pair[0]);
    close(g_rd_pair[1]);
    sched_down();
}

/* ---- read returns 0 at EOF ---- */
static int g_eof_pair[2];
static ssize_t g_eof_result;

static void eof_reader(Fiber* self, void* arg) {
    (void)arg;
    uint64_t deadline = tlang_now_ns() + 1000ull * 1000000ull;
    char b[8];
    g_eof_result = tlang_net_read(self, g_eof_pair[0], b, sizeof b, deadline);
}

static void eof_closer(Fiber* self, void* arg) {
    (void)self;
    (void)arg;
    close(g_eof_pair[1]);   /* peer closes: the reader sees EOF */
    g_eof_pair[1] = -1;
}

static void test_read_eof(void) {
    sched_up();
    CHECK(socketpair(AF_UNIX, SOCK_STREAM, 0, g_eof_pair) == 0);
    set_nonblock(g_eof_pair[0]);
    set_nonblock(g_eof_pair[1]);
    g_eof_result = -999;

    CHECK(tlang_spawn(&g_sched, eof_reader, NULL) != NULL);
    CHECK(tlang_spawn(&g_sched, eof_closer, NULL) != NULL);
    tlang_sched_run(&g_sched);

    CHECK(g_eof_result == 0);   /* end of stream */
    close(g_eof_pair[0]);
    if (g_eof_pair[1] >= 0) close(g_eof_pair[1]);
    sched_down();
}

/* ---- write_all completes a large write with the reader draining ---- */
#define BIG 200000
static int g_wr_pair[2];
static ssize_t g_wr_result;
static char* g_wr_data;
static size_t g_drained;

static void big_writer(Fiber* self, void* arg) {
    (void)arg;
    uint64_t deadline = tlang_now_ns() + 2000ull * 1000000ull;  /* 2s safety */
    g_wr_result = tlang_net_write_all(self, g_wr_pair[0], g_wr_data, BIG, deadline);
}

static void big_drainer(Fiber* self, void* arg) {
    (void)arg;
    char tmp[4096];
    uint64_t deadline = tlang_now_ns() + 2000ull * 1000000ull;
    while (g_drained < BIG) {
        ssize_t n = tlang_net_read(self, g_wr_pair[1], tmp, sizeof tmp, deadline);
        if (n <= 0) break;
        g_drained += (size_t)n;
    }
}

static void test_write_all_large(void) {
    sched_up();
    CHECK(socketpair(AF_UNIX, SOCK_STREAM, 0, g_wr_pair) == 0);
    set_nonblock(g_wr_pair[0]);
    set_nonblock(g_wr_pair[1]);
    g_wr_data = (char*)malloc(BIG);
    CHECK(g_wr_data != NULL);
    for (int i = 0; i < BIG; i++) g_wr_data[i] = (char)(i & 0xff);
    g_wr_result = -999;
    g_drained = 0;

    CHECK(tlang_spawn(&g_sched, big_writer, NULL) != NULL);
    CHECK(tlang_spawn(&g_sched, big_drainer, NULL) != NULL);
    tlang_sched_run(&g_sched);

    CHECK(g_wr_result == BIG);
    CHECK(g_drained == BIG);
    free(g_wr_data);
    g_wr_data = NULL;
    close(g_wr_pair[0]);
    close(g_wr_pair[1]);
    sched_down();
}

/* ---- writev_all gathers multiple buffers ---- */
static int g_iov_pair[2];
static ssize_t g_iov_result;
static char g_iov_got[32];
static size_t g_iov_got_len;

static void iov_writer(Fiber* self, void* arg) {
    (void)arg;
    struct iovec iov[3];
    iov[0].iov_base = (void*)"abc";
    iov[0].iov_len = 3;
    iov[1].iov_base = (void*)"de";
    iov[1].iov_len = 2;
    iov[2].iov_base = (void*)"fghi";
    iov[2].iov_len = 4;
    uint64_t deadline = tlang_now_ns() + 1000ull * 1000000ull;
    g_iov_result = tlang_net_writev_all(self, g_iov_pair[0], iov, 3, deadline);
}

static void iov_reader(Fiber* self, void* arg) {
    (void)arg;
    uint64_t deadline = tlang_now_ns() + 1000ull * 1000000ull;
    while (g_iov_got_len < 9) {
        ssize_t n = tlang_net_read(self, g_iov_pair[1], g_iov_got + g_iov_got_len,
                                   sizeof g_iov_got - g_iov_got_len, deadline);
        if (n <= 0) break;
        g_iov_got_len += (size_t)n;
    }
}

static void test_writev_all(void) {
    sched_up();
    CHECK(socketpair(AF_UNIX, SOCK_STREAM, 0, g_iov_pair) == 0);
    set_nonblock(g_iov_pair[0]);
    set_nonblock(g_iov_pair[1]);
    g_iov_result = -999;
    g_iov_got_len = 0;
    memset(g_iov_got, 0, sizeof g_iov_got);

    CHECK(tlang_spawn(&g_sched, iov_writer, NULL) != NULL);
    CHECK(tlang_spawn(&g_sched, iov_reader, NULL) != NULL);
    tlang_sched_run(&g_sched);

    CHECK(g_iov_result == 9);
    CHECK(g_iov_got_len == 9);
    CHECK(memcmp(g_iov_got, "abcdefghi", 9) == 0);
    close(g_iov_pair[0]);
    close(g_iov_pair[1]);
    sched_down();
}

/* ---- write_all reports TLANG_IO_ERR when the peer is gone (EPIPE) ---- */
static int g_pipe_pair[2];
static ssize_t g_pipe_result;

static void pipe_writer(Fiber* self, void* arg) {
    (void)arg;
    /* The peer end is already closed; writing eventually yields EPIPE. The
     * socket buffer may absorb a first chunk, so write enough to force it. */
    static char data[300000];
    uint64_t deadline = tlang_now_ns() + 1000ull * 1000000ull;
    g_pipe_result = tlang_net_write_all(self, g_pipe_pair[0], data, sizeof data, deadline);
}

static void test_write_epipe(void) {
    sched_up();
    CHECK(socketpair(AF_UNIX, SOCK_STREAM, 0, g_pipe_pair) == 0);
    set_nonblock(g_pipe_pair[0]);
    close(g_pipe_pair[1]);     /* peer gone */
    g_pipe_pair[1] = -1;
    g_pipe_result = -999;

    CHECK(tlang_spawn(&g_sched, pipe_writer, NULL) != NULL);
    tlang_sched_run(&g_sched);

    CHECK(g_pipe_result == TLANG_IO_ERR);
    close(g_pipe_pair[0]);
    sched_down();
}

int main(void) {
    signal(SIGPIPE, SIG_IGN);   /* tlang_main does this; mirror it in the test */

    test_listen_local_port();
    test_read_parks_then_wakes();
    test_read_eof();
    test_write_all_large();
    test_writev_all();
    test_write_epipe();

    if (failures != 0) {
        printf("FAIL test_net (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_net\n");
    return 0;
}
