/*
 * http.c - the HTTP/1.1 context API, the §8.1 request parser and the
 * keep-alive connection loop (DESIGN §4.3). docs/RUNTIME.md §4/§6 assigns
 * these functions here; the contracts are in tlang.h §15 and
 * tlang_internal.h §8.
 *
 * One fiber serves one connection. For each request the loop fills a
 * tlang_ctx (all tlang_string fields have non-NULL data), sets f->req and
 * f->watch_fd, runs the dispatcher under TLANG_ABORT_POINT, and then writes
 * either the response the dispatcher produced, a 404 when it produced none, or
 * the error status when an error escaped. Response bytes are built and written
 * after the dispatcher returns; the dispatcher's send/text/json only record
 * the status, content type and body on the private connection object.
 */
#include "tlang_internal.h"

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

/* ------------------------------------------------------------------ *
 * Private per-connection object (ctx->conn)
 * ------------------------------------------------------------------ */

typedef struct http_conn {
    bool have_response;        /* the dispatcher recorded a response */
    int32_t resp_status;       /* status to send */
    tlang_string resp_ctype;   /* content type to send */
    tlang_string resp_body;    /* body bytes (lifetime: request) */
    bool is_head;              /* the request method was HEAD */
} http_conn;

/* ------------------------------------------------------------------ *
 * Reason phrases (a small set is enough for a generic body)
 * ------------------------------------------------------------------ */

static const char* reason_phrase(int status) {
    switch (status) {
    case 200: return "OK";
    case 201: return "Created";
    case 202: return "Accepted";
    case 204: return "No Content";
    case 301: return "Moved Permanently";
    case 302: return "Found";
    case 303: return "See Other";
    case 304: return "Not Modified";
    case 307: return "Temporary Redirect";
    case 308: return "Permanent Redirect";
    case 400: return "Bad Request";
    case 401: return "Unauthorized";
    case 403: return "Forbidden";
    case 404: return "Not Found";
    case 405: return "Method Not Allowed";
    case 408: return "Request Timeout";
    case 409: return "Conflict";
    case 411: return "Length Required";
    case 413: return "Payload Too Large";
    case 414: return "URI Too Long";
    case 415: return "Unsupported Media Type";
    case 422: return "Unprocessable Entity";
    case 429: return "Too Many Requests";
    case 431: return "Request Header Fields Too Large";
    case 500: return "Internal Server Error";
    case 501: return "Not Implemented";
    case 502: return "Bad Gateway";
    case 503: return "Service Unavailable";
    case 504: return "Gateway Timeout";
    default:  return "Status";
    }
}

/* ------------------------------------------------------------------ *
 * Cached Date header (one format per second, per scheduler)
 * ------------------------------------------------------------------ */

static const char* http_date_now(tlang_sched* s) {
    time_t now = time(NULL);
    if ((int64_t)now != s->http_date_sec || s->http_date[0] == '\0') {
        struct tm tmv;
        gmtime_r(&now, &tmv);
        /* IMF-fixdate, e.g. "Sun, 06 Nov 1994 08:49:37 GMT". */
        strftime(s->http_date, sizeof s->http_date, "%a, %d %b %Y %H:%M:%S GMT", &tmv);
        s->http_date_sec = (int64_t)now;
    }
    return s->http_date;
}

/* ------------------------------------------------------------------ *
 * ctx.header
 * ------------------------------------------------------------------ */

tlang_string tlang_ctx_header(const tlang_ctx* ctx, tlang_string name) {
    for (size_t i = 0; i < ctx->num_headers; i++) {
        if (tlang_ascii_ieq(ctx->headers[i].name, name))
            return ctx->headers[i].value;
    }
    return TLANG_STR("");
}

/* ------------------------------------------------------------------ *
 * ctx.setHeader validation
 * ------------------------------------------------------------------ */

/* RFC 9110 token: 1*tchar. */
static bool is_tchar(unsigned char c) {
    if ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'))
        return true;
    switch (c) {
    case '!': case '#': case '$': case '%': case '&': case '\'':
    case '*': case '+': case '-': case '.': case '^': case '_':
    case '`': case '|': case '~':
        return true;
    default:
        return false;
    }
}

static bool is_token(tlang_string s) {
    if (s.len == 0) return false;
    for (size_t i = 0; i < s.len; i++)
        if (!is_tchar((unsigned char)s.data[i])) return false;
    return true;
}

/* The value must not contain CR, LF, NUL or any other control byte except
 * HTAB (header injection). */
static bool valid_header_value(tlang_string v) {
    for (size_t i = 0; i < v.len; i++) {
        unsigned char c = (unsigned char)v.data[i];
        if (c == '\t') continue;
        if (c < 0x20 || c == 0x7f) return false;
    }
    return true;
}

static bool name_is_managed(tlang_string name) {
    return tlang_ascii_ieq(name, TLANG_STR("Content-Length")) ||
           tlang_ascii_ieq(name, TLANG_STR("Transfer-Encoding")) ||
           tlang_ascii_ieq(name, TLANG_STR("Connection"));
}

