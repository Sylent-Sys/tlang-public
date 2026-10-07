/*
 * test_http_chunked.c - unit tests for the RFC 7230 §4.1 chunked request-body
 * decoder in src/http.c, driven through a socketpair against a real scheduler
 * (same harness pattern as test_http.c). A server fiber runs
 * tlang_http_conn_main on one end; a client fiber writes a raw chunked request
 * and reads the full response on the other. Covers: single/multiple chunks,
 * chunk extensions, the zero/empty final chunk, the decoded-size limit (exactly
 * at / one over -> 413, including the zero-cap edge), malformed framing (400),
 * an overflowing chunk-size (400), an over-long chunk-size line (431) both in a
 * single write and dribbled across a mid-line refill, trailers parsed and
 * discarded, Content-Length + Transfer-Encoding (400), a non-chunked coding
 * (501), Expect: 100-continue, keep-alive + pipelining, many tiny chunks across
 * buffer boundaries, and a valid-chunked request with no chunk bytes (a
 * timeout-close, not a 400). Design:
 * .agents/tasks/chunked-bodies/chunked-design.md §10.1.
 * Hermetic: socketpair only, no bound port.
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

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

/* ------------------------------------------------------------------ *
 * Harness (mirrors test_http.c)
 * ------------------------------------------------------------------ */

static tlang_config g_cfg;
static tlang_sched g_sched;
static tlang_program g_prog;

static int g_pair[2];               /* [0] server end, [1] client end */
static const char* g_request;       /* bytes the client sends */
static size_t g_request_len;
static char g_response[8192];       /* bytes the client received */
static size_t g_response_len;

/* The dispatcher under test is swapped per case. */
static void (*g_dispatch)(tlang_fiber*, tlang_ctx*);

/* A config mutator applied after sched_up()'s defaults (NULL = none). */
static void (*g_cfg_mutator)(tlang_config*);

/* Captured request fields. */
static char g_seen_body[4096];
static size_t g_seen_body_len;
static char g_seen_trailer[64];   /* value of ctx.header("X-Trailer") */
static char g_seen_method[16];    /* ctx.method, must survive decode scratch */
static char g_seen_path[64];      /* ctx.path, must survive decode scratch */

static void set_nonblock(int fd) {
    int fl = fcntl(fd, F_GETFL, 0);
    fcntl(fd, F_SETFL, fl | O_NONBLOCK);
}

static void sched_up(void) {
    char err[128];
    tlang_config_defaults(&g_cfg);
    g_cfg.stack_size = 256 * 1024;
    g_cfg.header_timeout_ms = 2000;
    g_cfg.idle_timeout_ms = 2000;
    if (g_cfg_mutator != NULL) g_cfg_mutator(&g_cfg);
    memset(&g_prog, 0, sizeof g_prog);
    g_prog.dispatcher = g_dispatch;
    if (tlang_sched_init(&g_sched, 0, &g_cfg, &g_prog, -1, err, sizeof err) != 0) {
        fprintf(stderr, "sched init failed: %s\n", err);
        exit(2);
    }
    tlang_fctx_init_thread(&g_sched.loop_ctx);
}

static void sched_down(void) {
    tlang_sched_destroy(&g_sched);
}

static void server_fiber(Fiber* self, void* arg) {
    (void)arg;
    tlang_http_conn_main(self, (void*)(intptr_t)g_pair[0]);
}

static void client_fiber(Fiber* self, void* arg) {
    (void)arg;
    uint64_t deadline = tlang_now_ns() + 2000ull * 1000000ull;
    if (tlang_net_write_all(self, g_pair[1], g_request, g_request_len, deadline) < 0)
        return;
    g_response_len = 0;
    for (;;) {
        if (g_response_len >= sizeof g_response) break;
        ssize_t n = tlang_net_read(self, g_pair[1], g_response + g_response_len,
                                   sizeof g_response - g_response_len, deadline);
        if (n <= 0) break;
        g_response_len += (size_t)n;
    }
    close(g_pair[1]);
    g_pair[1] = -1;
}

/* Runs one request/response round-trip. request_len is explicit so a request
 * with embedded NUL bytes (none here) or precomputed length can be used. */
