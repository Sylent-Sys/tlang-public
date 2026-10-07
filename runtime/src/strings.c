/*
 * strings.c - string operations, number formatting/parsing and the growable
 * output buffer (tlang_buf). docs/RUNTIME.md §4 assigns these functions here;
 * the contracts live in tlang.h §8/§9/§14 and tlang_internal.h §11.
 */
#include "tlang_internal.h"

#include <math.h>
#include <stdio.h>
#include <stdlib.h>

/* ------------------------------------------------------------------ *
 * Shared number helpers (tlang_internal.h §11)
 * ------------------------------------------------------------------ */

size_t tlang_fmt_i64(char* buf, int64_t v) {
    char tmp[TLANG_FMT_I64_MAX];
    size_t n = 0;
    size_t len;
    bool neg = v < 0;
    /* Accumulate magnitude in uint64_t so INT64_MIN negates without UB. */
    uint64_t mag = neg ? (0u - (uint64_t)v) : (uint64_t)v;

    do {
        tmp[n++] = (char)('0' + (int)(mag % 10));
        mag /= 10;
    } while (mag != 0);

    len = 0;
    if (neg) buf[len++] = '-';
    while (n > 0) buf[len++] = tmp[--n];
    buf[len] = '\0';
    return len;
}

size_t tlang_fmt_f64(char* buf, double v, bool json) {
    static const char* precs[3] = { "%.15g", "%.16g", "%.17g" };
    char tmp[TLANG_FMT_F64_MAX];
    int i;

    if (!isfinite(v)) {
        const char* lit;
        size_t len;
        if (json) lit = "null";
        else if (v != v) lit = "NaN";
        else lit = v < 0 ? "-Infinity" : "Infinity";
        len = strlen(lit);
        memcpy(buf, lit, len + 1);
        return len;
    }

    /* Normalise -0 to 0 so neither "-0" nor "0" surprises the reader. */
    if (v == 0.0) v = 0.0;

    for (i = 0; i < 3; i++) {
        int written = snprintf(tmp, sizeof tmp, precs[i], v);
        if (written > 0 && (size_t)written < sizeof tmp) {
            double back = strtod(tmp, NULL);
            if (back == v || i == 2) {
                memcpy(buf, tmp, (size_t)written + 1);
                return (size_t)written;
            }
        }
    }
    /* Unreachable in practice (the %.17g pass always round-trips). */
    buf[0] = '0';
    buf[1] = '\0';
    return 1;
}

bool tlang_parse_i64(const char* p, size_t n, int64_t* out) {
    size_t i = 0;
    bool neg = false;
    uint64_t acc = 0;
    uint64_t limit;

    if (n == 0) return false;
    if (p[0] == '+' || p[0] == '-') {
        neg = p[0] == '-';
        i = 1;
        if (n == 1) return false;  /* sign with no digits */
    }

    limit = neg ? (uint64_t)INT64_MAX + 1u : (uint64_t)INT64_MAX;
    for (; i < n; i++) {
        unsigned char c = (unsigned char)p[i];
        unsigned digit;
        if (c < '0' || c > '9') return false;
        digit = (unsigned)(c - '0');
        /* Reject overflow before it happens (no signed overflow anywhere). */
        if (acc > (UINT64_MAX - digit) / 10u) return false;
        acc = acc * 10u + digit;
        if (acc > limit) return false;
    }

    *out = neg ? (int64_t)(0u - acc) : (int64_t)acc;
    return true;
}

bool tlang_ascii_ieq(tlang_string a, tlang_string b) {
    size_t i;
    if (a.len != b.len) return false;
    for (i = 0; i < a.len; i++) {
        unsigned char ca = (unsigned char)a.data[i];
        unsigned char cb = (unsigned char)b.data[i];
        if (ca >= 'A' && ca <= 'Z') ca = (unsigned char)(ca - 'A' + 'a');
        if (cb >= 'A' && cb <= 'Z') cb = (unsigned char)(cb - 'A' + 'a');
        if (ca != cb) return false;
    }
    return true;
}

/* ------------------------------------------------------------------ *
 * String operations (tlang.h §8)
 * ------------------------------------------------------------------ */

int64_t tlang_str_index_of(tlang_string s, tlang_string needle) {
    const char* hit;
    if (needle.len == 0) return 0;
    if (needle.len > s.len) return -1;
    hit = (const char*)memmem(s.data, s.len, needle.data, needle.len);
    if (hit == NULL) return -1;
    return (int64_t)(hit - s.data);
}

