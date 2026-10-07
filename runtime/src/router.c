/*
 * router.c - route matching and path parameters (spec §8.2). docs/RUNTIME.md
 * §4/§6 assigns these functions here; the contracts are in tlang.h §15.
 * Matching writes parameters to a local array and copies them into
 * ctx->params only on success, so a failed match never disturbs the previous
 * one.
 */
#include "tlang_internal.h"

#include <string.h>

static bool str_eq_bytes(tlang_string a, tlang_string b) {
    if (a.len != b.len) return false;
    if (a.len == 0) return true;
    return memcmp(a.data, b.data, a.len) == 0;
}

bool tlang_ctx_match(tlang_ctx* ctx, const tlang_route* route) {
    /* Method is compared byte for byte (case-sensitive). */
    if (!str_eq_bytes(ctx->method, route->method)) return false;

    /* A route with more parameters than we can hold never matches. */
    if (route->nparams > TLANG_MAX_PARAMS) return false;

    const char* p = ctx->path.data;
    size_t n = ctx->path.len;

    /* The path must start with '/'; "/" has zero segments. */
    if (n == 0 || p[0] != '/') return false;

    /* Walk the path after the leading '/', splitting on '/'. */
    size_t pos = 1;                 /* index after the leading '/' */
    uint32_t seg_index = 0;
    tlang_route_param local[TLANG_MAX_PARAMS];
    size_t nlocal = 0;

    if (pos >= n) {
        /* Path is exactly "/": matches a route with no segments. */
        if (route->nsegs != 0) return false;
        ctx->param_count = 0;
        return true;
    }

    while (pos <= n) {
        /* The current segment runs from `pos` to the next '/' or the end. */
        size_t start = pos;
        while (pos < n && p[pos] != '/') pos++;
        size_t seg_len = pos - start;

        if (seg_index >= route->nsegs) return false;   /* too many segments */

        const tlang_route_seg* rs = &route->segs[seg_index];
        if (rs->kind == TLANG_SEG_PARAM) {
            if (seg_len == 0) return false;             /* params need a byte */
            if (nlocal >= TLANG_MAX_PARAMS) return false;
            local[nlocal].name = rs->text;
            local[nlocal].value.data = p + start;
            local[nlocal].value.len = seg_len;
            nlocal++;
        } else {
            tlang_string seg;
            seg.data = p + start;
            seg.len = seg_len;
            if (!str_eq_bytes(seg, rs->text)) return false;
        }
        seg_index++;

        if (pos == n) break;        /* consumed the last segment */
        pos++;                      /* skip the '/' and continue; a trailing
                                       '/' yields one more empty segment */
        if (pos == n) {
            /* Trailing slash: one final empty segment. */
            if (seg_index >= route->nsegs) return false;
            const tlang_route_seg* last = &route->segs[seg_index];
            if (last->kind == TLANG_SEG_PARAM) return false;  /* empty != param */
            tlang_string empty;
            empty.data = p + pos;
            empty.len = 0;
            if (!str_eq_bytes(empty, last->text)) return false;
            seg_index++;
            break;
        }
    }

    if (seg_index != route->nsegs) return false;    /* too few segments */

    /* Success: copy the recorded parameters into the context. */
    for (size_t i = 0; i < nlocal; i++) ctx->params[i] = local[i];
    ctx->param_count = nlocal;
    return true;
}

tlang_string tlang_ctx_param(const tlang_ctx* ctx, tlang_string name) {
    for (size_t i = 0; i < ctx->param_count; i++) {
        if (str_eq_bytes(ctx->params[i].name, name))
            return ctx->params[i].value;
    }
    return TLANG_STR("");
}

int64_t tlang_ctx_param_int(tlang_fiber* fib, const tlang_ctx* ctx, tlang_string name) {
    tlang_string v = tlang_ctx_param(ctx, name);
    int64_t out = 0;
    if (v.len == 0 || !tlang_parse_i64(v.data, v.len, &out)) {
        tlang_throw(fib, TLANG_STATUS_BAD_REQUEST, TLANG_STR(TLANG_MSG_INVALID_INT));
        return 0;
    }
    return out;
}