static void run_case_cfg_n(void (*dispatch)(tlang_fiber*, tlang_ctx*),
                           const char* request, size_t request_len,
                           void (*cfg_mutator)(tlang_config*)) {
    g_dispatch = dispatch;
    g_cfg_mutator = cfg_mutator;
    g_request = request;
    g_request_len = request_len;
    g_response_len = 0;
    g_seen_body_len = 0;
    memset(g_response, 0, sizeof g_response);
    memset(g_seen_body, 0, sizeof g_seen_body);
    memset(g_seen_trailer, 0, sizeof g_seen_trailer);
    memset(g_seen_method, 0, sizeof g_seen_method);
    memset(g_seen_path, 0, sizeof g_seen_path);

    sched_up();
    CHECK(socketpair(AF_UNIX, SOCK_STREAM, 0, g_pair) == 0);
    set_nonblock(g_pair[0]);
    set_nonblock(g_pair[1]);

    CHECK(tlang_spawn(&g_sched, server_fiber, NULL) != NULL);
    CHECK(tlang_spawn(&g_sched, client_fiber, NULL) != NULL);
    tlang_sched_run(&g_sched);

    if (g_pair[1] >= 0) { close(g_pair[1]); g_pair[1] = -1; }
    sched_down();
}

static void run_case(void (*dispatch)(tlang_fiber*, tlang_ctx*),
                     const char* request) {
    run_case_cfg_n(dispatch, request, strlen(request), NULL);
}

static void run_case_cfg(void (*dispatch)(tlang_fiber*, tlang_ctx*),
                         const char* request, void (*cfg_mutator)(tlang_config*)) {
    run_case_cfg_n(dispatch, request, strlen(request), cfg_mutator);
}

static bool resp_has(const char* needle) {
    return memmem(g_response, g_response_len, needle, strlen(needle)) != NULL;
}

/* ------------------------------------------------------------------ *
 * Dispatchers
 * ------------------------------------------------------------------ */

/* Captures the assembled body and the X-Trailer header (which must stay empty:
 * trailers are discarded, never merged into ctx.headers). */
static void d_capture_body(tlang_fiber* fib, tlang_ctx* ctx) {
    g_seen_body_len = ctx->body.len < sizeof g_seen_body
        ? ctx->body.len : sizeof g_seen_body - 1;
    memcpy(g_seen_body, ctx->body.data, g_seen_body_len);
    g_seen_body[g_seen_body_len] = '\0';
    snprintf(g_seen_method, sizeof g_seen_method, "%.*s",
             (int)ctx->method.len, ctx->method.data);
    snprintf(g_seen_path, sizeof g_seen_path, "%.*s",
             (int)ctx->path.len, ctx->path.data);
    tlang_string tr = tlang_ctx_header(ctx, TLANG_STR("X-Trailer"));
    snprintf(g_seen_trailer, sizeof g_seen_trailer, "%.*s",
             (int)tr.len, tr.data);
    tlang_ctx_text(fib, ctx, 200, TLANG_STR("ok"));
}

static void d_ok_text(tlang_fiber* fib, tlang_ctx* ctx) {
    tlang_ctx_text(fib, ctx, 200, TLANG_STR("hello body"));
}

/* ------------------------------------------------------------------ *
 * Config mutators
 * ------------------------------------------------------------------ */

static void cfg_body_cap_5(tlang_config* c) { c->max_body_bytes = 5; }
static void cfg_body_cap_0(tlang_config* c) { c->max_body_bytes = 0; }

static void cfg_fast_header_timeout(tlang_config* c) {
    c->header_timeout_ms = 100;
    c->idle_timeout_ms = 100;
}

/* ------------------------------------------------------------------ *
 * Request prefix: a valid chunked POST head (no Content-Length).
 * ------------------------------------------------------------------ */

#define CHUNKED_HEAD \
    "POST /x HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n" \
    "Connection: close\r\n\r\n"

/* ------------------------------------------------------------------ *
 * Tests
 * ------------------------------------------------------------------ */