void tlang_ctx_set_header(tlang_fiber* fib, tlang_ctx* ctx, tlang_string name,
                          tlang_string value) {
    if (ctx->response_sent) {
        tlang_log_warn("ctx.setHeader after the response was sent (ignored)");
        return;
    }
    if (!is_token(name) || !valid_header_value(value) || name_is_managed(name)) {
        tlang_throw(fib, TLANG_STATUS_INTERNAL, TLANG_STR(TLANG_MSG_BAD_HEADER));
        return;
    }
    tlang_resp_header* h = (tlang_resp_header*)tlang_alloc_raw(fib, sizeof *h);
    h->name = name;
    h->value = value;
    h->next = NULL;
    if (ctx->resp_headers_tail != NULL)
        ctx->resp_headers_tail->next = h;
    else
        ctx->resp_headers = h;
    ctx->resp_headers_tail = h;
}

/* ------------------------------------------------------------------ *
 * ctx.send / ctx.text / ctx.json
 * ------------------------------------------------------------------ */

void tlang_ctx_send(tlang_fiber* fib, tlang_ctx* ctx, int32_t status,
                    tlang_string content_type, tlang_string body) {
    (void)fib;
    http_conn* c = (http_conn*)ctx->conn;
    if (ctx->response_sent) {
        tlang_log_warn("a second response was produced for one request (ignored)");
        return;
    }
    if (status < 200 || status > 599) {
        tlang_log_warn("response status %d out of range, using 500", (int)status);
        status = 500;
    }
    c->have_response = true;
    c->resp_status = status;
    c->resp_ctype = content_type;
    c->resp_body = body;
    ctx->status = status;
    ctx->response_sent = true;
}

void tlang_ctx_text(tlang_fiber* fib, tlang_ctx* ctx, int32_t status, tlang_string body) {
    tlang_ctx_send(fib, ctx, status, TLANG_STR("text/plain; charset=utf-8"), body);
}

void tlang_ctx_json(tlang_fiber* fib, tlang_ctx* ctx, int32_t status, tlang_string json) {
    tlang_ctx_send(fib, ctx, status, TLANG_STR("application/json"), json);
}

/* ------------------------------------------------------------------ *
 * Response writer
 * ------------------------------------------------------------------ */

/* Does the set_header list already carry a header named `name`? */
static bool resp_has_header(const tlang_ctx* ctx, tlang_string name) {
    for (const tlang_resp_header* h = ctx->resp_headers; h != NULL; h = h->next)
        if (tlang_ascii_ieq(h->name, name)) return true;
    return false;
}

/* Writes one HTTP response. status/ctype/body describe the payload; is_head
 * suppresses the body; keep_alive controls the Connection header. Returns a
 * tlang_net_write_all result (len >= 0, or a negative code). */
static ssize_t write_response(Fiber* f, tlang_ctx* ctx, int status,
                              tlang_string ctype, tlang_string body,
                              bool is_head, bool keep_alive) {
    tlang_fiber* fib = &f->pub;
    tlang_sched* s = f->sched;
    tlang_buf buf;
    tlang_buf_init(fib, &buf, 512);

    char line[64];
    int n = snprintf(line, sizeof line, "HTTP/1.1 %d %s\r\n",
                     status, reason_phrase(status));
    tlang_buf_put(&buf, line, (size_t)n);

    /* Date (overridable with set_header). */
    if (!resp_has_header(ctx, TLANG_STR("Date"))) {
        TLANG_BUF_PUT_LIT(&buf, "Date: ");
        tlang_buf_put_str(&buf, tlang_str_cstr(http_date_now(s)));
        TLANG_BUF_PUT_LIT(&buf, "\r\n");
    }

    bool no_body = (status == 204 || status == 304);

    /* Content-Type (overridable with set_header; skipped for bodyless). */
    if (!no_body && !resp_has_header(ctx, TLANG_STR("Content-Type"))) {
        TLANG_BUF_PUT_LIT(&buf, "Content-Type: ");
        if (ctype.len == 0)
            TLANG_BUF_PUT_LIT(&buf, "application/octet-stream");
        else
            tlang_buf_put_str(&buf, ctype);
        TLANG_BUF_PUT_LIT(&buf, "\r\n");
    }

    /* Content-Length (runtime-managed; omitted for 204/304). */
    if (!no_body) {
        char cl[48];
        int m = snprintf(cl, sizeof cl, "Content-Length: %zu\r\n", body.len);
        tlang_buf_put(&buf, cl, (size_t)m);
    }

    /* Connection (runtime-managed). */
    if (keep_alive)
        TLANG_BUF_PUT_LIT(&buf, "Connection: keep-alive\r\n");
    else
        TLANG_BUF_PUT_LIT(&buf, "Connection: close\r\n");

    /* set_header headers, in call order. */
    for (const tlang_resp_header* h = ctx->resp_headers; h != NULL; h = h->next) {
        tlang_buf_put_str(&buf, h->name);
        TLANG_BUF_PUT_LIT(&buf, ": ");
        tlang_buf_put_str(&buf, h->value);
        TLANG_BUF_PUT_LIT(&buf, "\r\n");
    }

    TLANG_BUF_PUT_LIT(&buf, "\r\n");

    if (!no_body && !is_head && body.len > 0)
        tlang_buf_put(&buf, body.data, body.len);

    uint64_t deadline = f->sched->cfg->header_timeout_ms != 0
        ? tlang_now_ns() + (uint64_t)f->sched->cfg->header_timeout_ms * 1000000ull
        : 0;
    return tlang_net_write_all(f, ctx->client_fd, buf.data, buf.len, deadline);
}

