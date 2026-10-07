/*
 * test_http.c - unit tests for src/http.c driven through a socketpair against
 * a real scheduler. A server fiber runs tlang_http_conn_main on one end; a
 * client fiber writes a request and reads the full response on the other.
 * Covers: request parsing reaches the dispatcher (method/path/query/body);
 * response framing (status line, Date, Content-Type, Content-Length,
 * Connection); 404 when the dispatcher sends nothing; an escaped error maps to
 * the right status (in-range used as is, out-of-range -> 500); 499 writes
 * nothing; ctx.setHeader appears in the response; a managed header -> 500;
 * ctx.json sets application/json; HEAD gets headers but no body.
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
 * Harness
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

/* Captured request fields for the "parsing reaches dispatcher" case. */
static char g_seen_method[16];
static char g_seen_path[64];
static char g_seen_query[64];
static char g_seen_body[64];

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
    /* Send the request. */
    if (tlang_net_write_all(self, g_pair[1], g_request, g_request_len, deadline) < 0)
        return;
    /* Read until EOF (the server closes on Connection: close). */
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

/* Runs one request/response round-trip with the given dispatcher. */
static void run_case(void (*dispatch)(tlang_fiber*, tlang_ctx*),
                     const char* request) {
    g_dispatch = dispatch;
    g_request = request;
    g_request_len = strlen(request);
    g_response_len = 0;
    memset(g_response, 0, sizeof g_response);

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

static bool resp_has(const char* needle) {
    return memmem(g_response, g_response_len, needle, strlen(needle)) != NULL;
}

/* ------------------------------------------------------------------ *
 * Dispatchers
 * ------------------------------------------------------------------ */

static void d_ok_text(tlang_fiber* fib, tlang_ctx* ctx) {
    tlang_ctx_text(fib, ctx, 200, TLANG_STR("hello body"));
}

static void d_capture(tlang_fiber* fib, tlang_ctx* ctx) {
    snprintf(g_seen_method, sizeof g_seen_method, "%.*s",
             (int)ctx->method.len, ctx->method.data);
    snprintf(g_seen_path, sizeof g_seen_path, "%.*s",
             (int)ctx->path.len, ctx->path.data);
    snprintf(g_seen_query, sizeof g_seen_query, "%.*s",
             (int)ctx->query.len, ctx->query.data);
    snprintf(g_seen_body, sizeof g_seen_body, "%.*s",
             (int)ctx->body.len, ctx->body.data);
    tlang_ctx_text(fib, ctx, 200, TLANG_STR("ok"));
}

static void d_nothing(tlang_fiber* fib, tlang_ctx* ctx) {
    (void)fib;
    (void)ctx;
    /* Sends no response: the runtime must answer 404. */
}

static void d_throw_403(tlang_fiber* fib, tlang_ctx* ctx) {
    (void)ctx;
    tlang_throw(fib, 403, TLANG_STR("nope"));
}

static void d_throw_700(tlang_fiber* fib, tlang_ctx* ctx) {
    (void)ctx;
    tlang_throw(fib, 700, TLANG_STR("weird status"));  /* out of range -> 500 */
}

static void d_throw_499(tlang_fiber* fib, tlang_ctx* ctx) {
    (void)ctx;
    tlang_throw(fib, 499, TLANG_STR("client gone"));   /* writes nothing */
}

static void d_set_header(tlang_fiber* fib, tlang_ctx* ctx) {
    tlang_ctx_set_header(fib, ctx, TLANG_STR("X-Custom"), TLANG_STR("yes"));
    if (fib->err) return;
    tlang_ctx_text(fib, ctx, 200, TLANG_STR("hi"));
}

static void d_managed_header(tlang_fiber* fib, tlang_ctx* ctx) {
    tlang_ctx_set_header(fib, ctx, TLANG_STR("Content-Length"), TLANG_STR("5"));
    if (fib->err) return;              /* 500 BAD_HEADER expected -> error path */
    tlang_ctx_text(fib, ctx, 200, TLANG_STR("never"));
}

static void d_json(tlang_fiber* fib, tlang_ctx* ctx) {
    tlang_ctx_json(fib, ctx, 201, TLANG_STR("{\"ok\":true}"));
}

static void d_head(tlang_fiber* fib, tlang_ctx* ctx) {
    tlang_ctx_text(fib, ctx, 200, TLANG_STR("body-should-be-absent"));
}

/* ------------------------------------------------------------------ *
 * Tests
 * ------------------------------------------------------------------ */

static void test_ok_response_framing(void) {
    run_case(d_ok_text,
             "GET /hello HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(resp_has("Date: "));
    CHECK(resp_has("Content-Type: text/plain; charset=utf-8\r\n"));
    CHECK(resp_has("Content-Length: 10\r\n"));   /* "hello body" is 10 bytes */
    CHECK(resp_has("Connection: close\r\n"));
    CHECK(resp_has("\r\n\r\nhello body"));
}

static void test_parsing_reaches_dispatcher(void) {
    memset(g_seen_method, 0, sizeof g_seen_method);
    memset(g_seen_path, 0, sizeof g_seen_path);
    memset(g_seen_query, 0, sizeof g_seen_query);
    memset(g_seen_body, 0, sizeof g_seen_body);
    run_case(d_capture,
             "POST /items/5?x=1 HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\n"
             "Connection: close\r\n\r\nabcd");
    CHECK(strcmp(g_seen_method, "POST") == 0);
    CHECK(strcmp(g_seen_path, "/items/5") == 0);
    CHECK(strcmp(g_seen_query, "x=1") == 0);
    CHECK(strcmp(g_seen_body, "abcd") == 0);
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
}

static void test_404_when_nothing_sent(void) {
    run_case(d_nothing,
             "GET /missing HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 404 Not Found\r\n"));
    CHECK(resp_has("Content-Type: text/plain; charset=utf-8\r\n"));
}

static void test_error_in_range(void) {
    run_case(d_throw_403,
             "GET /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 403 Forbidden\r\n"));
}

