/*
 * errors.c - throwing and catching. docs/RUNTIME.md §4/§6 assigns these
 * functions here; the contracts are in tlang.h §6 and tlang_internal.h §10.
 */
#include "tlang_internal.h"

#include <stdio.h>

void tlang_throw(tlang_fiber* fib, int32_t status, tlang_string message) {
    fib->err = 1;
    fib->error.status = status;
    if (message.data == NULL) {
        /* A NULL message is stored as "" (tlang.h §6). */
        fib->error.message = TLANG_STR("");
    } else {
        fib->error.message = message;
    }
}

void tlang_throw_value(tlang_fiber* fib, tlang_error err) {
    tlang_throw(fib, err.status, err.message);
}

tlang_error tlang_take_error(tlang_fiber* fib) {
    tlang_error e;
    if (fib->err) {
        e = fib->error;
    } else {
        e.status = 0;
        e.message = TLANG_STR("");
    }
    fib->err = 0;
    fib->error.status = 0;
    fib->error.message = TLANG_STR("");
    return e;
}

void tlang_throw_fmt(tlang_fiber* fib, int32_t status, const char* fmt, ...) {
    char stackbuf[1024];
    va_list ap;
    int n;
    tlang_string msg;
    char* copy;

    va_start(ap, fmt);
    n = vsnprintf(stackbuf, sizeof stackbuf, fmt, ap);
    va_end(ap);

    if (n < 0) {
        /* Encoding error: fall back to an empty message. */
        tlang_throw(fib, status, TLANG_STR(""));
        return;
    }

    if ((size_t)n < sizeof stackbuf) {
        /* The whole message fits; copy it into the arena (lifetime request). */
        copy = (char*)tlang_alloc_raw(fib, (size_t)n == 0 ? 1 : (size_t)n);
        if (n != 0) memcpy(copy, stackbuf, (size_t)n);
    } else {
        /* Message was truncated: allocate the exact size and format again. */
        copy = (char*)tlang_alloc_raw(fib, (size_t)n + 1);
        va_start(ap, fmt);
        vsnprintf(copy, (size_t)n + 1, fmt, ap);
        va_end(ap);
    }

    msg.data = copy;
    msg.len = (size_t)n;
    tlang_throw(fib, status, msg);
}