/* ------------------------------------------------------------------ *
 * Request parsing
 * ------------------------------------------------------------------ */

#define HTTP_PARSE_OK      0
#define HTTP_PARSE_ERROR 400     /* malformed request line / headers */

static bool mem_find_crlfcrlf(const char* buf, size_t len, size_t* end_out) {
    for (size_t i = 0; i + 3 < len; i++) {
        if (buf[i] == '\r' && buf[i + 1] == '\n' &&
            buf[i + 2] == '\r' && buf[i + 3] == '\n') {
            *end_out = i + 4;
            return true;
        }
    }
    return false;
}

/* Trims leading and trailing spaces/tabs from a header value slice. */
static tlang_string trim_ows(const char* p, size_t len) {
    size_t a = 0, b = len;
    while (a < b && (p[a] == ' ' || p[a] == '\t')) a++;
    while (b > a && (p[b - 1] == ' ' || p[b - 1] == '\t')) b--;
    tlang_string s;
    s.data = p + a;
    s.len = b - a;
    return s;
}

/* Parses the request line and headers in buf[0..head_len) (which ends in
 * CRLFCRLF). Fills ctx (method/path/query/headers/num_headers, http_minor,
 * keep_alive). Returns 0 on success or an HTTP status to answer with. On
 * success *content_length and *has_body report the declared body length;
 * *chunked flags Transfer-Encoding: chunked; *expect_continue flags
 * Expect: 100-continue. */
static int parse_request(tlang_ctx* ctx, const char* buf, size_t head_len,
                         int max_headers, size_t* content_length, bool* has_body,
                         bool* chunked, bool* expect_continue) {
    *content_length = 0;
    *has_body = false;
    *chunked = false;
    *expect_continue = false;

    size_t pos = 0;

    /* Request line: METHOD SP target SP HTTP/1.x CRLF */
    size_t m0 = pos;
    while (pos < head_len && buf[pos] != ' ' && buf[pos] != '\r') pos++;
    if (pos >= head_len || buf[pos] != ' ') return HTTP_PARSE_ERROR;
    ctx->method.data = buf + m0;
    ctx->method.len = pos - m0;
    if (ctx->method.len == 0) return HTTP_PARSE_ERROR;
    pos++;  /* SP */

    size_t t0 = pos;
    while (pos < head_len && buf[pos] != ' ' && buf[pos] != '\r') pos++;
    if (pos >= head_len || buf[pos] != ' ') return HTTP_PARSE_ERROR;
    const char* target = buf + t0;
    size_t target_len = pos - t0;
    if (target_len == 0) return HTTP_PARSE_ERROR;
    pos++;  /* SP */

    /* HTTP version. */
    if (head_len - pos < 10) return HTTP_PARSE_ERROR;  /* "HTTP/1.x\r\n" */
    if (memcmp(buf + pos, "HTTP/1.", 7) != 0) return HTTP_PARSE_ERROR;
    char minor = buf[pos + 7];
    if (minor == '0') ctx->http_minor = 0;
    else if (minor == '1') ctx->http_minor = 1;
    else return HTTP_PARSE_ERROR;
    pos += 8;
    if (pos + 1 >= head_len || buf[pos] != '\r' || buf[pos + 1] != '\n')
        return HTTP_PARSE_ERROR;
    pos += 2;

    /* Split the target into path and raw query at the first '?'. */
    size_t qpos = target_len;
    for (size_t i = 0; i < target_len; i++)
        if (target[i] == '?') { qpos = i; break; }
    ctx->path.data = target;
    ctx->path.len = qpos;
    if (qpos < target_len) {
        ctx->query.data = target + qpos + 1;
        ctx->query.len = target_len - qpos - 1;
    } else {
        ctx->query = TLANG_STR("");
    }

    /* Default keep-alive: HTTP/1.1 keeps alive, HTTP/1.0 closes. */
    ctx->keep_alive = (ctx->http_minor == 1);

    /* Header fields. */
    ctx->num_headers = 0;
    bool saw_te = false;   /* a Transfer-Encoding line was already seen */
    while (pos < head_len) {
        if (buf[pos] == '\r' && pos + 1 < head_len && buf[pos + 1] == '\n') {
            pos += 2;
            break;  /* end of headers */
        }
        size_t n0 = pos;
        while (pos < head_len && buf[pos] != ':' && buf[pos] != '\r') pos++;
        if (pos >= head_len || buf[pos] != ':') return HTTP_PARSE_ERROR;
        tlang_string hname;
        hname.data = buf + n0;
        hname.len = pos - n0;
        if (hname.len == 0 || !is_token(hname)) return HTTP_PARSE_ERROR;
        pos++;  /* ':' */

        size_t v0 = pos;
        while (pos < head_len && buf[pos] != '\r') pos++;
        if (pos + 1 >= head_len || buf[pos] != '\r' || buf[pos + 1] != '\n')
            return HTTP_PARSE_ERROR;
        tlang_string hval = trim_ows(buf + v0, pos - v0);
        pos += 2;  /* CRLF */

        /* Interpret the headers the runtime acts on. */
        if (tlang_ascii_ieq(hname, TLANG_STR("Content-Length"))) {
            int64_t cl = 0;
            if (!tlang_parse_i64(hval.data, hval.len, &cl) || cl < 0)
                return HTTP_PARSE_ERROR;
            *content_length = (size_t)cl;
            *has_body = true;
        } else if (tlang_ascii_ieq(hname, TLANG_STR("Transfer-Encoding"))) {
            /* RFC 7230 §4.1: only a bare "chunked" is supported. Any other
             * non-empty coding (gzip, a comma list, a stacked coding, or a
             * value that merely ends in "chunked") is 501, and a second
             * Transfer-Encoding line on one request is 501 (anti-smuggling).
             * An empty value sets nothing and falls through to the no-body
             * path, matching the historical behavior. */
            if (hval.len > 0) {
                if (saw_te) return 501;
                saw_te = true;
                if (tlang_ascii_ieq(hval, TLANG_STR("chunked")))
                    *chunked = true;
                else
                    return 501;
            }
        } else if (tlang_ascii_ieq(hname, TLANG_STR("Connection"))) {
            if (tlang_ascii_ieq(hval, TLANG_STR("close")))
                ctx->keep_alive = false;
            else if (tlang_ascii_ieq(hval, TLANG_STR("keep-alive")))
                ctx->keep_alive = true;
        } else if (tlang_ascii_ieq(hname, TLANG_STR("Expect"))) {
            if (tlang_ascii_ieq(hval, TLANG_STR("100-continue")))
                *expect_continue = true;
        }

        if ((int)ctx->num_headers >= max_headers) return 431;
        if (ctx->num_headers < TLANG_CTX_MAX_HEADERS) {
            ctx->headers[ctx->num_headers].name = hname;
            ctx->headers[ctx->num_headers].value = hval;
            ctx->num_headers++;
        } else {
            return 431;
        }
    }

    return 0;
}