tlang_string tlang_str_concat(tlang_fiber* fib, tlang_string a, tlang_string b) {
    char* buf;
    tlang_string r;
    /* An empty operand yields the other, but never a NULL data pointer. */
    if (a.len == 0) return b.data != NULL ? b : TLANG_STR("");
    if (b.len == 0) return a;
    buf = (char*)tlang_alloc_raw(fib, a.len + b.len);
    memcpy(buf, a.data, a.len);
    memcpy(buf + a.len, b.data, b.len);
    r.data = buf;
    r.len = a.len + b.len;
    return r;
}

tlang_string tlang_str_clone(tlang_fiber* fib, tlang_string s) {
    char* buf;
    tlang_string r;
    if (s.len == 0) return TLANG_STR("");
    buf = (char*)tlang_alloc_raw(fib, s.len);
    memcpy(buf, s.data, s.len);
    r.data = buf;
    r.len = s.len;
    return r;
}

tlang_string tlang_str_clone_global(tlang_fiber* fib, tlang_string s) {
    char* buf;
    tlang_string r;
    if (s.len == 0) return TLANG_STR("");
    buf = (char*)tlang_alloc_global_zeroed(fib, s.len);
    memcpy(buf, s.data, s.len);
    r.data = buf;
    r.len = s.len;
    return r;
}

int64_t tlang_str_to_int(tlang_fiber* fib, tlang_string s) {
    int64_t v;
    if (!tlang_parse_i64(s.data, s.len, &v)) {
        tlang_throw(fib, TLANG_STATUS_BAD_REQUEST, TLANG_STR(TLANG_MSG_INVALID_INT));
        return 0;
    }
    return v;
}

tlang_string tlang_i32_to_string(tlang_fiber* fib, int32_t v) {
    return tlang_i64_to_string(fib, (int64_t)v);
}

tlang_string tlang_i64_to_string(tlang_fiber* fib, int64_t v) {
    char tmp[TLANG_FMT_I64_MAX];
    size_t n = tlang_fmt_i64(tmp, v);
    tlang_string src;
    src.data = tmp;
    src.len = n;
    return tlang_str_clone(fib, src);
}

tlang_string tlang_f64_to_string(tlang_fiber* fib, double v) {
    char tmp[TLANG_FMT_F64_MAX];
    size_t n = tlang_fmt_f64(tmp, v, false);
    tlang_string src;
    src.data = tmp;
    src.len = n;
    return tlang_str_clone(fib, src);
}

double tlang_mod_f64(double a, double b) {
    return fmod(a, b);
}

/* ------------------------------------------------------------------ *
 * Growable output buffer (tlang.h §14)
 * ------------------------------------------------------------------ */

void tlang_buf_init(tlang_fiber* fib, tlang_buf* b, size_t initial_cap) {
    b->data = NULL;
    b->len = 0;
    b->cap = 0;
    b->fib = fib;
    if (initial_cap != 0) {
        b->data = (char*)tlang_alloc_raw(fib, initial_cap);
        b->cap = initial_cap;
    }
}

void tlang_buf_grow(tlang_buf* b, size_t extra) {
    tlang_fiber* fib = b->fib;
    MemoryArena* a = fib->arena;
    ArenaChunk* c = a->current;
    size_t need;
    size_t newcap;
    char* nd;

    if (extra > SIZE_MAX - b->len) tlang_alloc_failed(fib, extra);
    need = b->len + extra;
    if (need <= b->cap) return;

    newcap = b->cap ? b->cap : 64;
    while (newcap < need) {
        if (newcap > SIZE_MAX / 2) { newcap = need; break; }
        newcap *= 2;
    }
    if (newcap < 64) newcap = 64;

    /* In-place extension: the buffer is the most recent allocation of the
     * current chunk and the chunk still has room for the larger capacity. */
    if (b->data != NULL && b->cap != 0 && c != NULL) {
        size_t aligned_cap = (b->cap + (ARENA_ALIGNMENT - 1)) & ~(size_t)(ARENA_ALIGNMENT - 1);
        if (aligned_cap <= c->offset) {
            uint8_t* block_start = c->data + (c->offset - aligned_cap);
            if ((char*)block_start == b->data) {
                size_t extra_aligned;
                size_t want = (newcap + (ARENA_ALIGNMENT - 1)) & ~(size_t)(ARENA_ALIGNMENT - 1);
                extra_aligned = want - aligned_cap;
                if (extra_aligned <= c->capacity - c->offset) {
                    c->offset += extra_aligned;
                    b->cap = want;
                    return;
                }
            }
        }
    }

    nd = (char*)tlang_alloc_raw(fib, newcap);
    if (b->len != 0) memcpy(nd, b->data, b->len);
    b->data = nd;
    b->cap = newcap;
}
