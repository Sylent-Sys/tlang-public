/*
 * query.c - the `ctx.query(name)` scanner and lazy percent-decoding
 * (spec §8.3). docs/RUNTIME.md §4/§6 assigns this function here; the contract
 * is in tlang.h §15. Keys are compared raw (not decoded); a value is a
 * zero-copy slice of the request buffer unless it contains '%' or '+', in
 * which case it is decoded into the fiber arena ('+' -> space, %XX -> byte,
 * an invalid escape kept literally).
 */
#include "tlang_internal.h"

#include <string.h>

static int hex_val(unsigned char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

/* Decodes [val, val+vlen) into fresh arena memory. The decoded form is never
 * longer than the raw form. */
static tlang_string query_decode(tlang_fiber* fib, const char* val, size_t vlen) {
    char* out = (char*)tlang_alloc_raw(fib, vlen);
    size_t w = 0;
    for (size_t i = 0; i < vlen; i++) {
        char c = val[i];
        if (c == '+') {
            out[w++] = ' ';
        } else if (c == '%' && i + 2 < vlen) {
            int hi = hex_val((unsigned char)val[i + 1]);
            int lo = hex_val((unsigned char)val[i + 2]);
            if (hi >= 0 && lo >= 0) {
                out[w++] = (char)((hi << 4) | lo);
                i += 2;
            } else {
                out[w++] = c;       /* invalid escape, kept literally */
            }
        } else {
            out[w++] = c;           /* lone '%' at the end stays literal */
        }
    }
    if (w == 0) return TLANG_STR("");
    tlang_string r;
    r.data = out;
    r.len = w;
    return r;
}

tlang_string tlang_ctx_query(tlang_fiber* fib, tlang_ctx* ctx, tlang_string name) {
    const char* q = ctx->query.data;
    size_t n = ctx->query.len;

    size_t pos = 0;
    while (pos < n) {
        /* One pair runs to the next '&' or the end. */
        size_t start = pos;
        while (pos < n && q[pos] != '&') pos++;
        size_t pair_len = pos - start;
        if (pos < n) pos++;             /* skip the '&' */

        /* Split the pair on the first '='. */
        const char* pair = q + start;
        size_t eq = pair_len;
        for (size_t i = 0; i < pair_len; i++) {
            if (pair[i] == '=') { eq = i; break; }
        }

        /* Compare the raw key (not decoded) byte for byte. */
        if (eq != name.len) continue;
        if (name.len != 0 && memcmp(pair, name.data, name.len) != 0) continue;

        /* Match. A key without '=' has the value "". */
        if (eq == pair_len) return TLANG_STR("");

        const char* val = pair + eq + 1;
        size_t vlen = pair_len - eq - 1;
        if (vlen == 0) return TLANG_STR("");

        bool needs_decode = false;
        for (size_t i = 0; i < vlen; i++) {
            if (val[i] == '%' || val[i] == '+') { needs_decode = true; break; }
        }
        if (!needs_decode) {
            tlang_string r;
            r.data = val;               /* zero-copy slice of the request */
            r.len = vlen;
            return r;
        }
        return query_decode(fib, val, vlen);
    }

    return TLANG_STR("");
}