/* ------------------------------------------------------------------ *
 * Chunked request-body decoder (RFC 7230 §4.1)
 *
 * Replaces the old "Transfer-Encoding: chunked -> 501" path with an in-module
 * decoder that assembles the decoded body into one contiguous fiber-arena
 * buffer and presents it to the handler as ctx.body, exactly as the
 * Content-Length path does. Design: .agents/tasks/chunked-bodies/chunked-design.md.
 *
 * State machine (design §3.3.1): two indices drive the decode over the
 * per-connection buffer `buf`:
 *   have   - valid bytes in buf; buf[0 .. have) is live, have <= cap.
 *   cursor - parse position; buf[0 .. cursor) is consumed/dead,
 *            buf[cursor .. have) is unconsumed encoded input. 0 <= cursor <= have.
 * refill() compacts buf[cursor .. have) to the front (cursor -> 0) then does one
 * tlang_net_read; any read result <= 0 mid-token is a premature EOF normalised
 * to the single caller sentinel -1 (connection gone, no response).
 * ------------------------------------------------------------------ */

#define TLANG_CHUNK_LINE_MAX    1024    /* max bytes of one chunk-size line */
#define TLANG_CHUNK_MAX_COUNT   1048576 /* max number of chunks */
#define TLANG_CHUNK_TRAILER_MAX 4096    /* max bytes of the trailer section */

typedef struct chunk_state {
    Fiber* self;
    int fd;
    char* buf;
    size_t cap;
    size_t have;      /* valid bytes in buf */
    size_t cursor;    /* parse position within buf */
} chunk_state;

/* Compacts consumed bytes then issues one tlang_net_read with a fresh per-read
 * header-timeout deadline (design §3.3.1 / §7). Returns the byte count (> 0) or
 * <= 0 to signal EOF/error/timeout; the caller normalises any <= 0 mid-token to
 * the -1 sentinel. */
static ssize_t chunk_refill(chunk_state* st) {
    if (st->cursor > 0) {
        memmove(st->buf, st->buf + st->cursor, st->have - st->cursor);
        st->have -= st->cursor;
        st->cursor = 0;
    }
    if (st->have == st->cap)
        return 0;   /* buffer full with nothing consumed: backstop (design §3.3.1) */
    const tlang_config* cfg = st->self->sched->cfg;
    uint64_t rdl = tlang_now_ns() + (uint64_t)cfg->header_timeout_ms * 1000000ull;
    ssize_t n = tlang_net_read(st->self, st->fd, st->buf + st->have,
                               st->cap - st->have, rdl);
    if (n > 0) st->have += (size_t)n;
    return n;
}

/* Reads one CRLF-terminated line starting at st->cursor into *line (the bytes
 * before the CRLF), leaving st->cursor just past the CRLF. The caller supplies
 * the monotonic length accumulator *acc (design §3.3.3): it counts stream bytes
 * of this line across refill()/compaction and is reset to 0 by the caller when
 * a line is fully consumed. `cap_max` is the length cap for this line.
 * Returns 0 on success, 431 when *acc reaches cap_max before a CRLF, 400 on a
 * bare CR not followed by LF, and -1 on premature EOF/error. */
