/*
 * test_console.c - unit tests for src/console.c. console.* and tlang_log_*
 * write to real fds; the test redirects a pipe over the fd, runs the call,
 * and reads back the bytes. console.c allocates nothing from the arena, so no
 * arena/alloc stand-ins are needed.
 */
#include "tlang_internal.h"

#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

static int failures;

#define CHECK(cond)                                                                \
    do {                                                                           \
        if (!(cond)) {                                                             \
            fprintf(stderr, "%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            failures++;                                                            \
        }                                                                          \
    } while (0)

/* Runs `body`, capturing everything written to fd (STDOUT_FILENO or
 * STDERR_FILENO) into out (NUL-terminated, truncated to outlen). */
typedef void (*body_fn)(void);

static size_t capture_fd(int fd, body_fn body, char* out, size_t outlen) {
    int pipefd[2];
    int saved;
    size_t total = 0;
    ssize_t r;

    if (pipe(pipefd) != 0) { perror("pipe"); exit(2); }
    saved = dup(fd);
    if (saved < 0) { perror("dup"); exit(2); }
    if (dup2(pipefd[1], fd) < 0) { perror("dup2"); exit(2); }
    close(pipefd[1]);

    body();

    fflush(NULL);
    if (dup2(saved, fd) < 0) { perror("dup2 restore"); exit(2); }
    close(saved);

    while ((r = read(pipefd[0], out + total, outlen - 1 - total)) > 0) {
        total += (size_t)r;
        if (total >= outlen - 1) break;
    }
    close(pipefd[0]);
    out[total] = '\0';
    return total;
}

/* ---- bodies ---- */

static void body_log_mixed(void) {
    tlang_value args[6] = {
        TLANG_VAL_STR(TLANG_STR("x")),
        TLANG_VAL_I32(-7),
        TLANG_VAL_I64(INT64_MIN),
        TLANG_VAL_F64(0.5),
        TLANG_VAL_BOOL(true),
        TLANG_VAL_NULL,
    };
    tlang_console_log(NULL, args, 6);
}

static void body_log_empty(void) {
    tlang_console_log(NULL, NULL, 0);
}

static char g_long_src[4096];
static void body_log_long(void) {
    tlang_string s;
    tlang_value a;
    s.data = g_long_src;
    s.len = sizeof(g_long_src);
    a = TLANG_VAL_STR(s);
    tlang_console_log(NULL, &a, 1);  /* exceeds the 1 KiB stack buffer -> malloc path */
}

static void body_err(void) {
    tlang_value a = TLANG_VAL_STR(TLANG_STR("oops"));
    tlang_console_error(NULL, &a, 1);
}

static void body_log_warn(void) {
    tlang_log_error("code %d", 42);
    tlang_log_warn("careful");
}

/* ---- tests ---- */

static void test_log_mixed(void) {
    char buf[256];
    capture_fd(STDOUT_FILENO, body_log_mixed, buf, sizeof buf);
    CHECK(strcmp(buf, "x -7 -9223372036854775808 0.5 true null\n") == 0);
}

static void test_log_empty(void) {
    char buf[16];
    size_t n = capture_fd(STDOUT_FILENO, body_log_empty, buf, sizeof buf);
    CHECK(n == 1 && buf[0] == '\n');
}

static void test_log_long(void) {
    char buf[8192];
    size_t n;
    size_t i;
    for (i = 0; i < sizeof(g_long_src); i++) g_long_src[i] = 'Z';
    n = capture_fd(STDOUT_FILENO, body_log_long, buf, sizeof buf);
    CHECK(n == sizeof(g_long_src) + 1);  /* content + newline */
    CHECK(buf[n - 1] == '\n');
    for (i = 0; i < sizeof(g_long_src); i++) CHECK(buf[i] == 'Z');
}

static void test_error(void) {
    char buf[64];
    capture_fd(STDERR_FILENO, body_err, buf, sizeof buf);
    CHECK(strcmp(buf, "oops\n") == 0);
}

static void test_log_lines(void) {
    char buf[128];
    capture_fd(STDERR_FILENO, body_log_warn, buf, sizeof buf);
    CHECK(strcmp(buf, "tlang: error: code 42\ntlang: warning: careful\n") == 0);
}

int main(void) {
    test_log_mixed();
    test_log_empty();
    test_log_long();
    test_error();
    test_log_lines();

    if (failures != 0) {
        printf("FAIL test_console (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_console\n");
    return 0;
}
