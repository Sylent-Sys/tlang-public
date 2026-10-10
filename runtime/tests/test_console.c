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
    tlang_console_info(NULL, NULL, 0);
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

static void test_structured_record_is_single_line(void) {
    char buf[256];
    tlang_string line = TLANG_STR("{\"timestamp\":\"2026-01-02T03:04:05Z\",\"level\":\"info\",\"message\":\"hello\",\"fields\":{}}\n");
    FILE* tmp = tmpfile();
    CHECK(tmp != NULL);
    if (tmp == NULL) return;
    int saved = dup(STDOUT_FILENO);
    CHECK(saved >= 0);
    if (saved < 0) { fclose(tmp); return; }
    fflush(stdout);
    CHECK(dup2(fileno(tmp), STDOUT_FILENO) >= 0);
    tlang_console_json_write_record(STDOUT_FILENO, line);
    fflush(stdout);
    CHECK(dup2(saved, STDOUT_FILENO) >= 0);
    close(saved);
    rewind(tmp);
    size_t n = fread(buf, 1, sizeof buf - 1, tmp);
    buf[n] = '\0';
    CHECK(n == line.len && memcmp(buf, line.data, line.len) == 0);
    CHECK(strstr(buf, "\"timestamp\":\"2026-") != NULL);
    fclose(tmp);
}

static void test_utf8_escaping(void) {
    MemoryArena arena;
    tlang_fiber fib;
    tlang_buf b;
    const char raw[] = {'a', (char)0xFF, 'b'};
    CHECK(tlang_arena_init(&arena) == 0);
    memset(&fib, 0, sizeof fib); fib.arena = &arena;
    tlang_buf_init(&fib, &b, 32);
    json_write_str_utf8(&b, (tlang_string){raw, sizeof raw});
    CHECK(b.len == 10 && memcmp(b.data, "\"a\\u00ffb\"", 10) == 0);
    tlang_arena_destroy(&arena);
}

typedef struct { int id; } log_thread_arg;
static void* log_concurrently(void* opaque) {
    log_thread_arg* arg = (log_thread_arg*)opaque;
    int i;
    char line[64];
    for (i = 0; i < 50; i++) {
        int n = snprintf(line, sizeof line, "{\"thread\":%d,\"seq\":%d}\n", arg->id, i);
        tlang_console_json_write_record(STDOUT_FILENO, (tlang_string){line, (size_t)n});
    }
    return NULL;
}

static void test_concurrent_records(void) {
    FILE* tmp = tmpfile();
    pthread_t threads[4];
    log_thread_arg args[4];
    int saved, i;
    char line[64];
    CHECK(tmp != NULL); if (tmp == NULL) return;
    saved = dup(STDOUT_FILENO); CHECK(saved >= 0); if (saved < 0) { fclose(tmp); return; }
    CHECK(dup2(fileno(tmp), STDOUT_FILENO) >= 0);
    for (i = 0; i < 4; i++) { args[i].id = i; CHECK(pthread_create(&threads[i], NULL, log_concurrently, &args[i]) == 0); }
    for (i = 0; i < 4; i++) CHECK(pthread_join(threads[i], NULL) == 0);
    fflush(stdout); CHECK(dup2(saved, STDOUT_FILENO) >= 0); close(saved);
    rewind(tmp);
    for (i = 0; i < 200; i++) {
        CHECK(fgets(line, sizeof line, tmp) != NULL);
        CHECK(strchr(line, '\n') != NULL);
    }
    CHECK(fgets(line, sizeof line, tmp) == NULL);
    fclose(tmp);
}

static void body_record(tlang_fiber* fib) {
    tlang_json_value* value = tlang_json_string(fib, TLANG_STR("v\n\""));
    tlang_string keys[2] = {TLANG_STR("level"), TLANG_STR("nested")};
    const tlang_json_value* values[2] = {value, tlang_json_bool(fib, true)};
    tlang_json_value* fields = tlang_json_object(fib, keys, values, 2);
    tlang_console_json(fib, TLANG_LOG_INFO, TLANG_STR("msg\n"), fields);
}

static void test_structured_jsonl(void) {
    char buf[4096];
    MemoryArena arena;
    tlang_fiber fib;
    int pipefd[2], saved;
    ssize_t n;
    CHECK(pipe(pipefd) == 0);
    saved = dup(STDOUT_FILENO);
    CHECK(saved >= 0);
    if (saved < 0) { close(pipefd[0]); close(pipefd[1]); return; }
    CHECK(dup2(pipefd[1], STDOUT_FILENO) >= 0); close(pipefd[1]);
    CHECK(tlang_arena_init(&arena) == 0); memset(&fib, 0, sizeof fib); fib.arena = &arena;
    body_record(&fib);
    CHECK(!fib.err);
    CHECK(dup2(saved, STDOUT_FILENO) >= 0); close(saved);
    n = read(pipefd[0], buf, sizeof buf - 1); close(pipefd[0]);
    if (n < 0) n = 0;
    buf[n] = '\0';
    CHECK(strstr(buf, "\"level\":\"info\"") != NULL);
    CHECK(strstr(buf, "\"message\":\"msg\\n\"") != NULL);
    CHECK(strstr(buf, "\"fields\":{\"level\":\"v\\n\\\"\",\"nested\":true}") != NULL);
    CHECK(strstr(buf, "\"timestamp\":\"20") != NULL && buf[n - 1] == '\n');
    tlang_arena_destroy(&arena);
}

static void test_oversized_record_dropped(void) {
    char* message = (char*)malloc(TLANG_LOG_MAX_RECORD_BYTES + 1);
    FILE* tmp = tmpfile();
    MemoryArena arena;
    tlang_fiber fib;
    int saved;
    CHECK(message != NULL && tmp != NULL);
    if (message == NULL || tmp == NULL) { free(message); if (tmp) fclose(tmp); return; }
    memset(message, 'x', TLANG_LOG_MAX_RECORD_BYTES + 1);
    saved = dup(STDOUT_FILENO);
    CHECK(saved >= 0);
    if (saved < 0) { free(message); fclose(tmp); return; }
    CHECK(dup2(fileno(tmp), STDOUT_FILENO) >= 0);
    CHECK(tlang_arena_init(&arena) == 0); memset(&fib, 0, sizeof fib); fib.arena = &arena;
    tlang_console_json(&fib, TLANG_LOG_INFO, (tlang_string){message, TLANG_LOG_MAX_RECORD_BYTES + 1}, NULL);
    CHECK(!fib.err);
    fflush(stdout); CHECK(dup2(saved, STDOUT_FILENO) >= 0); close(saved);
    rewind(tmp); CHECK(fgetc(tmp) == EOF);
    tlang_arena_destroy(&arena); fclose(tmp); free(message);
}

int main(void) {
    test_log_mixed();
    test_log_empty();
    test_log_long();
    test_error();
    test_log_lines();
    test_structured_record_is_single_line();
    test_utf8_escaping();
    test_concurrent_records();
    test_structured_jsonl();
    test_oversized_record_dropped();

    if (failures != 0) {
        printf("FAIL test_console (%d checks failed)\n", failures);
        return 1;
    }
    printf("PASS test_console\n");
    return 0;
}