static int chunk_read_line(chunk_state* st, tlang_string* line, size_t* acc,
                           size_t cap_max) {
    size_t scan = st->cursor;   /* position of the next byte to inspect */
    for (;;) {
        if (scan >= st->have) {
            /* Need more bytes. refill() may compact, moving buf[cursor..have)
             * to the front; re-base scan relative to the new cursor. */
            size_t rel = scan - st->cursor;
            ssize_t n = chunk_refill(st);
            if (n <= 0) return -1;
            scan = st->cursor + rel;
            continue;
        }
        char c = st->buf[scan];
        if (c == '\r') {
            /* Need the following LF. */
            if (scan + 1 >= st->have) {
                size_t rel = scan - st->cursor;
                ssize_t n = chunk_refill(st);
                if (n <= 0) return -1;
                scan = st->cursor + rel;
                continue;
            }
            if (st->buf[scan + 1] != '\n') return 400;
            line->data = st->buf + st->cursor;
            line->len = scan - st->cursor;
            st->cursor = scan + 2;   /* past CRLF */
            *acc = 0;                /* line fully consumed */
            return 0;
        }
        if (c == '\n') return 400;   /* bare LF where CRLF is required */
        /* An ordinary byte advances both the scan and the monotonic counter. */
        if (*acc >= cap_max) return 431;
        (*acc)++;
        scan++;
    }
}

/* Parses the chunk-size line slice: 1*HEXDIG optionally followed by
 * ";"-extensions, which are ignored. Sets *size. Returns 0 on success or 400 on
 * a bad/absent hex digit or a uint64 overflow (design §4.2). */
static int chunk_parse_size(tlang_string line, uint64_t* size) {
    uint64_t v = 0;
    size_t i = 0;
    size_t ndigits = 0;
    for (; i < line.len; i++) {
        unsigned char c = (unsigned char)line.data[i];
        int d;
        if (c >= '0' && c <= '9') d = c - '0';
        else if (c >= 'a' && c <= 'f') d = c - 'a' + 10;
        else if (c >= 'A' && c <= 'F') d = c - 'A' + 10;
        else break;   /* end of hex run (';' or stray byte handled below) */
        if (v > (UINT64_MAX >> 4)) return 400;   /* would overflow */
        v = v * 16 + (uint64_t)d;
        ndigits++;
    }
    if (ndigits == 0) return 400;   /* at least one hex digit required */
    /* After the hex digits, only OWS then ';' extensions (ignored) are allowed
     * up to end-of-line; a stray non-';' byte is a framing error. */
    while (i < line.len && (line.data[i] == ' ' || line.data[i] == '\t')) i++;
    if (i < line.len && line.data[i] != ';') return 400;
    *size = v;
    return 0;
}

/* Appends exactly `n` chunk-data bytes to `out`, pulling from buf[cursor..have)
 * and refilling as needed, then consumes the mandatory trailing CRLF (design
 * §8 copy_chunk_data). Returns 0 on success, 400 on a missing/malformed
 * trailing CRLF, and -1 on premature EOF/error. Precondition: the §4.1 size
 * check already passed for this chunk. */
static int chunk_copy_data(chunk_state* st, uint64_t n, tlang_buf* out) {
    uint64_t remaining = n;
    while (remaining > 0) {
        if (st->cursor == st->have) {
            ssize_t r = chunk_refill(st);
            if (r <= 0) return -1;
        }
        size_t avail = st->have - st->cursor;
        size_t take = avail < remaining ? avail : (size_t)remaining;
        tlang_buf_put(out, st->buf + st->cursor, take);
        st->cursor += take;
        remaining -= take;
    }
    /* Mandatory CRLF after the chunk data. */
    for (;;) {
        if (st->have - st->cursor >= 2) break;
        ssize_t r = chunk_refill(st);
        if (r <= 0) return -1;
    }
    if (st->buf[st->cursor] != '\r' || st->buf[st->cursor + 1] != '\n')
        return 400;
    st->cursor += 2;
    return 0;
}

/* ctx->method/path/query and every ctx->headers[i] slice the connection buffer
 * buf[0 .. head_end). The chunked decoder reuses that buffer as scratch and
 * overwrites those bytes (entry compaction + refill()), so before decoding the
 * head must be copied into the fiber arena and the ctx slices rebased onto the
 * copy. Only the pointers change; lengths and the parsed structure are
 * untouched, so ctx keeps its contract and the handler sees the real
 * method/path/query/headers after the buffer is scrambled. */
static void rehome_request_head(Fiber* self, tlang_ctx* ctx,
                                const char* buf, size_t head_end) {
    char* copy = (char*)tlang_alloc_raw(&self->pub, head_end);
    memcpy(copy, buf, head_end);
    ptrdiff_t shift = copy - buf;
    if (ctx->method.len > 0) ctx->method.data += shift;
    if (ctx->path.len > 0)   ctx->path.data += shift;
    if (ctx->query.len > 0)  ctx->query.data += shift;
    for (size_t i = 0; i < ctx->num_headers; i++) {
        if (ctx->headers[i].name.len > 0)  ctx->headers[i].name.data += shift;
        if (ctx->headers[i].value.len > 0) ctx->headers[i].value.data += shift;
    }
}

