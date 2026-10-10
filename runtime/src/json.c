/*
 * json.c - JSON scanners, the depth limit, and the JSON writers.
 * docs/RUNTIME.md §4/§6 assigns these functions here; the contracts are in
 * tlang.h §14 and tlang_internal.h §12. Scanners never touch fib->err and
 * never abort; json_scan_string reports an arena-alloc failure by returning
 * false (it does not abort).
 */
#include "tlang_internal.h"

#include <stdlib.h>

/* ------------------------------------------------------------------ *
 * Depth limit
 * ------------------------------------------------------------------ */

#ifndef TLANG_JSON_MAX_DEPTH
#define TLANG_JSON_MAX_DEPTH 32
#endif

static int g_json_max_depth = TLANG_JSON_MAX_DEPTH;

int json_max_depth(void) {
    return g_json_max_depth;
}

void json_set_max_depth(int depth) {
    if (depth < 1) depth = 1;
    if (depth > 256) depth = 256;
    g_json_max_depth = depth;
}

/* ------------------------------------------------------------------ *
 * String scanning
 * ------------------------------------------------------------------ */

static int hex_digit(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

/* Reads \uXXXX at *p (which points just past the backslash's 'u'); returns the
 * code unit or -1 on a malformed escape. Advances *p past the four hex digits. */
static int scan_u4(const char** p, const char* end) {
    int v = 0;
    int i;
    const char* q = *p;
    for (i = 0; i < 4; i++) {
        int d;
        if (q >= end) return -1;
        d = hex_digit(*q);
        if (d < 0) return -1;
        v = (v << 4) | d;
        q++;
    }
    *p = q;
    return v;
}

/* Encodes code point cp (0..0x10FFFF) as UTF-8 into out (at least 4 bytes),
 * returning the byte count. */
static size_t utf8_encode(uint32_t cp, unsigned char* out) {
    if (cp < 0x80) {
        out[0] = (unsigned char)cp;
        return 1;
    } else if (cp < 0x800) {
        out[0] = (unsigned char)(0xC0 | (cp >> 6));
        out[1] = (unsigned char)(0x80 | (cp & 0x3F));
        return 2;
    } else if (cp < 0x10000) {
        out[0] = (unsigned char)(0xE0 | (cp >> 12));
        out[1] = (unsigned char)(0x80 | ((cp >> 6) & 0x3F));
        out[2] = (unsigned char)(0x80 | (cp & 0x3F));
        return 3;
    } else {
        out[0] = (unsigned char)(0xF0 | (cp >> 18));
        out[1] = (unsigned char)(0x80 | ((cp >> 12) & 0x3F));
        out[2] = (unsigned char)(0x80 | ((cp >> 6) & 0x3F));
        out[3] = (unsigned char)(0x80 | (cp & 0x3F));
        return 4;
    }
}

/* Scans a JSON string body, assuming *p is at the opening quote. On success,
 * *p is left just past the closing quote, *raw spans the bytes between the
 * quotes, and *has_escape says whether any escape sequence was present.
 * Returns false on a missing closing quote, a raw byte below 0x20, or a
 * malformed escape. Does not decode. */
static bool scan_string_raw(const char** p, const char* end, tlang_string* raw, bool* has_escape) {
    const char* q = *p;
    const char* body;
    bool esc = false;

    if (q >= end || *q != '"') return false;
    q++;
    body = q;
    while (q < end) {
        unsigned char c = (unsigned char)*q;
        if (c == '"') {
            raw->data = body;
            raw->len = (size_t)(q - body);
            *has_escape = esc;
            *p = q + 1;
            return true;
        }
        if (c == '\\') {
            char e;
            esc = true;
            q++;
            if (q >= end) return false;
            e = *q;
            switch (e) {
            case '"': case '\\': case '/':
            case 'b': case 'f': case 'n': case 'r': case 't':
                q++;
                break;
            case 'u': {
                int cu;
                q++;  /* past 'u' */
                cu = scan_u4(&q, end);
                if (cu < 0) return false;
                break;
            }
            default:
                return false;
            }
        } else if (c < 0x20) {
            return false;  /* unescaped control byte */
        } else {
            q++;
        }
    }
    return false;  /* no closing quote */
}

/* Decodes the raw bytes of a JSON string (which contain escapes) into dst,
 * returning the decoded length. dst must be at least `raw.len` bytes (decoding
 * never grows the byte count). */
static size_t decode_string(tlang_string raw, char* dst) {
    const char* p = raw.data;
    const char* end = raw.data + raw.len;
    size_t out = 0;
    while (p < end) {
        unsigned char c = (unsigned char)*p;
        if (c != '\\') {
            dst[out++] = (char)c;
            p++;
            continue;
        }
        p++;  /* past backslash; scan_string_raw guarantees a valid escape */
        switch (*p) {
        case '"':  dst[out++] = '"';  p++; break;
        case '\\': dst[out++] = '\\'; p++; break;
        case '/':  dst[out++] = '/';  p++; break;
        case 'b':  dst[out++] = '\b'; p++; break;
        case 'f':  dst[out++] = '\f'; p++; break;
        case 'n':  dst[out++] = '\n'; p++; break;
        case 'r':  dst[out++] = '\r'; p++; break;
        case 't':  dst[out++] = '\t'; p++; break;
        case 'u': {
            uint32_t cp;
            int hi;
            p++;  /* past 'u' */
            hi = scan_u4(&p, end);
            if (hi >= 0xD800 && hi <= 0xDBFF) {
                /* High surrogate: expect a following \uXXXX low surrogate. */
                if (p + 1 < end && p[0] == '\\' && p[1] == 'u') {
                    const char* save = p;
                    int lo;
                    p += 2;
                    lo = scan_u4(&p, end);
                    if (lo >= 0xDC00 && lo <= 0xDFFF) {
                        cp = 0x10000u + (((uint32_t)hi - 0xD800u) << 10) +
                             ((uint32_t)lo - 0xDC00u);
                    } else {
                        /* Not a valid pair: emit U+FFFD, rewind to reprocess. */
                        cp = 0xFFFD;
                        p = save;
                    }
                } else {
                    cp = 0xFFFD;  /* lone high surrogate */
                }
            } else if (hi >= 0xDC00 && hi <= 0xDFFF) {
                cp = 0xFFFD;  /* lone low surrogate */
            } else {
                cp = (uint32_t)hi;
            }
            out += utf8_encode(cp, (unsigned char*)dst + out);
            break;
        }
        default:
            /* Unreachable: scan_string_raw rejected other escapes. */
            p++;
            break;
        }
    }
    return out;
}

bool json_scan_string(const char** p, const char* end, MemoryArena* arena, tlang_string* out) {
    tlang_string raw;
    bool has_escape;
    char* dst;
    size_t n;

    if (!scan_string_raw(p, end, &raw, &has_escape)) return false;
    if (!has_escape) {
        /* Zero-copy slice of the input. */
        out->data = raw.data;
        out->len = raw.len;
        return true;
    }
    if (raw.len == 0) {
        out->data = "";
        out->len = 0;
        return true;
    }
    /* Decoding never grows the byte count beyond raw.len (worst-case \uFFFD
     * from \uXXXX is 3 bytes from 6, surrogate pairs 4 from 12). */
    dst = (char*)arena_alloc(arena, raw.len);
    if (dst == NULL) return false;  /* report, do not abort (tlang.h §14) */
    n = decode_string(raw, dst);
    out->data = dst;
    out->len = n;
    return true;
}

bool json_scan_key(const char** p, const char* end, tlang_string* key) {
    tlang_string raw;
    bool has_escape;
    if (!scan_string_raw(p, end, &raw, &has_escape)) return false;
    /* Keys are left undecoded: an escaped key never equals a field name, so
     * the generated parser treats it as unknown and skips its value. */
    *key = raw;
    return true;
}

/* ------------------------------------------------------------------ *
 * Number scanning
 * ------------------------------------------------------------------ */

/* Spans a JSON number token at *p (-?(0|[1-9][0-9]*)(.[0-9]+)?([eE][+-]?[0-9]+)?),
 * leaving *p past it. Reports via *has_frac_exp whether a fraction or exponent
 * was present. Returns false on invalid syntax. */
static bool scan_number_token(const char** p, const char* end, bool* has_frac_exp) {
    const char* q = *p;
    bool frac_exp = false;

    if (q < end && *q == '-') q++;
    if (q >= end) return false;
    if (*q == '0') {
        q++;
    } else if (*q >= '1' && *q <= '9') {
        while (q < end && *q >= '0' && *q <= '9') q++;
    } else {
        return false;
    }
    if (q < end && *q == '.') {
        frac_exp = true;
        q++;
        if (q >= end || *q < '0' || *q > '9') return false;
        while (q < end && *q >= '0' && *q <= '9') q++;
    }
    if (q < end && (*q == 'e' || *q == 'E')) {
        frac_exp = true;
        q++;
        if (q < end && (*q == '+' || *q == '-')) q++;
        if (q >= end || *q < '0' || *q > '9') return false;
        while (q < end && *q >= '0' && *q <= '9') q++;
    }
    *has_frac_exp = frac_exp;
    *p = q;
    return true;
}

bool json_scan_int64(const char** p, const char* end, int64_t* out) {
    const char* start = *p;
    const char* q = start;
    bool frac_exp;
    if (!scan_number_token(&q, end, &frac_exp)) return false;
    if (frac_exp) return false;  /* integers only */
    if (!tlang_parse_i64(start, (size_t)(q - start), out)) return false;
    *p = q;
    return true;
}

bool json_scan_int32(const char** p, const char* end, int32_t* out) {
    int64_t v;
    const char* save = *p;
    if (!json_scan_int64(p, end, &v)) return false;
    if (v < INT32_MIN || v > INT32_MAX) {
        *p = save;
        return false;
    }
    *out = (int32_t)v;
    return true;
}

bool json_scan_float64(const char** p, const char* end, double* out) {
    const char* start = *p;
    const char* q = start;
    bool frac_exp;
    size_t n;
    char stackbuf[64];
    char* buf;
    bool heap = false;
    char* parse_end;
    double v;

    if (!scan_number_token(&q, end, &frac_exp)) return false;
    (void)frac_exp;
    n = (size_t)(q - start);

    /* strtod needs a NUL-terminated string; copy the token out. */
    if (n + 1 <= sizeof stackbuf) {
        buf = stackbuf;
    } else {
        buf = (char*)malloc(n + 1);
        if (buf == NULL) return false;  /* report, do not abort */
        heap = true;
    }
    memcpy(buf, start, n);
    buf[n] = '\0';

    parse_end = NULL;
    v = strtod(buf, &parse_end);
    if (parse_end != buf + n) {
        if (heap) free(buf);
        return false;
    }
    if (heap) free(buf);

    /* Overflow to infinity fails; underflow to 0/subnormal is fine. */
    if (v == v && (v > 1.7976931348623157e308 || v < -1.7976931348623157e308)) {
        return false;
    }

    *out = v;
    *p = q;
    return true;
}

/* ------------------------------------------------------------------ *
 * Literals
 * ------------------------------------------------------------------ */

bool json_scan_bool(const char** p, const char* end, bool* out) {
    const char* q = *p;
    if (end - q >= 4 && memcmp(q, "true", 4) == 0) {
        *out = true;
        *p = q + 4;
        return true;
    }
    if (end - q >= 5 && memcmp(q, "false", 5) == 0) {
        *out = false;
        *p = q + 5;
        return true;
    }
    return false;
}

bool json_scan_null(const char** p, const char* end) {
    const char* q = *p;
    if (end - q >= 4 && memcmp(q, "null", 4) == 0) {
        *p = q + 4;
        return true;
    }
    return false;
}

/* ------------------------------------------------------------------ *
 * Skipping a complete value (depth-bounded)
 * ------------------------------------------------------------------ */

bool json_skip_value(const char** p, const char* end, int depth) {
    const char* q = json_skip_ws(*p, end);
    if (q >= end) return false;

    switch (*q) {
    case '"': {
        tlang_string raw;
        bool esc;
        if (!scan_string_raw(&q, end, &raw, &esc)) return false;
        break;
    }
    case 't': case 'f': {
        bool b;
        if (!json_scan_bool(&q, end, &b)) return false;
        break;
    }
    case 'n': {
        if (!json_scan_null(&q, end)) return false;
        break;
    }
    case '{': {
        /* A container at `depth` is accepted only while depth < max. */
        if (depth >= json_max_depth()) return false;
        q++;
        q = json_skip_ws(q, end);
        if (q < end && *q == '}') { q++; break; }
        for (;;) {
            tlang_string key;
            q = json_skip_ws(q, end);
            if (!json_scan_key(&q, end, &key)) return false;
            q = json_skip_ws(q, end);
            if (q >= end || *q != ':') return false;
            q++;
            if (!json_skip_value(&q, end, depth + 1)) return false;
            q = json_skip_ws(q, end);
            if (q >= end) return false;
            if (*q == ',') { q++; continue; }
            if (*q == '}') { q++; break; }
            return false;
        }
        break;
    }
    case '[': {
        if (depth >= json_max_depth()) return false;
        q++;
        q = json_skip_ws(q, end);
        if (q < end && *q == ']') { q++; break; }
        for (;;) {
            if (!json_skip_value(&q, end, depth + 1)) return false;
            q = json_skip_ws(q, end);
            if (q >= end) return false;
            if (*q == ',') { q++; continue; }
            if (*q == ']') { q++; break; }
            return false;
        }
        break;
    }
    default: {
        /* number */
        bool frac_exp;
        if (!scan_number_token(&q, end, &frac_exp)) return false;
        break;
    }
    }

    *p = q;
    return true;
}

/* ------------------------------------------------------------------ *
 * Writers
 * ------------------------------------------------------------------ */

void json_write_str(tlang_buf* b, tlang_string s) {
    static const char hexdig[] = "0123456789abcdef";
    size_t i;
    tlang_buf_putc(b, '"');
    for (i = 0; i < s.len; i++) {
        unsigned char c = (unsigned char)s.data[i];
        switch (c) {
        case '"':  TLANG_BUF_PUT_LIT(b, "\\\""); break;
        case '\\': TLANG_BUF_PUT_LIT(b, "\\\\"); break;
        case '\b': TLANG_BUF_PUT_LIT(b, "\\b"); break;
        case '\f': TLANG_BUF_PUT_LIT(b, "\\f"); break;
        case '\n': TLANG_BUF_PUT_LIT(b, "\\n"); break;
        case '\r': TLANG_BUF_PUT_LIT(b, "\\r"); break;
        case '\t': TLANG_BUF_PUT_LIT(b, "\\t"); break;
        default:
            if (c < 0x20) {
                char esc[6];
                esc[0] = '\\';
                esc[1] = 'u';
                esc[2] = '0';
                esc[3] = '0';
                esc[4] = hexdig[(c >> 4) & 0xF];
                esc[5] = hexdig[c & 0xF];
                tlang_buf_put(b, esc, 6);
            } else {
                tlang_buf_putc(b, (char)c);  /* >= 0x80 copied unchanged */
            }
            break;
        }
    }
    tlang_buf_putc(b, '"');
}

static int utf8_sequence(const unsigned char* p, size_t left, size_t* n) {
    unsigned char c = p[0];
    if (c < 0x80) { *n = 1; return 1; }
    if (c >= 0xC2 && c <= 0xDF) *n = 2;
    else if (c >= 0xE0 && c <= 0xEF) *n = 3;
    else if (c >= 0xF0 && c <= 0xF4) *n = 4;
    else return 0;
    if (left < *n) return 0;
    if ((*n >= 2 && (p[1] & 0xC0) != 0x80) ||
        (*n >= 3 && (p[2] & 0xC0) != 0x80) ||
        (*n >= 4 && (p[3] & 0xC0) != 0x80)) return 0;
    if ((*n == 3 && c == 0xE0 && p[1] < 0xA0) || (*n == 3 && c == 0xED && p[1] >= 0xA0) ||
        (*n == 4 && c == 0xF0 && p[1] < 0x90) || (*n == 4 && c == 0xF4 && p[1] >= 0x90)) return 0;
    return 1;
}

void json_write_str_utf8(tlang_buf* b, tlang_string s) {
    static const char hex[] = "0123456789abcdef";
    size_t i = 0;
    tlang_buf_putc(b, '"');
    while (i < s.len) {
        unsigned char c = (unsigned char)s.data[i];
        size_t n;
        if (c >= 0x80 && !utf8_sequence((const unsigned char*)s.data + i, s.len - i, &n)) {
            char esc[6] = {'\\','u','0','0',hex[c >> 4],hex[c & 15]};
            tlang_buf_put(b, esc, sizeof esc); i++; continue;
        }
        if (c >= 0x80) { tlang_buf_put(b, s.data + i, n); i += n; continue; }
        switch (c) {
        case '"': TLANG_BUF_PUT_LIT(b, "\\\""); break;
        case '\\': TLANG_BUF_PUT_LIT(b, "\\\\"); break;
        case '\b': TLANG_BUF_PUT_LIT(b, "\\b"); break;
        case '\f': TLANG_BUF_PUT_LIT(b, "\\f"); break;
        case '\n': TLANG_BUF_PUT_LIT(b, "\\n"); break;
        case '\r': TLANG_BUF_PUT_LIT(b, "\\r"); break;
        case '\t': TLANG_BUF_PUT_LIT(b, "\\t"); break;
        default:
            if (c < 0x20) {
                char esc[6] = {'\\','u','0','0',hex[c >> 4],hex[c & 15]};
                tlang_buf_put(b, esc, sizeof esc);
            } else tlang_buf_putc(b, (char)c);
        }
        i++;
    }
    tlang_buf_putc(b, '"');
}

void json_write_i64(tlang_buf* b, int64_t v) {
    char tmp[TLANG_FMT_I64_MAX];
    size_t n = tlang_fmt_i64(tmp, v);
    tlang_buf_put(b, tmp, n);
}

void json_write_i32(tlang_buf* b, int32_t v) {
    json_write_i64(b, (int64_t)v);
}

void json_write_f64(tlang_buf* b, double v) {
    char tmp[TLANG_FMT_F64_MAX];
    size_t n = tlang_fmt_f64(tmp, v, true);  /* non-finite -> "null" */
    tlang_buf_put(b, tmp, n);
}
