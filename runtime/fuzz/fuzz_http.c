/*
 * fuzz_http.c - libFuzzer harness for the locked HTTP request parser
 * (the static parse_request in runtime/src/http.c). Developer opt-in; NOT
 * built or run by the Go suite.
 *
 * parse_request is `static`, so this harness reaches it by compiling the real
 * locked translation unit directly:
 *
 *     #include "../src/http.c"
 *
 * This compiles http.c in-place (NOT a copy, and http.c is therefore NOT
 * listed again on the clang command line - see runtime/fuzz/README.md). No
 * runtime file is edited.
 *
 * Because the whole http.c TU is compiled, every external symbol it references
 * must resolve at link time. The minimal transitive deps are linked on the
 * command line (strings.c, slices.c, arena.c, errors.c, router.c, query.c);
 * the remaining references - the net, scheduler, fiber-cleanup and console
 * entry points used only by http.c's connection loop (never by parse_request)
 * - are satisfied by the local stand-ins below. parse_request touches none of
 * them, so a never-called stand-in is enough to link.
 *
 * LLVMFuzzerTestOneInput builds a zeroed tlang_ctx, copies the fuzzer bytes
 * into a request buffer, locates the first CRLFCRLF as head_len (synthesizing
 * a CRLFCRLF-terminated buffer when the input lacks one, so the parser's
 * documented precondition "buf[0..head_len) ends in CRLFCRLF" holds), sets
 * max_headers to the default 32, and calls parse_request.
 *
 * Invariant checked by ASan/UBSan: parse_request never crashes, never reads
 * past head_len, and returns either 0 or a status in 400..599. A crash here is
 * a real bug in the locked runtime/src/http.c -> STOP and raise (design
 * §5.4/§8); the harness never works around it.
 */
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>

/* The real, locked translation unit. parse_request and its static helpers
 * (is_token, trim_ows, mem_find_crlfcrlf, reason_phrase, ...) become visible. */
#include "../src/http.c"

/* ------------------------------------------------------------------ *
 * Link-only stand-ins
 *
 * These satisfy the references http.c's connection loop makes into net.c /
 * sched.c / fiber.c / console.c, which are deliberately NOT on the link line.
 * parse_request (the only thing the harness calls) never reaches any of them,
 * so they only need to exist. If one is ever reached it traps loudly rather
 * than silently returning bogus data.
 * ------------------------------------------------------------------ */

ssize_t tlang_net_read(Fiber* f, int fd, void* buf, size_t len, uint64_t deadline_ns) {
    (void)f; (void)fd; (void)buf; (void)len; (void)deadline_ns;
    __builtin_trap();
}

ssize_t tlang_net_write_all(Fiber* f, int fd, const void* buf, size_t len, uint64_t deadline_ns) {
    (void)f; (void)fd; (void)buf; (void)len; (void)deadline_ns;
    __builtin_trap();
}

void tlang_net_close(tlang_sched* s, int fd) {
    (void)s; (void)fd;
    __builtin_trap();
}

uint64_t tlang_now_ns(void) {
    return 0;
}

void tlang_cleanup_run_all(Fiber* f) {
    (void)f;
    __builtin_trap();
}

_Noreturn void tlang_request_abort(Fiber* f, int32_t status, const char* reason) {
    (void)f; (void)status; (void)reason;
    __builtin_trap();
}

void tlang_log_error(const char* fmt, ...) {
    (void)fmt;
}

void tlang_log_warn(const char* fmt, ...) {
    (void)fmt;
}

/* ------------------------------------------------------------------ *
 * The fuzz target
 * ------------------------------------------------------------------ */

/* Default header limit (TLANG_MAX_HEADERS can lower it, never raise it past
 * TLANG_CTX_MAX_HEADERS == 32). The harness pins the default 32. */
#define FUZZ_MAX_HEADERS 32

int LLVMFuzzerTestOneInput(const uint8_t* data, size_t size) {
    /* parse_request reads buf[0..head_len); keep the whole input available and
     * a little slack for a synthesized terminator. */
    size_t cap = size + 4;
    char* buf = (char*)malloc(cap == 0 ? 1 : cap);
    if (buf == NULL) return 0;
    if (size != 0) memcpy(buf, data, size);

    size_t head_len = 0;
    if (!mem_find_crlfcrlf(buf, size, &head_len)) {
        /* No complete header terminator in the input: synthesize one so the
         * parser precondition holds. The parser is documented to be called
         * only on bytes that already end in CRLFCRLF. */
        buf[size + 0] = '\r';
        buf[size + 1] = '\n';
        buf[size + 2] = '\r';
        buf[size + 3] = '\n';
        head_len = size + 4;
    }

    tlang_ctx ctx;
    memset(&ctx, 0, sizeof ctx);

    size_t content_length = 0;
    bool has_body = false, chunked = false, expect_continue = false;

    int status = parse_request(&ctx, buf, head_len, FUZZ_MAX_HEADERS,
                               &content_length, &has_body, &chunked,
                               &expect_continue);

    /* Invariant: 0 (success) or a 4xx/5xx answer status. */
    if (!(status == 0 || (status >= 400 && status <= 599))) {
        __builtin_trap();
    }

    free(buf);
    return 0;
}