/* Reads and decodes a chunked request body. On success returns 0, sets *body to
 * the assembled bytes (non-NULL data) and the leftover out-params to the
 * pipelined tail in buf. On a framing/limit failure returns the HTTP status to
 * answer with (400/413/431). Returns -1 for a premature EOF / read error /
 * timeout (close with no response). See the file-level header above. */
static int read_chunked_body(Fiber* self, int fd, char* buf, size_t cap,
                             size_t body_start, size_t have,
                             tlang_string* body,
                             size_t* leftover_off, size_t* leftover) {
    const tlang_config* cfg = self->sched->cfg;

    chunk_state st;
    st.self = self;
    st.fd = fd;
    st.buf = buf;
    st.cap = cap;
    st.have = have;
    st.cursor = 0;

    /* One-time entry compaction: drop the request head so the cursor model
     * starts clean (design §3.3.1). */
    if (body_start > 0) {
        memmove(buf, buf + body_start, have - body_start);
        st.have = have - body_start;
    }

    tlang_buf decoded;
    tlang_buf_init(&self->pub, &decoded, 0);

    uint64_t decoded_total = 0;
    uint64_t chunk_count = 0;

    for (;;) {
        tlang_string size_line;
        size_t line_acc = 0;
        int rc = chunk_read_line(&st, &size_line, &line_acc, TLANG_CHUNK_LINE_MAX);
        if (rc != 0) return rc;   /* -1, 400 or 431 */

        uint64_t csize = 0;
        rc = chunk_parse_size(size_line, &csize);
        if (rc != 0) return rc;   /* 400 */

        if (++chunk_count > TLANG_CHUNK_MAX_COUNT) return 413;

        if (csize == 0) break;   /* last chunk: go to trailers */

        if (csize > (uint64_t)cfg->max_body_bytes - decoded_total) return 413;

        rc = chunk_copy_data(&st, csize, &decoded);
        if (rc != 0) return rc;   /* -1 or 400 */

        decoded_total += csize;
    }

    /* Trailer section: read lines until an empty line, discarding each. The
     * monotonic trailer_total bounds the whole section across refill()
     * compaction (design §3.3.3 / §5); each line is additionally bounded to the
     * budget that remains, so an unterminated line cannot overrun the section
     * cap before the per-line accumulator trips 431. */
    size_t trailer_total = 0;
    for (;;) {
        tlang_string tline;
        size_t line_acc = 0;
        size_t remaining = TLANG_CHUNK_TRAILER_MAX - trailer_total;
        int rc = chunk_read_line(&st, &tline, &line_acc, remaining);
        if (rc != 0) return rc;   /* -1 or 431 */
        if (tline.len == 0) break;   /* terminating empty line */
        /* Account the line's bytes plus its CRLF against the section budget. */
        trailer_total += tline.len + 2;
        if (trailer_total >= TLANG_CHUNK_TRAILER_MAX) return 431;
    }

    *body = (decoded_total == 0) ? TLANG_STR("") : tlang_buf_string(&decoded);
    *leftover_off = st.cursor;
    *leftover = st.have - st.cursor;
    return 0;
}

/* ------------------------------------------------------------------ *
 * Connection loop
 * ------------------------------------------------------------------ */

/* Runs the dispatcher under TLANG_ABORT_POINT and writes the response (the
 * dispatcher's own, a 404 when it sent nothing, or the error status). Returns
 * whether the connection should be kept alive. Isolated from the connection
 * loop so the loop's locals are not subject to setjmp/longjmp clobbering. */
static bool dispatch_and_respond(Fiber* self, tlang_ctx* ctx, http_conn* hc) {
    tlang_sched* s = self->sched;
    int fd = ctx->client_fd;

    self->req = ctx;
    self->watch_fd = fd;

    volatile int32_t err_status = 0;
    volatile int32_t err_msg_len = 0;
    const char* volatile err_msg_data = "";

    if (TLANG_ABORT_POINT(self) == 0) {
        self->abort_armed = true;
        if (s->prog->dispatcher != NULL)
            s->prog->dispatcher(&self->pub, ctx);
        self->abort_armed = false;
        if (self->pub.err) {
            err_status = self->pub.error.status;
            err_msg_data = self->pub.error.message.data;
            err_msg_len = (int32_t)self->pub.error.message.len;
            tlang_clear_error(&self->pub);
        }
    } else {
        self->abort_armed = false;
        err_status = self->abort_status;
    }

    self->req = NULL;
    self->watch_fd = -1;

    bool keep = ctx->keep_alive;

    if (hc->have_response) {
        if (write_response(self, ctx, hc->resp_status, hc->resp_ctype,
                           hc->resp_body, hc->is_head, keep) < 0)
            keep = false;
        return keep;
    }

    int status;
    const char* msg;
    if (err_status != 0) {
        if (err_status == 499) {
            /* 499 writes nothing. */
            tlang_log_error("tlang: request error: 499 %.*s",
                            (int)err_msg_len, err_msg_data);
            return false;
        }
        status = (err_status >= 400 && err_status <= 599) ? (int)err_status : 500;
        msg = reason_phrase(status);
        tlang_log_error("tlang: request error: %d %.*s",
                        status, (int)err_msg_len, err_msg_data);
    } else {
        status = 404;
        msg = reason_phrase(404);
    }

    char body[64];
    int bn = snprintf(body, sizeof body, "%d %s\n", status, msg);
    tlang_string b;
    b.data = body;
    b.len = (size_t)bn;
    if (write_response(self, ctx, status, TLANG_STR("text/plain; charset=utf-8"),
                       b, hc->is_head, keep) < 0)
        keep = false;
    return keep;
}