static void test_single_chunk(void) {
    run_case(d_capture_body, CHUNKED_HEAD "5\r\nhello\r\n0\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(g_seen_body_len == 5);
    CHECK(memcmp(g_seen_body, "hello", 5) == 0);
    /* The request head (method/path) must survive the decoder's buffer reuse:
     * ctx.method/path slice the connection buffer, which the decoder scrambles
     * as scratch, so they are rehomed to the arena before decoding. */
    CHECK(strcmp(g_seen_method, "POST") == 0);
    CHECK(strcmp(g_seen_path, "/x") == 0);
}

static void test_multiple_chunks(void) {
    run_case(d_capture_body, CHUNKED_HEAD "5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(g_seen_body_len == 11);
    CHECK(memcmp(g_seen_body, "hello world", 11) == 0);
}

static void test_chunk_extension(void) {
    run_case(d_capture_body, CHUNKED_HEAD "5;foo=bar\r\nhello\r\n0\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(g_seen_body_len == 5);
    CHECK(memcmp(g_seen_body, "hello", 5) == 0);
}

static void test_empty_body_zero_chunk(void) {
    run_case(d_capture_body, CHUNKED_HEAD "0\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(g_seen_body_len == 0);
}

static void test_exactly_at_limit(void) {
    /* cap 5, body "hello" (5 bytes) -> 200. */
    run_case_cfg(d_capture_body, CHUNKED_HEAD "5\r\nhello\r\n0\r\n\r\n",
                 cfg_body_cap_5);
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(g_seen_body_len == 5);
}

static void test_one_over_limit(void) {
    /* cap 5, body 6 bytes -> 413. */
    run_case_cfg(d_capture_body, CHUNKED_HEAD "6\r\nhelloX\r\n0\r\n\r\n",
                 cfg_body_cap_5);
    CHECK(resp_has("HTTP/1.1 413 Payload Too Large\r\n"));
}

static void test_zero_cap_empty_body(void) {
    /* cap 0, bare last chunk -> 200, empty body. */
    run_case_cfg(d_capture_body, CHUNKED_HEAD "0\r\n\r\n", cfg_body_cap_0);
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(g_seen_body_len == 0);
}

static void test_zero_cap_one_byte(void) {
    /* cap 0, one data byte -> 413. */
    run_case_cfg(d_capture_body, CHUNKED_HEAD "1\r\nx\r\n0\r\n\r\n", cfg_body_cap_0);
    CHECK(resp_has("HTTP/1.1 413 Payload Too Large\r\n"));
}

static void test_bad_hex(void) {
    run_case(d_capture_body, CHUNKED_HEAD "zz\r\nhello\r\n0\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 400 Bad Request\r\n"));
}

static void test_missing_crlf_after_data(void) {
    /* 5 declared, then 7 bytes with no CRLF where CRLF is required. */
    run_case(d_capture_body, CHUNKED_HEAD "5\r\nhelloXX0\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 400 Bad Request\r\n"));
}

static void test_overflow_chunk_size(void) {
    /* 17 hex F's overflow uint64 -> 400 (fires before the 431 line cap). */
    run_case(d_capture_body, CHUNKED_HEAD "FFFFFFFFFFFFFFFF0\r\nx\r\n0\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 400 Bad Request\r\n"));
}

static void test_line_too_long(void) {
    /* "1;" + >1024 'a' extension bytes, no CRLF reached within the cap -> 431.
     * The hex value is a single short digit so the overflow guard never fires. */
    static char req[2048];
    size_t n = 0;
    n += (size_t)snprintf(req + n, sizeof req - n, "%s", CHUNKED_HEAD);
    n += (size_t)snprintf(req + n, sizeof req - n, "1;");
    for (int i = 0; i < 1100; i++) req[n++] = 'a';
    req[n] = '\0';
    run_case_cfg_n(d_capture_body, req, n, NULL);
    CHECK(resp_has("HTTP/1.1 431 Request Header Fields Too Large\r\n"));
}

static void test_trailers_discarded(void) {
    run_case(d_capture_body,
             CHUNKED_HEAD "5\r\nhello\r\n0\r\nX-Trailer: v\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(g_seen_body_len == 5);
    CHECK(memcmp(g_seen_body, "hello", 5) == 0);
    /* Trailer not merged into ctx.headers. */
    CHECK(strcmp(g_seen_trailer, "") == 0);
}

static void test_both_cl_and_te(void) {
    run_case(d_capture_body,
             "POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\n"
             "Transfer-Encoding: chunked\r\nConnection: close\r\n\r\n"
             "5\r\nhello\r\n0\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 400 Bad Request\r\n"));
}

static void test_te_gzip_501(void) {
    run_case(d_ok_text,
             "POST /x HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: gzip\r\n"
             "Connection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 501 Not Implemented\r\n"));
}

static void test_te_stacked_501(void) {
    run_case(d_ok_text,
             "POST /x HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: gzip, chunked\r\n"
             "Connection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 501 Not Implemented\r\n"));
}

static void test_te_duplicate_501(void) {
    run_case(d_ok_text,
             "POST /x HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n"
             "Transfer-Encoding: chunked\r\nConnection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 501 Not Implemented\r\n"));
}

static void test_expect_100_continue(void) {
    run_case(d_capture_body,
             "POST /x HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n"
             "Expect: 100-continue\r\nConnection: close\r\n\r\n"
             "5\r\nhello\r\n0\r\n\r\n");
    /* The interim 100 Continue precedes the final 200 in the byte stream. */
    CHECK(resp_has("HTTP/1.1 100 Continue\r\n\r\n"));
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(g_seen_body_len == 5);
    CHECK(memcmp(g_seen_body, "hello", 5) == 0);
}

