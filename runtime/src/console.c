/*
 * console.c - console.log/console.error and the stderr logger. docs/RUNTIME.md
 * §4/§6 assigns these functions here; the contracts are in tlang.h §13 and
 * tlang_internal.h §10.
 */
#include "tlang_internal.h"

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

/* ------------------------------------------------------------------ *
 * Low-level write with EINTR / short-write retry
 * ------------------------------------------------------------------ */

void tlang_log_write(int fd, const char* data, size_t len) {
    size_t off = 0;
    while (off < len) {
        ssize_t w = write(fd, data + off, len - off);
        if (w < 0) {
            if (errno == EINTR) continue;
            return;  /* give up silently on any other error */
        }
        if (w == 0) return;  /* no progress: give up */
        off += (size_t)w;
    }
}

/* ------------------------------------------------------------------ *
 * console.log / console.error
 * ------------------------------------------------------------------ */

#define CONSOLE_NUM_MAX \
    (TLANG_FMT_F64_MAX > TLANG_FMT_I64_MAX ? TLANG_FMT_F64_MAX : TLANG_FMT_I64_MAX)

/* The source bytes and length one value formats to. For numbers the bytes are
 * written into `num` (at least CONSOLE_NUM_MAX bytes); for strings and literals
 * `*src` borrows the value's own storage. */
static size_t console_value(const tlang_value* v, char* num, const char** src) {
    if (v->is_null || v->kind == TLANG_KIND_NULL) {
        *src = "null";
        return 4;
    }
    switch (v->kind) {
    case TLANG_KIND_STR:
        *src = v->v.str.data != NULL ? v->v.str.data : "";
        return v->v.str.len;
    case TLANG_KIND_I32:
        *src = num;
        return tlang_fmt_i64(num, (int64_t)v->v.i32);
    case TLANG_KIND_I64:
        *src = num;
        return tlang_fmt_i64(num, v->v.i64);
    case TLANG_KIND_F64:
        *src = num;
        return tlang_fmt_f64(num, v->v.f64, false);
    case TLANG_KIND_BOOL:
        *src = v->v.b ? "true" : "false";
        return v->v.b ? 4u : 5u;
    default:
        *src = "";
        return 0;
    }
}

/* Total bytes the formatted line needs (values space-separated, '\n'-end). */
static size_t console_line_size(const tlang_value* args, int nargs) {
    char num[CONSOLE_NUM_MAX];
    size_t total = 0;
    const char* src;
    int i;
    for (i = 0; i < nargs; i++) {
        if (i != 0) total++;  /* separator space */
        total += console_value(&args[i], num, &src);
    }
    total++;  /* trailing newline */
    return total;
}

static void console_write(int fd, const tlang_value* args, int nargs) {
    char stackbuf[1024];
    size_t need = console_line_size(args, nargs);
    char* buf;
    bool heap = false;
    size_t cap;
    size_t pos = 0;
    int i;

    if (need <= sizeof stackbuf) {
        buf = stackbuf;
        cap = sizeof stackbuf;
    } else {
        buf = (char*)malloc(need);
        if (buf == NULL) {
            buf = stackbuf;       /* fall back, truncating the line */
            cap = sizeof stackbuf;
        } else {
            heap = true;
            cap = need;
        }
    }

    /* cap is always >= need when not truncating, and `need` already accounts
     * for every value plus the newline, so writes below never overflow. When
     * truncating onto the stack buffer we clamp each copy to the room left. */
    for (i = 0; i < nargs; i++) {
        char num[CONSOLE_NUM_MAX];
        const char* src;
        size_t n;
        if (i != 0 && pos + 1 < cap) buf[pos++] = ' ';
        n = console_value(&args[i], num, &src);
        if (pos < cap - 1) {
            size_t room = (cap - 1) - pos;  /* keep one byte for '\n' */
            size_t copy = n < room ? n : room;
            memcpy(buf + pos, src, copy);
            pos += copy;
        }
    }
    buf[pos++] = '\n';

    tlang_log_write(fd, buf, pos);
    if (heap) free(buf);
}

void tlang_console_log(tlang_fiber* fib, const tlang_value* args, int nargs) {
    (void)fib;
    console_write(STDOUT_FILENO, args, nargs);
}

void tlang_console_info(tlang_fiber* fib, const tlang_value* args, int nargs) {
    (void)fib;
    console_write(STDOUT_FILENO, args, nargs);
}

void tlang_console_error(tlang_fiber* fib, const tlang_value* args, int nargs) {
    (void)fib;
    console_write(STDERR_FILENO, args, nargs);
}

/* ------------------------------------------------------------------ *
 * stderr logger (never allocates, truncates at 4 KiB)
 * ------------------------------------------------------------------ */

static void log_line(const char* prefix, const char* fmt, va_list ap) {
    char buf[4096];
    size_t plen = strlen(prefix);
    size_t pos;
    int n;

    if (plen >= sizeof buf) plen = sizeof buf - 1;
    memcpy(buf, prefix, plen);
    pos = plen;

    n = vsnprintf(buf + pos, sizeof buf - pos, fmt, ap);
    if (n < 0) {
        /* Formatting error: emit the prefix alone. */
    } else if ((size_t)n < sizeof buf - pos) {
        pos += (size_t)n;
    } else {
        pos = sizeof buf - 1;  /* truncated */
    }

    if (pos >= sizeof buf) pos = sizeof buf - 1;
    buf[pos++] = '\n';
    tlang_log_write(STDERR_FILENO, buf, pos);
}

void tlang_log_error(const char* fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    log_line("tlang: error: ", fmt, ap);
    va_end(ap);
}

void tlang_log_warn(const char* fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    log_line("tlang: warning: ", fmt, ap);
    va_end(ap);
}