static void test_error_out_of_range(void) {
    run_case(d_throw_700,
             "GET /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 500 Internal Server Error\r\n"));
}

static void test_error_499_writes_nothing(void) {
    run_case(d_throw_499,
             "GET /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    CHECK(g_response_len == 0);   /* 499 writes nothing, then closes */
}

static void test_set_header(void) {
    run_case(d_set_header,
             "GET /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(resp_has("X-Custom: yes\r\n"));
}

static void test_managed_header_fails(void) {
    run_case(d_managed_header,
             "GET /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    /* setHeader threw 500; the dispatcher returned without sending. */
    CHECK(resp_has("HTTP/1.1 500 Internal Server Error\r\n"));
}

static void test_json_content_type(void) {
    run_case(d_json,
             "GET /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 201 Created\r\n"));
    CHECK(resp_has("Content-Type: application/json\r\n"));
    CHECK(resp_has("\r\n\r\n{\"ok\":true}"));
}

static void test_head_no_body(void) {
    run_case(d_head,
             "HEAD /x HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 200 OK\r\n"));
    CHECK(resp_has("Content-Length: 21\r\n"));   /* length still reported */
    /* No body after the header terminator. */
    const char* sep = memmem(g_response, g_response_len, "\r\n\r\n", 4);
    CHECK(sep != NULL);
    if (sep != NULL) {
        size_t body_off = (size_t)(sep - g_response) + 4;
        CHECK(body_off == g_response_len);       /* nothing follows */
    }
}

/* A non-chunked Transfer-Encoding is still unsupported -> 501. (Chunked
 * request bodies are now decoded; that coverage lives in test_http_chunked.c.) */
static void test_te_gzip_501(void) {
    run_case(d_ok_text,
             "POST /x HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: gzip\r\n"
             "Connection: close\r\n\r\n");
    CHECK(resp_has("HTTP/1.1 501 Not Implemented\r\n"));
}

int main(void) {
    signal(SIGPIPE, SIG_IGN);

    test_ok_response_framing();
    test_parsing_reaches_dispatcher();
    test_404_when_nothing_sent();
    test_error_in_range();
    test_error_out_of_range();
    test_error_499_writes_nothing();
    test_set_header();
    test_managed_header_fails();
    test_json_content_type();
    test_head_no_body();
    test_te_gzip_501();

    if (failures != 0) {
        printf("FAIL test_http (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_http\n");
    return 0;
}