static void ctx_reset_strings(tlang_ctx* ctx) {
    ctx->method = TLANG_STR("");
    ctx->path = TLANG_STR("");
    ctx->query = TLANG_STR("");
    ctx->body = TLANG_STR("");
    ctx->param_count = 0;
    ctx->num_headers = 0;
    ctx->response_sent = false;
    ctx->status = 0;
    ctx->resp_headers = NULL;
    ctx->resp_headers_tail = NULL;
}

void tlang_http_conn_main(Fiber* self, void* arg) {
    int fd = (int)(intptr_t)arg;
    tlang_sched* s = self->sched;
    const tlang_config* cfg = s->cfg;

    /* Per-connection read buffer; grows up to the header limit, then holds the
     * body when it fits. Lives on the fiber stack region via a heap buffer? We
     * use the arena-free path: a fixed-size stack buffer sized to the header
     * limit plus a small body budget would be large; instead we keep a buffer
     * in the arena that survives arena_reset only if we re-fill it. To keep
     * pipelined bytes across arena_reset, we store leftover in a small static
     * stack buffer here and copy it back. */
    size_t cap = cfg->max_header_bytes + 1;
    if (cap < 2048) cap = 2048;

    /* Buffer in dynamic storage outside the arena (arena_reset would drop it).
     * A single malloc per connection; freed before the fiber returns. */
    char* buf = (char*)malloc(cap);
    if (buf == NULL) {
        tlang_net_close(s, fd);
        return;
    }
    size_t have = 0;             /* bytes currently in buf */
    bool first_request = true;

    for (;;) {
        /* ---- Read the request head (up to CRLFCRLF) ---- */
        size_t head_end = 0;
        bool got_head = false;
        bool saw_any = (have > 0);
        uint64_t deadline = 0;   /* set from the first byte of this request */

        if (have > 0 && mem_find_crlfcrlf(buf, have, &head_end))
            got_head = true;

        while (!got_head) {
            if (have == cap) {
                /* Head exceeds the header budget. */
                http_conn hc0; memset(&hc0, 0, sizeof hc0);
                tlang_ctx ectx; memset(&ectx, 0, sizeof ectx);
                ctx_reset_strings(&ectx);
                ectx.client_fd = fd; ectx.conn = &hc0;
                write_response(self, &ectx, 431, TLANG_STR("text/plain; charset=utf-8"),
                               tlang_str_cstr("431 Request Header Fields Too Large\n"),
                               false, false);
                goto done;
            }

            /* Idle timeout before the first byte of a request (interruptible);
             * header timeout once the first byte has arrived. */
            uint64_t rdl;
            if (!saw_any) {
                self->interruptible = true;
                rdl = cfg->idle_timeout_ms != 0
                    ? tlang_now_ns() + (uint64_t)cfg->idle_timeout_ms * 1000000ull : 0;
                if (first_request) {
                    /* The very first request still uses the header timeout. */
                    rdl = tlang_now_ns() + (uint64_t)cfg->header_timeout_ms * 1000000ull;
                    self->interruptible = false;
                }
            } else {
                self->interruptible = false;
                if (deadline == 0)
                    deadline = tlang_now_ns() + (uint64_t)cfg->header_timeout_ms * 1000000ull;
                rdl = deadline;
            }

            ssize_t n = tlang_net_read(self, fd, buf + have, cap - have, rdl);
            self->interruptible = false;
            if (n <= 0) goto done;   /* EOF, timeout, cancelled or I/O error */
            if (!saw_any) {
                saw_any = true;
                deadline = tlang_now_ns() + (uint64_t)cfg->header_timeout_ms * 1000000ull;
                first_request = false;
            }
            have += (size_t)n;
            if (mem_find_crlfcrlf(buf, have, &head_end)) got_head = true;
        }

        /* ---- Parse ---- */
        tlang_ctx ctx;
        memset(&ctx, 0, sizeof ctx);
        ctx_reset_strings(&ctx);
        ctx.client_fd = fd;
        ctx.fiber = &self->pub;
        ctx.arena = self->pub.arena;
        http_conn hc;
        memset(&hc, 0, sizeof hc);
        ctx.conn = &hc;

        size_t content_length = 0;
        bool has_body = false, chunked = false, expect_continue = false;
        int perr = parse_request(&ctx, buf, head_end, cfg->max_headers,
                                 &content_length, &has_body, &chunked, &expect_continue);

        bool keep = true;
        if (perr != 0) {
            const char* perr_body =
                perr == 431 ? "431 Request Header Fields Too Large\n" :
                perr == 501 ? "501 Not Implemented\n" :
                              "400 Bad Request\n";
            write_response(self, &ctx, perr, TLANG_STR("text/plain; charset=utf-8"),
                           tlang_str_cstr(perr_body), false, false);
            goto done;
        }

        /* URI too long. */
        if (ctx.path.len + 1 + ctx.query.len > cfg->max_uri_bytes) {
            write_response(self, &ctx, 414, TLANG_STR("text/plain; charset=utf-8"),
                           tlang_str_cstr("414 URI Too Long\n"), false, false);
            goto done;
        }

        /* Both Content-Length and Transfer-Encoding: chunked present is
         * rejected (anti-smuggling, RFC 9112 §6.1 / RFC 7230 §3.3.3). */
        if (chunked && has_body) {
            write_response(self, &ctx, 400, TLANG_STR("text/plain; charset=utf-8"),
                           tlang_str_cstr("400 Bad Request\n"), false, false);
            goto done;
        }

        /* Body too large (Content-Length path; the chunked path enforces the
         * same cap against the decoded size inside read_chunked_body). */
        if (has_body && content_length > cfg->max_body_bytes) {
            write_response(self, &ctx, 413, TLANG_STR("text/plain; charset=utf-8"),
                           tlang_str_cstr("413 Payload Too Large\n"), false, false);
            goto done;
        }

        hc.is_head = tlang_ascii_ieq(ctx.method, TLANG_STR("HEAD"));

        /* Expect: 100-continue: tell the client to send the body. A chunked
         * request carries no Content-Length, so has_body is false for it;
         * widen the guard to cover chunked too (design §3.1). */
        if (expect_continue && (has_body || chunked)) {
            const char cont[] = "HTTP/1.1 100 Continue\r\n\r\n";
            uint64_t wdl = tlang_now_ns() + (uint64_t)cfg->header_timeout_ms * 1000000ull;
            if (tlang_net_write_all(self, fd, cont, sizeof cont - 1, wdl) < 0)
                goto done;
        }

        /* ---- Read the body ---- */
        size_t body_start = head_end;
        size_t body_have = have - head_end;   /* body bytes already buffered */
        size_t leftover = 0;                  /* pipelined bytes kept in buf */
        size_t leftover_off = 0;              /* their offset within buf */

        if (chunked) {
            /* The decoder reuses buf as scratch and overwrites the request
             * head, which ctx.method/path/query/headers slice; copy the head
             * into the arena and rebase those slices first. */
            rehome_request_head(self, &ctx, buf, head_end);
            /* RFC 7230 §4.1 chunked decode into the arena; read_chunked_body
             * owns its own have/cursor and reports the pipelined tail. */
            int cstatus = read_chunked_body(self, fd, buf, cap, body_start, have,
                                             &ctx.body, &leftover_off, &leftover);
            if (cstatus == -1) goto done;   /* premature EOF/error: close */
            if (cstatus != 0) {
                const char* cbody =
                    cstatus == 413 ? "413 Payload Too Large\n" :
                    cstatus == 431 ? "431 Request Header Fields Too Large\n" :
                                     "400 Bad Request\n";
                write_response(self, &ctx, cstatus,
                               TLANG_STR("text/plain; charset=utf-8"),
                               tlang_str_cstr(cbody), false, false);
                goto done;
            }
        } else if (has_body && content_length > 0) {
            if (body_start + content_length <= cap) {
                /* Fits in the connection buffer: read the rest in place. */
                while (body_have < content_length) {
                    uint64_t bdl = tlang_now_ns()
                        + (uint64_t)cfg->header_timeout_ms * 1000000ull;
                    ssize_t n = tlang_net_read(self, fd, buf + head_end + body_have,
                                               content_length - body_have, bdl);
                    if (n <= 0) goto done;
                    body_have += (size_t)n;
                    have += (size_t)n;
                }
                ctx.body.data = buf + body_start;
                ctx.body.len = content_length;
                leftover_off = body_start + content_length;
                leftover = have - leftover_off;
            } else {
                /* Larger than the buffer: read into the arena. Any pipelined
                 * bytes after the body are not retained in this rare case. */
                char* body = (char*)tlang_alloc_raw(&self->pub, content_length);
                size_t copied = body_have < content_length ? body_have : content_length;
                memcpy(body, buf + body_start, copied);
                size_t got = copied;
                while (got < content_length) {
                    uint64_t bdl = tlang_now_ns()
                        + (uint64_t)cfg->header_timeout_ms * 1000000ull;
                    ssize_t n = tlang_net_read(self, fd, body + got,
                                               content_length - got, bdl);
                    if (n <= 0) goto done;
                    got += (size_t)n;
                }
                ctx.body.data = body;
                ctx.body.len = content_length;
                leftover = 0;
            }
        } else {
            /* No body: everything after the head is pipelined. */
            leftover_off = head_end;
            leftover = have - head_end;
        }

        /* ---- Dispatch and respond (isolated frame for the abort point) ---- */
        keep = dispatch_and_respond(self, &ctx, &hc);

        /* ---- Finish the request ---- */
        tlang_cleanup_run_all(self);

        /* Preserve pipelined bytes by moving them to the front of the
         * (non-arena) connection buffer; arena_reset below drops arena data
         * but the connection buffer survives. */
        if (leftover > 0) {
            memmove(buf, buf + leftover_off, leftover);
            have = leftover;
        } else {
            have = 0;
        }

        arena_reset(self->pub.arena);

        if (!keep) goto done;
        /* Loop: serve the next (possibly already-buffered) request. */
    }

done:
    free(buf);
    tlang_net_close(s, fd);
}