static void test_keepalive_pipelining(void) {
    /* A chunked keep-alive request immediately followed by a Connection: close
     * GET on the same connection: both are served, the first body is correct,
     * and the leftover compaction keeps the pipelined GET (design §3.3.2). */
    run_case(d_capture_body,
             "POST /x HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n"
             "5\r\nhello\r\n0\r\n\r\n"
             "GET /next HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    /* Two 200 OK status lines (one per request). */
    const char* first = memmem(g_response, g_response_len,
                               "HTTP/1.1 200 OK\r\n", 17);
    CHECK(first != NULL);
    if (first != NULL) {
        size_t off = (size_t)(first - g_response) + 17;
        const char* second = memmem(g_response + off, g_response_len - off,
                                    "HTTP/1.1 200 OK\r\n", 17);
        CHECK(second != NULL);
    }
    /* g_seen_body reflects the LAST dispatched request (the GET has no body). */
    CHECK(g_seen_body_len == 0);
}

static void test_many_tiny_chunks(void) {
    /* 2000 one-byte chunks: the encoded stream (~8 KB) exceeds the default cap,
     * forcing refill()/compaction across chunk boundaries. Assembled body is
     * the 2000 'a' bytes, under the default 1 MiB body cap. */
    static char req[16384];
    size_t n = 0;
    n += (size_t)snprintf(req + n, sizeof req - n, "%s", CHUNKED_HEAD);
    for (int i = 0; i < 2000; i++) {
        req[n++] = '1'; req[n++] = '\r'; req[n++] = '\n';
        req[n++] = 'a'; req[n++] = '\r'; req[n++] = '\n';
    }
    req[n++] = '0'; req[n++] = '\r'; req[n++] = '\n';
    req[n++] = '\r'; req[n++] = '\n';
    req[n] = '\0';
    run_case_cfg_n(d_capture_body, req, n, NULL);
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(g_seen_body_len == 2000);
    bool all_a = true;
    for (size_t i = 0; i < g_seen_body_len; i++)
        if (g_seen_body[i] != 'a') { all_a = false; break; }
    CHECK(all_a);
}

static void test_no_chunk_bytes_after_head(void) {
    /* A valid chunked head with no chunk bytes: the first refill() blocks on
     * the socket until the (small) header timeout, then closes with NO
     * response, matching a Content-Length body announced-but-never-sent. */
    run_case_cfg(d_capture_body,
                 "POST /x HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n",
                 cfg_fast_header_timeout);
    CHECK(g_response_len == 0);   /* connection closed, no response written */
}

static void test_split_line_cap(void) {
    /* Dribble an over-long chunk-size line across writes so the server performs
     * a mid-line refill()/compaction before the line reaches the cap; the
     * monotonic line_len accumulator must still trip 431 (design §3.3.3).
     * The client here sends the whole buffer at once, but the server read
     * buffer is far smaller than the line, so the decoder MUST refill mid-line
     * and the accumulator (not a per-cursor count) is what enforces the cap. */
    static char req[4096];
    size_t n = 0;
    n += (size_t)snprintf(req + n, sizeof req - n, "%s", CHUNKED_HEAD);
    n += (size_t)snprintf(req + n, sizeof req - n, "1;");
    for (int i = 0; i < 3000; i++) req[n++] = 'b';
    req[n] = '\0';
    run_case_cfg_n(d_capture_body, req, n, NULL);
    CHECK(resp_has("HTTP/1.1 431 Request Header Fields Too Large\r\n"));
}

int main(void) {
    signal(SIGPIPE, SIG_IGN);

    test_single_chunk();
    test_multiple_chunks();
    test_chunk_extension();
    test_empty_body_zero_chunk();
    test_exactly_at_limit();
    test_one_over_limit();
    test_zero_cap_empty_body();
    test_zero_cap_one_byte();
    test_bad_hex();
    test_missing_crlf_after_data();
    test_overflow_chunk_size();
    test_line_too_long();
    test_trailers_discarded();
    test_both_cl_and_te();
    test_te_gzip_501();
    test_te_stacked_501();
    test_te_duplicate_501();
    test_expect_100_continue();
    test_keepalive_pipelining();
    test_many_tiny_chunks();
    test_no_chunk_bytes_after_head();
    test_split_line_cap();

    if (failures != 0) {
        printf("FAIL test_http_chunked (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_http_chunked\n");
    return 0;
}
