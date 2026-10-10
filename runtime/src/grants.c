/*
 * grants.c - bounded strict JSON validation for runtime grants. This is the
 * final startup gate: generated binaries validate the grant file themselves,
 * including when launched without the tlang Go CLI.
 */
#include "tlang_internal.h"

#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/stat.h>
#include <unistd.h>

#define TL_GRANTS_MAX_BYTES (1u << 20)
#define TL_GRANTS_MAX_NODES 32768
#define TL_GRANTS_MAX_DEPTH 32

static char** g_env_names;
static size_t g_env_count;
static const tlang_program* g_env_program;

bool tlang_env_name_authorized(tlang_string name) {
    size_t i;
    if (g_env_program != NULL) {
        if (g_env_program->env_name_count > 0 && g_env_program->env_names == NULL) return false;
        for (i = 0; i < g_env_program->env_name_count; i++) {
            tlang_string granted = g_env_program->env_names[i];
            if (granted.len == name.len && memcmp(granted.data, name.data, name.len) == 0) return true;
        }
    }
    /* Generated manifest names are authoritative; the global snapshot is
     * retained for older program descriptors and direct runtime tests. */
    for (i = 0; i < g_env_count; i++) {
        size_t n = strlen(g_env_names[i]);
        if (n == name.len && memcmp(g_env_names[i], name.data, n) == 0) return true;
    }
    return false;
}

static uint32_t rotr32(uint32_t x, unsigned n) { return (x >> n) | (x << (32 - n)); }

static void sha256(const unsigned char* data, size_t len, unsigned char out[32]) {
    static const uint32_t k[64] = {
        0x428a2f98u,0x71374491u,0xb5c0fbcfu,0xe9b5dba5u,0x3956c25bu,0x59f111f1u,0x923f82a4u,0xab1c5ed5u,
        0xd807aa98u,0x12835b01u,0x243185beu,0x550c7dc3u,0x72be5d74u,0x80deb1feu,0x9bdc06a7u,0xc19bf174u,
        0xe49b69c1u,0xefbe4786u,0x0fc19dc6u,0x240ca1ccu,0x2de92c6fu,0x4a7484aau,0x5cb0a9dcu,0x76f988dau,
        0x983e5152u,0xa831c66du,0xb00327c8u,0xbf597fc7u,0xc6e00bf3u,0xd5a79147u,0x06ca6351u,0x14292967u,
        0x27b70a85u,0x2e1b2138u,0x4d2c6dfcu,0x53380d13u,0x650a7354u,0x766a0abbu,0x81c2c92eu,0x92722c85u,
        0xa2bfe8a1u,0xa81a664bu,0xc24b8b70u,0xc76c51a3u,0xd192e819u,0xd6990624u,0xf40e3585u,0x106aa070u,
        0x19a4c116u,0x1e376c08u,0x2748774cu,0x34b0bcb5u,0x391c0cb3u,0x4ed8aa4au,0x5b9cca4fu,0x682e6ff3u,
        0x748f82eeu,0x78a5636fu,0x84c87814u,0x8cc70208u,0x90befffau,0xa4506cebu,0xbef9a3f7u,0xc67178f2u
    };
    uint32_t h[8] = {0x6a09e667u,0xbb67ae85u,0x3c6ef372u,0xa54ff53au,
                     0x510e527fu,0x9b05688cu,0x1f83d9abu,0x5be0cd19u};
    uint64_t bit_len = (uint64_t)len * 8;
    size_t total = ((len + 9 + 63) / 64) * 64;
    unsigned char* padded = (unsigned char*)calloc(1, total);
    size_t offset;
    if (padded == NULL) { memset(out, 0, 32); return; }
    memcpy(padded, data, len);
    padded[len] = 0x80;
    for (offset = 0; offset < 8; offset++) padded[total - 1 - offset] = (unsigned char)(bit_len >> (offset * 8));
    for (offset = 0; offset < total; offset += 64) {
        uint32_t w[64], a,b,c,d,e,f,g,hh;
        unsigned i;
        for (i = 0; i < 16; i++) {
            size_t at = offset + i * 4;
            w[i] = ((uint32_t)padded[at] << 24) | ((uint32_t)padded[at+1] << 16) |
                   ((uint32_t)padded[at+2] << 8) | padded[at+3];
        }
        for (i = 16; i < 64; i++) {
            uint32_t s0 = rotr32(w[i-15],7) ^ rotr32(w[i-15],18) ^ (w[i-15] >> 3);
            uint32_t s1 = rotr32(w[i-2],17) ^ rotr32(w[i-2],19) ^ (w[i-2] >> 10);
            w[i] = w[i-16] + s0 + w[i-7] + s1;
        }
        a=h[0]; b=h[1]; c=h[2]; d=h[3]; e=h[4]; f=h[5]; g=h[6]; hh=h[7];
        for (i = 0; i < 64; i++) {
            uint32_t s1 = rotr32(e,6) ^ rotr32(e,11) ^ rotr32(e,25);
            uint32_t ch = (e & f) ^ (~e & g);
            uint32_t t1 = hh + s1 + ch + k[i] + w[i];
            uint32_t s0 = rotr32(a,2) ^ rotr32(a,13) ^ rotr32(a,22);
            uint32_t maj = (a & b) ^ (a & c) ^ (b & c);
            uint32_t t2 = s0 + maj;
            hh=g; g=f; f=e; e=d+t1; d=c; c=b; b=a; a=t1+t2;
        }
        h[0]+=a; h[1]+=b; h[2]+=c; h[3]+=d; h[4]+=e; h[5]+=f; h[6]+=g; h[7]+=hh;
    }
    free(padded);
    for (offset = 0; offset < 8; offset++) {
        out[offset*4] = (unsigned char)(h[offset] >> 24);
        out[offset*4+1] = (unsigned char)(h[offset] >> 16);
        out[offset*4+2] = (unsigned char)(h[offset] >> 8);
        out[offset*4+3] = (unsigned char)h[offset];
    }
}

static int digest_matches(const char* data, size_t len, tlang_string expected) {
    static const char hex[] = "0123456789abcdef";
    unsigned char digest[32];
    char encoded[64];
    size_t i;
    if (expected.data == NULL || expected.len != sizeof encoded) return 0;
    sha256((const unsigned char*)data, len, digest);
    for (i = 0; i < sizeof digest; i++) {
        encoded[i*2] = hex[digest[i] >> 4];
        encoded[i*2+1] = hex[digest[i] & 15];
    }
    return memcmp(encoded, expected.data, sizeof encoded) == 0;
}

enum { J_NULL, J_BOOL, J_NUMBER, J_STRING, J_OBJECT, J_ARRAY };

typedef struct {
    int kind;
    int first;
    int next;
    const char* start;
    const char* end;
    const char* key_start;
    const char* key_end;
} tl_json_node;

typedef struct {
    const char* p;
    const char* end;
    tl_json_node* nodes;
    size_t count;
    int failed;
} tl_json_parser;

static void skip_ws(tl_json_parser* p) {
    while (p->p < p->end && (*p->p == ' ' || *p->p == '\t' ||
           *p->p == '\r' || *p->p == '\n')) p->p++;
}

static int hex_value(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

static int scan_u4(const char** cursor, const char* end, uint32_t* out) {
    uint32_t value = 0;
    int i;
    for (i = 0; i < 4; i++) {
        int digit;
        if (*cursor >= end || (digit = hex_value(*(*cursor)++)) < 0) return 0;
        value = (value << 4) | (uint32_t)digit;
    }
    *out = value;
    return 1;
}

static int utf8_next(const char** cursor, const char* end, uint32_t* out) {
    const unsigned char* p = (const unsigned char*)*cursor;
    uint32_t cp;
    size_t n, i;
    if (*cursor >= end) return 0;
    if (p[0] < 0x80) { cp = p[0]; n = 1; }
    else if (p[0] >= 0xC2 && p[0] <= 0xDF) { cp = p[0] & 0x1F; n = 2; }
    else if (p[0] >= 0xE0 && p[0] <= 0xEF) { cp = p[0] & 0x0F; n = 3; }
    else if (p[0] >= 0xF0 && p[0] <= 0xF4) { cp = p[0] & 0x07; n = 4; }
    else return 0;
    if ((size_t)(end - *cursor) < n) return 0;
    for (i = 1; i < n; i++) {
        if ((p[i] & 0xC0) != 0x80) return 0;
        cp = (cp << 6) | (p[i] & 0x3F);
    }
    if ((n == 3 && cp < 0x800) || (n == 4 && cp < 0x10000) ||
        cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF)) return 0;
    *cursor += n;
    *out = cp;
    return 1;
}

/* Returns a validated string's closing quote, or NULL. */
static const char* scan_string(const char* p, const char* end) {
    if (p >= end || *p++ != '"') return NULL;
    while (p < end) {
        unsigned char c = (unsigned char)*p;
        if (c == '"') return p + 1;
        if (c == '\\') {
            uint32_t hi, lo;
            p++;
            if (p >= end) return NULL;
            switch (*p++) {
            case '"': case '\\': case '/': case 'b': case 'f': case 'n': case 'r': case 't':
                break;
            case 'u':
                if (!scan_u4(&p, end, &hi)) return NULL;
                if (hi >= 0xD800 && hi <= 0xDBFF) {
                    if (end - p < 2 || p[0] != '\\' || p[1] != 'u') return NULL;
                    p += 2;
                    if (!scan_u4(&p, end, &lo) || lo < 0xDC00 || lo > 0xDFFF) return NULL;
                } else if (hi >= 0xDC00 && hi <= 0xDFFF) return NULL;
                break;
            default: return NULL;
            }
        } else if (c < 0x20) return NULL;
        else if (c < 0x80) p++;
        else {
            uint32_t cp;
            if (!utf8_next(&p, end, &cp)) return NULL;
        }
    }
    return NULL;
}

static int string_codepoint(const char** cursor, const char* end, uint32_t* cp) {
    const char* p = *cursor;
    if (p >= end) return 0;
    if (*p != '\\') return utf8_next(cursor, end, cp);
    p++;
    if (p >= end) return 0;
    switch (*p++) {
    case '"': *cp = '"'; break;
    case '\\': *cp = '\\'; break;
    case '/': *cp = '/'; break;
    case 'b': *cp = '\b'; break;
    case 'f': *cp = '\f'; break;
    case 'n': *cp = '\n'; break;
    case 'r': *cp = '\r'; break;
    case 't': *cp = '\t'; break;
    case 'u': {
        uint32_t hi, lo;
        if (!scan_u4(&p, end, &hi)) return 0;
        if (hi >= 0xD800 && hi <= 0xDBFF) {
            if (end - p < 2 || p[0] != '\\' || p[1] != 'u') return 0;
            p += 2;
            if (!scan_u4(&p, end, &lo) || lo < 0xDC00 || lo > 0xDFFF) return 0;
            hi = 0x10000 + ((hi - 0xD800) << 10) + (lo - 0xDC00);
        }
        *cp = hi;
        break;
    }
    default: return 0;
    }
    *cursor = p;
    return 1;
}

static int string_equal_raw(const char* a, const char* a_end, const char* b, const char* b_end) {
    const char* ap = a;
    const char* bp = b;
    uint32_t acp, bcp;
    while (ap < a_end && bp < b_end) {
        if (!string_codepoint(&ap, a_end, &acp) || !string_codepoint(&bp, b_end, &bcp) || acp != bcp)
            return 0;
    }
    return ap == a_end && bp == b_end;
}

static int string_equal(const tl_json_node* node, const char* value) {
    size_t n = strlen(value);
    return node != NULL && node->kind == J_STRING &&
        string_equal_raw(node->start + 1, node->end - 1, value, value + n);
}

static int add_node(tl_json_parser* p, int kind, const char* start, const char* key_start,
                    const char* key_end) {
    int index;
    if (p->count >= TL_GRANTS_MAX_NODES) { p->failed = 1; return -1; }
    index = (int)p->count++;
    p->nodes[index].kind = kind;
    p->nodes[index].first = -1;
    p->nodes[index].next = -1;
    p->nodes[index].start = start;
    p->nodes[index].end = NULL;
    p->nodes[index].key_start = key_start;
    p->nodes[index].key_end = key_end;
    return index;
}

static int parse_value(tl_json_parser* p, int depth, const char* key_start, const char* key_end);

static int parse_array(tl_json_parser* p, int index, int depth) {
    int last = -1;
    p->p++;
    skip_ws(p);
    if (p->p < p->end && *p->p == ']') { p->nodes[index].end = ++p->p; return 1; }
    for (;;) {
        int child = parse_value(p, depth + 1, NULL, NULL);
        if (child < 0) return 0;
        if (last < 0) p->nodes[index].first = child;
        else p->nodes[last].next = child;
        last = child;
        skip_ws(p);
        if (p->p >= p->end) return 0;
        if (*p->p == ']') { p->nodes[index].end = ++p->p; return 1; }
        if (*p->p++ != ',') return 0;
        skip_ws(p);
    }
}

static int parse_object(tl_json_parser* p, int index, int depth) {
    int last = -1;
    p->p++;
    skip_ws(p);
    if (p->p < p->end && *p->p == '}') { p->nodes[index].end = ++p->p; return 1; }
    for (;;) {
        const char* key_start;
        const char* key_end;
        int child, prior;
        if (p->p >= p->end || *p->p != '"') return 0;
        key_start = p->p;
        key_end = scan_string(p->p, p->end);
        if (key_end == NULL) return 0;
        p->p = key_end;
        for (prior = p->nodes[index].first; prior >= 0; prior = p->nodes[prior].next) {
            if (string_equal_raw(p->nodes[prior].key_start + 1, p->nodes[prior].key_end - 1,
                                 key_start + 1, key_end - 1)) return 0;
        }
        skip_ws(p);
        if (p->p >= p->end || *p->p++ != ':') return 0;
        skip_ws(p);
        child = parse_value(p, depth + 1, key_start, key_end);
        if (child < 0) return 0;
        if (last < 0) p->nodes[index].first = child;
        else p->nodes[last].next = child;
        last = child;
        skip_ws(p);
        if (p->p >= p->end) return 0;
        if (*p->p == '}') { p->nodes[index].end = ++p->p; return 1; }
        if (*p->p++ != ',') return 0;
        skip_ws(p);
    }
}

static int parse_value(tl_json_parser* p, int depth, const char* key_start, const char* key_end) {
    const char* start;
    int kind, index;
    if (depth > TL_GRANTS_MAX_DEPTH || p->p >= p->end) return -1;
    start = p->p;
    switch (*p->p) {
    case '{': kind = J_OBJECT; break;
    case '[': kind = J_ARRAY; break;
    case '"': kind = J_STRING; break;
    case 't': case 'f': kind = J_BOOL; break;
    case 'n': kind = J_NULL; break;
    default: kind = J_NUMBER; break;
    }
    index = add_node(p, kind, start, key_start, key_end);
    if (index < 0) return -1;
    if (kind == J_OBJECT) {
        if (!parse_object(p, index, depth)) return -1;
    } else if (kind == J_ARRAY) {
        if (!parse_array(p, index, depth)) return -1;
    } else if (kind == J_STRING) {
        p->nodes[index].end = scan_string(p->p, p->end);
        if (p->nodes[index].end == NULL) return -1;
        p->p = p->nodes[index].end;
    } else if (kind == J_BOOL) {
        const char* word = *p->p == 't' ? "true" : "false";
        size_t n = strlen(word);
        if ((size_t)(p->end - p->p) < n || memcmp(p->p, word, n) != 0) return -1;
        p->p += n;
        p->nodes[index].end = p->p;
    } else if (kind == J_NULL) {
        if (p->end - p->p < 4 || memcmp(p->p, "null", 4) != 0) return -1;
        p->p += 4;
        p->nodes[index].end = p->p;
    } else {
        const char* q = p->p;
        if (q < p->end && *q == '-') q++;
        if (q >= p->end) return -1;
        if (*q == '0') q++;
        else if (*q >= '1' && *q <= '9') { while (q < p->end && *q >= '0' && *q <= '9') q++; }
        else return -1;
        if (q < p->end && *q == '.') {
            q++;
            if (q >= p->end || *q < '0' || *q > '9') return -1;
            while (q < p->end && *q >= '0' && *q <= '9') q++;
        }
        if (q < p->end && (*q == 'e' || *q == 'E')) {
            q++;
            if (q < p->end && (*q == '+' || *q == '-')) q++;
            if (q >= p->end || *q < '0' || *q > '9') return -1;
            while (q < p->end && *q >= '0' && *q <= '9') q++;
        }
        p->p = q;
        p->nodes[index].end = q;
    }
    return index;
}

static int parse_document(const char* data, size_t len, tl_json_node* nodes, size_t* count) {
    tl_json_parser parser;
    int root;
    memset(&parser, 0, sizeof parser);
    parser.p = data;
    parser.end = data + len;
    parser.nodes = nodes;
    skip_ws(&parser);
    root = parse_value(&parser, 0, NULL, NULL);
    skip_ws(&parser);
    if (root != 0 || parser.p != parser.end || parser.failed) return -1;
    *count = parser.count;
    return root;
}

static int utf8_document(const char* data, size_t len) {
    const char* p = data;
    const char* end = data + len;
    while (p < end) {
        uint32_t cp;
        if ((unsigned char)*p < 0x80) { p++; continue; }
        if (!utf8_next(&p, end, &cp)) return 0;
    }
    return 1;
}

static int key_equal(const tl_json_node* node, const char* key) {
    return node->key_start != NULL &&
        string_equal_raw(node->key_start + 1, node->key_end - 1, key, key + strlen(key));
}

static int field(const tl_json_node* nodes, int object_index, const char* key) {
    int child;
    if (object_index < 0 || nodes[object_index].kind != J_OBJECT) return -1;
    for (child = nodes[object_index].first; child >= 0; child = nodes[child].next)
        if (key_equal(&nodes[child], key)) return child;
    return -1;
}

typedef struct { const char* name; int required; } tl_field_spec;

static int shape_object(const tl_json_node* nodes, int index, const tl_field_spec* spec, size_t count) {
    int child;
    size_t i;
    if (index < 0 || nodes[index].kind != J_OBJECT) return 0;
    for (child = nodes[index].first; child >= 0; child = nodes[child].next) {
        int found = 0;
        for (i = 0; i < count; i++) if (key_equal(&nodes[child], spec[i].name)) { found = 1; break; }
        if (!found) return 0;
    }
    for (i = 0; i < count; i++)
        if (spec[i].required && field(nodes, index, spec[i].name) < 0) return 0;
    return 1;
}

static int array_kind(const tl_json_node* nodes, int index, int kind) {
    int child;
    if (index < 0 || nodes[index].kind != J_ARRAY) return 0;
    for (child = nodes[index].first; child >= 0; child = nodes[child].next)
        if (nodes[child].kind != kind) return 0;
    return 1;
}

static int number_value(const tl_json_node* node, int64_t* value) {
    const char* p;
    uint64_t v = 0;
    int negative = 0;
    uint64_t limit;
    if (node == NULL || node->kind != J_NUMBER || node->start == NULL || node->end == NULL) return 0;
    p = node->start;
    if (p < node->end && *p == '-') { negative = 1; p++; }
    if (p == node->end) return 0;
    limit = negative ? (uint64_t)INT64_MAX + 1 : (uint64_t)INT64_MAX;
    for (; p < node->end; p++) {
        unsigned digit;
        if (*p < '0' || *p > '9') return 0;
        digit = (unsigned)(*p - '0');
        if (v > (limit - digit) / 10) return 0;
        v = v * 10 + digit;
    }
    *value = negative ? (v == (uint64_t)INT64_MAX + 1 ? INT64_MIN : -(int64_t)v) : (int64_t)v;
    return 1;
}

static int get_nonnegative(const tl_json_node* nodes, int object_index, const char* name,
                           int optional, int64_t* out) {
    int i = field(nodes, object_index, name);
    if (i < 0) { *out = 0; return optional; }
    return number_value(&nodes[i], out) && *out >= 0;
}

static int identifier(const tl_json_node* node) {
    const char* p;
    const char* end;
    uint32_t cp;
    int first = 1;
    if (node == NULL || node->kind != J_STRING) return 0;
    p = node->start + 1; end = node->end - 1;
    while (p < end) {
        if (!string_codepoint(&p, end, &cp) || cp > 0x7f) return 0;
        if (first ? !((cp >= 'A' && cp <= 'Z') || (cp >= 'a' && cp <= 'z') || cp == '_') :
            !((cp >= 'A' && cp <= 'Z') || (cp >= 'a' && cp <= 'z') ||
              (cp >= '0' && cp <= '9') || cp == '_' || cp == '.' || cp == '-')) return 0;
        first = 0;
    }
    return !first;
}

static int string_list_valid(const tl_json_node* nodes, int index, int identifiers,
                             const char* const* choices, size_t nchoices) {
    int child, other;
    if (!array_kind(nodes, index, J_STRING)) return 0;
    for (child = nodes[index].first; child >= 0; child = nodes[child].next) {
        int valid = identifiers ? identifier(&nodes[child]) : 1;
        size_t i;
        if (choices != NULL) {
            valid = 0;
            for (i = 0; i < nchoices; i++) if (string_equal(&nodes[child], choices[i])) valid = 1;
        }
        if (!valid) return 0;
        for (other = nodes[index].first; other != child; other = nodes[other].next)
            if (string_equal_raw(nodes[other].start + 1, nodes[other].end - 1,
                                 nodes[child].start + 1, nodes[child].end - 1)) return 0;
    }
    return 1;
}

static size_t child_count(const tl_json_node* nodes, int index) {
    size_t count = 0;
    int child;
    if (index < 0) return 0;
    for (child = nodes[index].first; child >= 0; child = nodes[child].next) count++;
    return count;
}

static int strings_equal(const tl_json_node* a_nodes, int a, const tl_json_node* b_nodes, int b) {
    int i, j;
    if (!array_kind(a_nodes, a, J_STRING) || !array_kind(b_nodes, b, J_STRING) ||
        child_count(a_nodes, a) != child_count(b_nodes, b)) return 0;
    for (i = a_nodes[a].first; i >= 0; i = a_nodes[i].next) {
        int match = 0;
        for (j = b_nodes[b].first; j >= 0; j = b_nodes[j].next) {
            if (string_equal_raw(a_nodes[i].start + 1, a_nodes[i].end - 1,
                                 b_nodes[j].start + 1, b_nodes[j].end - 1)) { match = 1; break; }
        }
        if (!match) return 0;
    }
    return 1;
}

static int string_set_covered(const tl_json_node* req_nodes, int req, const tl_json_node* grant_nodes, int grant) {
    int i, j;
    if (!array_kind(req_nodes, req, J_STRING) || !array_kind(grant_nodes, grant, J_STRING)) return 0;
    for (i = req_nodes[req].first; i >= 0; i = req_nodes[i].next) {
        int found = 0;
        for (j = grant_nodes[grant].first; j >= 0; j = grant_nodes[j].next)
            if (string_equal_raw(req_nodes[i].start + 1, req_nodes[i].end - 1,
                    grant_nodes[j].start + 1, grant_nodes[j].end - 1)) { found = 1; break; }
        if (!found) return 0;
    }
    return 1;
}

static int limits_covered(const tl_json_node* req_nodes, int req, const tl_json_node* grant_nodes, int grant);

static int pathsafe(const tl_json_node* node) {
    const char* p;
    const char* end;
    if (node == NULL || node->kind != J_STRING) return 0;
    p = node->start + 1; end = node->end - 1;
    if (p == end || *p == '/' || *p == '\\') return 0;
    while (p < end) {
        if (*p == '\\' || *p == ':' || *p == 0) return 0;
        if (end - p >= 2 && p[0] == '.' && p[1] == '.') {
            if ((p == node->start + 1 || p[-1] == '/') && (p + 2 == end || p[2] == '/')) return 0;
        }
        p++;
    }
    if (node->end - node->start > 2048) return 0;
    return 1;
}

static int valid_modes(const tl_json_node* nodes, int index) {
    static const char* const modes[] = {"read", "write", "create", "delete", "list"};
    return string_list_valid(nodes, index, 0, modes, sizeof modes / sizeof modes[0]);
}

static int valid_ports(const tl_json_node* nodes, int index) {
    static const tl_field_spec spec[] = {{"from",1},{"to",1}};
    int child, other;
    if (!array_kind(nodes, index, J_OBJECT)) return 0;
    for (child = nodes[index].first; child >= 0; child = nodes[child].next) {
        int64_t from, to;
        if (!shape_object(nodes, child, spec, 2) ||
            !get_nonnegative(nodes, child, "from", 0, &from) ||
            !get_nonnegative(nodes, child, "to", 0, &to) || from < 1 || from > to || to > 65535) return 0;
        for (other = nodes[index].first; other != child; other = nodes[other].next) {
            int64_t pf, pt;
            if (get_nonnegative(nodes, other, "from", 0, &pf) && get_nonnegative(nodes, other, "to", 0, &pt) &&
                pf == from && pt == to) return 0;
        }
    }
    return 1;
}

static int ports_equal(const tl_json_node* a_nodes, int a, const tl_json_node* b_nodes, int b);

static int host_valid(const tl_json_node* node) {
    const char* p;
    const char* end;
    int previous_dot = 0;
    if (node == NULL) return 1;
    if (node->kind != J_STRING) return 0;
    p = node->start + 1; end = node->end - 1;
    if (p == end) return 0;
    for (; p < end; p++) {
        unsigned char c = (unsigned char)*p;
        if (!(c == '.' || c == '-' || c == ':' || c == '[' || c == ']' ||
              (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'))) return 0;
        if (c == '.' && previous_dot) return 0;
        previous_dot = c == '.';
    }
    if (node->end - node->start > 255 || node->start[1] == '.' || node->end[-2] == '.') return 0;
    return 1;
}

static int valid_network_rules(const tl_json_node* nodes, int index) {
    static const tl_field_spec spec[] = {{"protocol",1},{"host",0},{"ports",1},{"groups",1}};
    int child, other;
    if (!array_kind(nodes, index, J_OBJECT)) return 0;
    for (child = nodes[index].first; child >= 0; child = nodes[child].next) {
        int protocol, host, ports, groups;
        int host_missing;
        if (!shape_object(nodes, child, spec, 4)) return 0;
        protocol = field(nodes, child, "protocol");
        host = field(nodes, child, "host");
        ports = field(nodes, child, "ports");
        groups = field(nodes, child, "groups");
        host_missing = host < 0;
        if ((!string_equal(&nodes[protocol], "tcp") && !string_equal(&nodes[protocol], "udp")) ||
            (!host_missing && !host_valid(&nodes[host])) || !valid_ports(nodes, ports) ||
            !string_list_valid(nodes, groups, 1, NULL, 0) ||
            ((host_missing || (nodes[host].end - nodes[host].start == 2)) && nodes[groups].first < 0)) return 0;
        for (other = nodes[index].first; other != child; other = nodes[other].next) {
            int op = field(nodes, other, "protocol"), oh = field(nodes, other, "host");
            if (string_equal_raw(nodes[op].start + 1, nodes[op].end - 1,
                                 nodes[protocol].start + 1, nodes[protocol].end - 1) &&
                ((host_missing && oh < 0) || (host >= 0 && oh >= 0 &&
                 string_equal_raw(nodes[host].start + 1, nodes[host].end - 1,
                                  nodes[oh].start + 1, nodes[oh].end - 1))) &&
                strings_equal(nodes, groups, nodes, field(nodes, other, "groups")) &&
                ports_equal(nodes, ports, nodes, field(nodes, other, "ports"))) return 0;
        }
    }
    return 1;
}

static int ports_equal(const tl_json_node* a_nodes, int a, const tl_json_node* b_nodes, int b) {
    int p, q;
    if (a < 0 || b < 0 || a_nodes[a].kind != J_ARRAY || b_nodes[b].kind != J_ARRAY ||
        child_count(a_nodes, a) != child_count(b_nodes, b)) return 0;
    for (p = a_nodes[a].first; p >= 0; p = a_nodes[p].next) {
        int64_t from, to;
        if (!get_nonnegative(a_nodes, p, "from", 0, &from) || !get_nonnegative(a_nodes, p, "to", 0, &to)) return 0;
        for (q = b_nodes[b].first; q >= 0; q = b_nodes[q].next) {
            int64_t grant_from, grant_to;
            if (get_nonnegative(b_nodes, q, "from", 0, &grant_from) && get_nonnegative(b_nodes, q, "to", 0, &grant_to) &&
                grant_from == from && grant_to == to) break;
        }
        if (q < 0) return 0;
    }
    return 1;
}

static int ports_covered(const tl_json_node* req_nodes, int req, const tl_json_node* grant_nodes, int grant) {
    int p, q;
    if (req < 0 || grant < 0 || req_nodes[req].kind != J_ARRAY || grant_nodes[grant].kind != J_ARRAY) return 0;
    for (p = req_nodes[req].first; p >= 0; p = req_nodes[p].next) {
        int64_t from, to;
        if (!get_nonnegative(req_nodes, p, "from", 0, &from) || !get_nonnegative(req_nodes, p, "to", 0, &to)) return 0;
        for (q = grant_nodes[grant].first; q >= 0; q = grant_nodes[q].next) {
            int64_t grant_from, grant_to;
            if (get_nonnegative(grant_nodes, q, "from", 0, &grant_from) && get_nonnegative(grant_nodes, q, "to", 0, &grant_to) &&
                grant_from <= from && grant_to >= to) break;
        }
        if (q < 0) return 0;
    }
    return 1;
}

static int valid_capabilities(const tl_json_node* nodes, int index) {
    static const tl_field_spec cap_spec[] = {{"env",1},{"filesystem",1},{"process",1},{"network",1},{"lifecycle",1}};
    static const tl_field_spec fs_spec[] = {{"root",1},{"modes",1}};
    static const tl_field_spec network_spec[] = {{"connect",1},{"listen",1}};
    static const tl_field_spec life_spec[] = {{"signals",1}};
    int env, fs, processes, network, lifecycle, child, other, net_connect, net_listen, signals;
    if (!shape_object(nodes, index, cap_spec, 5)) return 0;
    env = field(nodes, index, "env"); fs = field(nodes, index, "filesystem");
    processes = field(nodes, index, "process"); network = field(nodes, index, "network");
    lifecycle = field(nodes, index, "lifecycle");
    if (!string_list_valid(nodes, env, 1, NULL, 0) || !array_kind(nodes, fs, J_OBJECT) ||
        !array_kind(nodes, processes, J_OBJECT)) return 0;
    for (child = nodes[processes].first; child >= 0; child = nodes[child].next) {
        static const tl_field_spec process_spec[] = {{"executable",1},{"maxArgs",0},{"maxOutputBytes",0}};
        int executable, prior;
        int64_t bound;
        if (!shape_object(nodes, child, process_spec, 3) ||
            (executable = field(nodes, child, "executable")) < 0 || !identifier(&nodes[executable]) ||
            !get_nonnegative(nodes, child, "maxArgs", 1, &bound) || bound > 65536 ||
            !get_nonnegative(nodes, child, "maxOutputBytes", 1, &bound) || bound > 67108864) return 0;
        for (prior = nodes[processes].first; prior != child; prior = nodes[prior].next) {
            int prior_executable = field(nodes, prior, "executable");
            if (prior_executable >= 0 && string_equal_raw(nodes[prior_executable].start + 1,
                    nodes[prior_executable].end - 1, nodes[executable].start + 1,
                    nodes[executable].end - 1)) return 0;
        }
    }
    for (child = nodes[fs].first; child >= 0; child = nodes[child].next) {
        int root, modes;
        if (!shape_object(nodes, child, fs_spec, 2)) return 0;
        root = field(nodes, child, "root"); modes = field(nodes, child, "modes");
        if (!pathsafe(&nodes[root]) || !valid_modes(nodes, modes)) return 0;
        for (other = nodes[fs].first; other != child; other = nodes[other].next) {
            int prior = field(nodes, other, "root");
            if (string_equal_raw(nodes[root].start + 1, nodes[root].end - 1,
                                 nodes[prior].start + 1, nodes[prior].end - 1)) return 0;
        }
    }
    if (!shape_object(nodes, network, network_spec, 2) ||
        !valid_network_rules(nodes, (net_connect = field(nodes, network, "connect"), net_connect)) ||
        !valid_network_rules(nodes, (net_listen = field(nodes, network, "listen"), net_listen)) ||
        !shape_object(nodes, lifecycle, life_spec, 1)) return 0;
    signals = field(nodes, lifecycle, "signals");
    {
        static const char* const allowed[] = {"SIGINT", "SIGTERM"};
        if (!string_list_valid(nodes, signals, 0, allowed, 2)) return 0;
    }
    return 1;
}

static int network_rules_covered(const tl_json_node* req_nodes, int req, const tl_json_node* grant_nodes, int grant) {
    int i, j;
    if (req < 0 || grant < 0 || req_nodes[req].kind != J_ARRAY || grant_nodes[grant].kind != J_ARRAY) return 0;
    for (i = req_nodes[req].first; i >= 0; i = req_nodes[i].next) {
        int req_protocol = field(req_nodes, i, "protocol");
        int req_host = field(req_nodes, i, "host");
        int req_groups = field(req_nodes, i, "groups");
        int req_ports = field(req_nodes, i, "ports");
        int matched = 0;
        for (j = grant_nodes[grant].first; j >= 0; j = grant_nodes[j].next) {
            int grant_protocol = field(grant_nodes, j, "protocol");
            int grant_host = field(grant_nodes, j, "host");
            int grant_groups = field(grant_nodes, j, "groups");
            int grant_ports = field(grant_nodes, j, "ports");
            if (req_protocol < 0 || grant_protocol < 0 || !string_equal_raw(req_nodes[req_protocol].start + 1,
                    req_nodes[req_protocol].end - 1, grant_nodes[grant_protocol].start + 1, grant_nodes[grant_protocol].end - 1)) continue;
            if ((req_host < 0) != (grant_host < 0) || (req_host >= 0 && !string_equal_raw(req_nodes[req_host].start + 1,
                    req_nodes[req_host].end - 1, grant_nodes[grant_host].start + 1, grant_nodes[grant_host].end - 1))) continue;
            if (!string_set_covered(req_nodes, req_groups, grant_nodes, grant_groups)) continue;
            if (ports_covered(req_nodes, req_ports, grant_nodes, grant_ports)) { matched = 1; break; }
        }
        if (!matched) return 0;
    }
    return 1;
}

static int process_covered(const tl_json_node* req_nodes, int req, const tl_json_node* grant_nodes, int grant) {
    int req_exe = field(req_nodes, req, "executable"), grant_exe = field(grant_nodes, grant, "executable");
    const char* bound_names[] = {"maxArgs", "maxOutputBytes"};
    size_t i;
    if (req_exe < 0 || grant_exe < 0 || !string_equal_raw(req_nodes[req_exe].start + 1,
            req_nodes[req_exe].end - 1, grant_nodes[grant_exe].start + 1, grant_nodes[grant_exe].end - 1)) return 0;
    for (i = 0; i < 2; i++) {
        int64_t requested, allowed;
        if (!get_nonnegative(req_nodes, req, bound_names[i], 1, &requested) ||
            !get_nonnegative(grant_nodes, grant, bound_names[i], 1, &allowed)) return 0;
        if (requested != 0 && (allowed == 0 || allowed > requested)) return 0;
    }
    return 1;
}

static int processes_covered(const tl_json_node* req_nodes, int req, const tl_json_node* grant_nodes, int grant) {
    int i, j;
    if (req_nodes[req].kind != J_ARRAY || grant_nodes[grant].kind != J_ARRAY ||
        child_count(req_nodes, req) != child_count(grant_nodes, grant)) return 0;
    for (i = req_nodes[req].first; i >= 0; i = req_nodes[i].next) {
        int found = 0;
        for (j = grant_nodes[grant].first; j >= 0; j = grant_nodes[j].next)
            if (process_covered(req_nodes, i, grant_nodes, j)) { found = 1; break; }
        if (!found) return 0;
    }
    return 1;
}

static int filesystem_covered(const tl_json_node* req_nodes, int req, const tl_json_node* grant_nodes, int grant) {
    int i, j;
    if (req_nodes[req].kind != J_ARRAY || grant_nodes[grant].kind != J_ARRAY ||
        child_count(req_nodes, req) != child_count(grant_nodes, grant)) return 0;
    for (i = req_nodes[req].first; i >= 0; i = req_nodes[i].next) {
        int ir = field(req_nodes, i, "root"), im = field(req_nodes, i, "modes"), found = 0;
        for (j = grant_nodes[grant].first; j >= 0; j = grant_nodes[j].next) {
            int jr = field(grant_nodes, j, "root"), jm = field(grant_nodes, j, "modes");
            if (ir >= 0 && jr >= 0 && string_equal_raw(req_nodes[ir].start + 1, req_nodes[ir].end - 1,
                grant_nodes[jr].start + 1, grant_nodes[jr].end - 1) &&
                strings_equal(req_nodes, im, grant_nodes, jm)) {
                found = 1; break;
            }
        }
        if (!found) return 0;
    }
    return 1;
}

static int capabilities_covered(const tl_json_node* req_nodes, int req, const tl_json_node* grant_nodes, int grant) {
    int re = field(req_nodes, req, "env"), ge = field(grant_nodes, grant, "env");
    int rf = field(req_nodes, req, "filesystem"), gf = field(grant_nodes, grant, "filesystem");
    int rp = field(req_nodes, req, "process"), gp = field(grant_nodes, grant, "process");
    int rn = field(req_nodes, req, "network"), gn = field(grant_nodes, grant, "network");
    int rc = field(req_nodes, rn, "connect"), gc = field(grant_nodes, gn, "connect");
    int rl = field(req_nodes, rn, "listen"), gl = field(grant_nodes, gn, "listen");
    int rs = field(req_nodes, field(req_nodes, req, "lifecycle"), "signals");
    int gs = field(grant_nodes, field(grant_nodes, grant, "lifecycle"), "signals");
    return strings_equal(req_nodes, re, grant_nodes, ge) && filesystem_covered(req_nodes, rf, grant_nodes, gf) &&
        processes_covered(req_nodes, rp, grant_nodes, gp) && network_rules_covered(req_nodes, rc, grant_nodes, gc) &&
        network_rules_covered(req_nodes, rl, grant_nodes, gl) && strings_equal(req_nodes, rs, grant_nodes, gs);
}

static int limits_valid(const tl_json_node* nodes, int index, int grants) {
    static const tl_field_spec spec[] = {
        {"maxWorkers",0},{"maxQueueCapacity",0},{"maxDeadlineMs",0},{"maxFileBytes",0},
        {"maxProcessOutputBytes",0},{"maxNetworkConnections",0},{"maxDatabaseConnections",0}
    };
    static const int64_t ceilings[] = {256,65536,86400000,1073741824,67108864,65536,4096};
    size_t i;
    if (!shape_object(nodes, index, spec, sizeof spec / sizeof spec[0])) return 0;
    for (i = 0; i < sizeof spec / sizeof spec[0]; i++) {
        int64_t value;
        if (!get_nonnegative(nodes, index, spec[i].name, 1, &value) || value > ceilings[i]) return 0;
    }
    (void)grants;
    return 1;
}

static int limits_covered(const tl_json_node* req_nodes, int req, const tl_json_node* grant_nodes, int grant);

static int limits_covered(const tl_json_node* req_nodes, int req, const tl_json_node* grant_nodes, int grant) {
    static const char* const names[] = {"maxWorkers","maxQueueCapacity","maxDeadlineMs","maxFileBytes",
        "maxProcessOutputBytes","maxNetworkConnections","maxDatabaseConnections"};
    size_t i;
    for (i = 0; i < sizeof names / sizeof names[0]; i++) {
        int64_t requested, allowed;
        if (!get_nonnegative(req_nodes, req, names[i], 1, &requested) ||
            !get_nonnegative(grant_nodes, grant, names[i], 1, &allowed)) return 0;
        if (requested != 0 && (allowed == 0 || allowed > requested)) return 0;
    }
    return 1;
}

static int decode_string_value(const tl_json_node* node, char* out, size_t capacity);

static int valid_database_url(const tl_json_node* node, const char* engine) {
    char* value;
    const char* p;
    const char* authority;
    const char* authority_end;
    int postgres = strcmp(engine, "postgres") == 0;
    int valid = 0;
    size_t i, len;
    if (node == NULL || node->kind != J_STRING) return 0;
    value = (char*)malloc(TL_GRANTS_MAX_BYTES + 1);
    if (value == NULL || !decode_string_value(node, value, TL_GRANTS_MAX_BYTES + 1)) { free(value); return 0; }
    len = strlen(value);
    if (len == 0 || value[0] == ' ' || value[len - 1] == ' ') goto done;
    for (i = 0; i < len; i++)
        if ((unsigned char)value[i] < 0x20 || (unsigned char)value[i] == 0x7f) goto done;
    p = value;
    if (strncmp(p, "postgres://", 11) == 0 || strncmp(p, "postgresql://", 12) == 0) {
        size_t prefix = strncmp(p, "postgres://", 11) == 0 ? 11 : 12;
        if (!postgres) goto done;
        authority = p + prefix;
        authority_end = authority;
        while (*authority_end != '\0' && *authority_end != '/' && *authority_end != '?' && *authority_end != '#') authority_end++;
        valid = authority_end > authority;
    } else if (!postgres && (strncmp(p, "sqlite:", 7) == 0 || strncmp(p, "file:", 5) == 0)) {
        size_t prefix = p[0] == 's' ? 7 : 5;
        valid = strlen(p + prefix) > 0;
    }
done:
    free(value);
    return valid;
}

static int valid_databases(const tl_json_node* manifest_nodes, int manifest_db,
                           const tl_json_node* grant_nodes, int grant_db) {
    int request, granted;
    if (manifest_nodes[manifest_db].kind != J_OBJECT || grant_nodes[grant_db].kind != J_OBJECT) return 0;
    for (request = manifest_nodes[manifest_db].first; request >= 0; request = manifest_nodes[request].next) {
        int name_match = -1, database = request, engine, grant, url;
        for (granted = grant_nodes[grant_db].first; granted >= 0; granted = grant_nodes[granted].next) {
            if (string_equal_raw(manifest_nodes[request].key_start + 1, manifest_nodes[request].key_end - 1,
                                 grant_nodes[granted].key_start + 1, grant_nodes[granted].key_end - 1)) {
                name_match = granted; break;
            }
        }
        if (name_match < 0) return 0;
        {
            static const tl_field_spec db_spec[] = {{"engine",1},{"config",1}};
            static const tl_field_spec grant_spec[] = {{"url",1}};
            if (!shape_object(manifest_nodes, database, db_spec, 2) ||
                !shape_object(grant_nodes, name_match, grant_spec, 1)) return 0;
        }
        engine = field(manifest_nodes, database, "engine");
        grant = name_match;
        url = field(grant_nodes, grant, "url");
        if (engine < 0 || !valid_database_url(&grant_nodes[url], string_equal(&manifest_nodes[engine], "postgres") ? "postgres" : "sqlite")) return 0;
    }
    for (granted = grant_nodes[grant_db].first; granted >= 0; granted = grant_nodes[granted].next) {
        int found = 0;
        for (request = manifest_nodes[manifest_db].first; request >= 0; request = manifest_nodes[request].next) {
            if (string_equal_raw(manifest_nodes[request].key_start + 1, manifest_nodes[request].key_end - 1,
                                 grant_nodes[granted].key_start + 1, grant_nodes[granted].key_end - 1)) { found = 1; break; }
        }
        if (!found) return 0;
    }
    return 1;
}

static int current_os(const tl_json_node* node) {
#if defined(__linux__)
    return string_equal(node, "linux");
#elif defined(__APPLE__)
    return string_equal(node, "darwin");
#elif defined(_WIN32)
    return string_equal(node, "windows");
#elif defined(__FreeBSD__)
    return string_equal(node, "freebsd");
#else
    (void)node; return 0;
#endif
}

static int current_arch(const tl_json_node* node) {
#if defined(__x86_64__) || defined(_M_X64)
    return string_equal(node, "amd64");
#elif defined(__aarch64__) || defined(_M_ARM64)
    return string_equal(node, "arm64");
#elif defined(__i386__) || defined(_M_IX86)
    return string_equal(node, "386");
#elif defined(__arm__)
    return string_equal(node, "arm");
#else
    (void)node; return 0;
#endif
}

static int target_supported(const tl_json_node* nodes, int manifest) {
    static const tl_field_spec target_spec[] = {{"os",1},{"arch",1}};
    int target = field(nodes, manifest, "target"), os, arch, child;
    int has_os = 0, has_arch = 0;
    if (!shape_object(nodes, target, target_spec, 2)) return 0;
    os = field(nodes, target, "os"); arch = field(nodes, target, "arch");
    if (!array_kind(nodes, os, J_STRING) || !array_kind(nodes, arch, J_STRING)) return 0;
    if (nodes[os].first < 0) has_os = 1;
    for (child = nodes[os].first; child >= 0; child = nodes[child].next) {
        int other;
        for (other = nodes[os].first; other != child; other = nodes[other].next)
            if (string_equal_raw(nodes[other].start + 1, nodes[other].end - 1,
                                 nodes[child].start + 1, nodes[child].end - 1)) return 0;
        if (current_os(&nodes[child])) has_os = 1;
    }
    if (nodes[arch].first < 0) has_arch = 1;
    for (child = nodes[arch].first; child >= 0; child = nodes[child].next) {
        int other;
        for (other = nodes[arch].first; other != child; other = nodes[other].next)
            if (string_equal_raw(nodes[other].start + 1, nodes[other].end - 1,
                                 nodes[child].start + 1, nodes[child].end - 1)) return 0;
        if (current_arch(&nodes[child])) has_arch = 1;
    }
    return has_os && has_arch;
}

static int decode_string_value(const tl_json_node* node, char* out, size_t capacity) {
    const char* p;
    const char* end;
    size_t used = 0;
    if (node == NULL || node->kind != J_STRING) return 0;
    p = node->start + 1; end = node->end - 1;
    while (p < end) {
        uint32_t cp;
        if (!string_codepoint(&p, end, &cp)) return 0;
        if (cp < 0x80) {
            if (used + 1 >= capacity) return 0;
            out[used++] = (char)cp;
        } else if (cp < 0x800) {
            if (used + 2 >= capacity) return 0;
            out[used++] = (char)(0xC0 | (cp >> 6)); out[used++] = (char)(0x80 | (cp & 0x3F));
        } else if (cp < 0x10000) {
            if (used + 3 >= capacity) return 0;
            out[used++] = (char)(0xE0 | (cp >> 12)); out[used++] = (char)(0x80 | ((cp >> 6) & 0x3F)); out[used++] = (char)(0x80 | (cp & 0x3F));
        } else {
            if (used + 4 >= capacity) return 0;
            out[used++] = (char)(0xF0 | (cp >> 18)); out[used++] = (char)(0x80 | ((cp >> 12) & 0x3F));
            out[used++] = (char)(0x80 | ((cp >> 6) & 0x3F)); out[used++] = (char)(0x80 | (cp & 0x3F));
        }
    }
    out[used] = '\0';
    return 1;
}

static void retain_env_names(const tlang_program* prog, char*** names_out, size_t* count_out) {
    tl_json_node* nodes;
    size_t count = 0, i = 0;
    int root, caps, env, child;
    *names_out = NULL; *count_out = 0;
    if (prog->manifest_json.data == NULL || prog->manifest_json.len == 0) return;
    nodes = (tl_json_node*)calloc(TL_GRANTS_MAX_NODES, sizeof *nodes);
    if (nodes == NULL) return;
    root = parse_document(prog->manifest_json.data, prog->manifest_json.len, nodes, &count);
    caps = root >= 0 ? field(nodes, root, "capabilities") : -1;
    env = caps >= 0 ? field(nodes, caps, "env") : -1;
    if (env >= 0) {
        *count_out = child_count(nodes, env);
        *names_out = (char**)calloc(*count_out ? *count_out : 1, sizeof **names_out);
        if (*names_out != NULL) for (child = nodes[env].first; child >= 0; child = nodes[child].next) {
            size_t size = (size_t)(nodes[child].end - nodes[child].start) + 1;
            (*names_out)[i] = (char*)malloc(size);
            if ((*names_out)[i] == NULL || !decode_string_value(&nodes[child], (*names_out)[i], size)) break;
            i++;
        }
        if (i != *count_out) { while (i > 0) free((*names_out)[--i]); free(*names_out); *names_out = NULL; *count_out = 0; }
    }
    free(nodes);
}

static int read_bounded(const char* path, char** data, size_t* len) {
    int fd;
    char* buffer;
    size_t used = 0;
    fd = open(path, O_RDONLY | O_CLOEXEC);
    if (fd < 0) return -1;
    buffer = (char*)malloc(TL_GRANTS_MAX_BYTES + 1);
    if (buffer == NULL) { close(fd); return -1; }
    while (used <= TL_GRANTS_MAX_BYTES) {
        ssize_t n = read(fd, buffer + used, TL_GRANTS_MAX_BYTES + 1 - used);
        if (n < 0) { if (errno == EINTR) continue; free(buffer); close(fd); return -1; }
        if (n == 0) break;
        used += (size_t)n;
    }
    close(fd);
    if (used == 0 || used > TL_GRANTS_MAX_BYTES) { free(buffer); return -1; }
    *data = buffer;
    *len = used;
    return 0;
}

static int validate_documents(const tlang_program* prog, const char* grant_data, size_t grant_len,
                              char** database_url, char*** env_names, size_t* env_count) {
    static const tl_field_spec manifest_spec[] = {{"schemaVersion",1},{"language",1},{"entry",1},
        {"target",1},{"capabilities",1},{"databases",1},{"limits",1}};
    static const tl_field_spec grant_spec[] = {{"schemaVersion",1},{"manifestSha256",1},
        {"capabilities",1},{"databases",1},{"limits",1}};
    tl_json_node* manifest_nodes = NULL;
    tl_json_node* grant_nodes = NULL;
    size_t manifest_count = 0, grant_count = 0;
    int manifest_root, grant_root, field_index, manifest_db, grant_db;
    int result = -1;
    char* manifest_data = NULL;
    size_t manifest_len = prog->manifest_json.len;
    int64_t version;
    if (!prog->has_manifest || prog->manifest_json.data == NULL || manifest_len == 0 || manifest_len > TL_GRANTS_MAX_BYTES ||
        prog->manifest_sha256.data == NULL || prog->manifest_sha256.len != 64 ||
        !digest_matches(prog->manifest_json.data, manifest_len, prog->manifest_sha256)) return -1;
    manifest_data = (char*)malloc(manifest_len + 1);
    manifest_nodes = (tl_json_node*)calloc(TL_GRANTS_MAX_NODES, sizeof *manifest_nodes);
    grant_nodes = (tl_json_node*)calloc(TL_GRANTS_MAX_NODES, sizeof *grant_nodes);
    if (manifest_data == NULL || manifest_nodes == NULL || grant_nodes == NULL) goto done;
    memcpy(manifest_data, prog->manifest_json.data, manifest_len);
    manifest_data[manifest_len] = '\0';
    if (!utf8_document(manifest_data, manifest_len) || !utf8_document(grant_data, grant_len)) goto done;
    manifest_root = parse_document(manifest_data, manifest_len, manifest_nodes, &manifest_count);
    grant_root = parse_document(grant_data, grant_len, grant_nodes, &grant_count);
    if (manifest_root < 0 || grant_root < 0 || !shape_object(manifest_nodes, manifest_root, manifest_spec, 7) ||
        !shape_object(grant_nodes, grant_root, grant_spec, 5)) goto done;
    field_index = field(manifest_nodes, manifest_root, "schemaVersion");
    if (!number_value(&manifest_nodes[field_index], &version) || version != 1) goto done;
    field_index = field(manifest_nodes, manifest_root, "language");
    if (!string_equal(&manifest_nodes[field_index], "1")) goto done;
    field_index = field(grant_nodes, grant_root, "schemaVersion");
    if (!number_value(&grant_nodes[field_index], &version) || version != 1) goto done;
    field_index = field(grant_nodes, grant_root, "manifestSha256");
    if (!string_equal_raw(grant_nodes[field_index].start + 1, grant_nodes[field_index].end - 1,
            prog->manifest_sha256.data, prog->manifest_sha256.data + prog->manifest_sha256.len) ||
        !target_supported(manifest_nodes, manifest_root)) goto done;
    if (!valid_capabilities(manifest_nodes, field(manifest_nodes, manifest_root, "capabilities")) ||
        !valid_capabilities(grant_nodes, field(grant_nodes, grant_root, "capabilities")) ||
        !capabilities_covered(manifest_nodes, field(manifest_nodes, manifest_root, "capabilities"),
            grant_nodes, field(grant_nodes, grant_root, "capabilities"))) goto done;
    {
        int env = field(manifest_nodes, field(manifest_nodes, manifest_root, "capabilities"), "env");
        size_t count = child_count(manifest_nodes, env), i = 0;
        char** names = (char**)calloc(count == 0 ? 1 : count, sizeof *names);
        int child;
        if (names == NULL) goto done;
        for (child = manifest_nodes[env].first; child >= 0; child = manifest_nodes[child].next) {
            size_t cap = (size_t)(manifest_nodes[child].end - manifest_nodes[child].start) + 1;
            names[i] = (char*)malloc(cap);
            if (names[i] == NULL || !decode_string_value(&manifest_nodes[child], names[i], cap)) {
                size_t j; for (j = 0; j <= i; j++) free(names[j]); free(names); goto done;
            }
            i++;
        }
        *env_names = names; *env_count = count;
    }
    manifest_db = field(manifest_nodes, manifest_root, "databases");
    grant_db = field(grant_nodes, grant_root, "databases");
    if (manifest_db < 0 || grant_db < 0 || !valid_databases(manifest_nodes, manifest_db, grant_nodes, grant_db) ||
        !limits_valid(manifest_nodes, field(manifest_nodes, manifest_root, "limits"), 0) ||
        !limits_valid(grant_nodes, field(grant_nodes, grant_root, "limits"), 1) ||
        !limits_covered(manifest_nodes, field(manifest_nodes, manifest_root, "limits"),
            grant_nodes, field(grant_nodes, grant_root, "limits"))) goto done;
    if (prog->uses_db) {
        int db = manifest_nodes[manifest_db].first;
        if (db < 0 || manifest_nodes[db].next >= 0) goto done;
        {
            int grant = grant_nodes[grant_db].first;
            int grant_url;
            char* decoded;
            while (grant >= 0 && !string_equal_raw(manifest_nodes[db].key_start + 1, manifest_nodes[db].key_end - 1,
                    grant_nodes[grant].key_start + 1, grant_nodes[grant].key_end - 1)) grant = grant_nodes[grant].next;
            if (grant < 0) goto done;
            grant_url = field(grant_nodes, grant, "url");
            decoded = (char*)malloc(TL_GRANTS_MAX_BYTES + 1);
            if (decoded == NULL || !decode_string_value(&grant_nodes[grant_url], decoded, TL_GRANTS_MAX_BYTES + 1)) { free(decoded); goto done; }
            *database_url = decoded;
        }
    } else if (manifest_nodes[manifest_db].first >= 0) {
        goto done;
    }
    result = 0;
done:
    free(manifest_data);
    free(manifest_nodes);
    free(grant_nodes);
    return result;
}

int tlang_grants_load_validate(const tlang_program* prog, const char* path,
                               tlang_config* cfg, char* err, size_t errlen) {
    char* data = NULL;
    char* database_url = NULL;
    char** env_names = NULL;
    size_t env_count = 0;
    size_t len = 0;
    int result = -1;
    if (errlen > 0) err[0] = '\0';
    if (!prog->has_manifest) {
        if (path != NULL) goto invalid;
        if (prog->env_name_count > 0 && prog->env_names == NULL) goto invalid;
        g_env_program = prog;
        retain_env_names(prog, &env_names, &env_count);
        g_env_names = env_names; g_env_count = env_count;
        return 0;
    }
    if (path == NULL || path[0] == '\0' || read_bounded(path, &data, &len) != 0) goto invalid;
    if (validate_documents(prog, data, len, &database_url, &env_names, &env_count) != 0) goto invalid;
    cfg->database_url = database_url;
    g_env_names = env_names;
    g_env_count = env_count;
    g_env_program = prog;
    result = 0;
    goto done;
invalid:
    if (errlen > 0) snprintf(err, errlen, "runtime grants are missing, invalid, or insufficient");
done:
    if (result != 0) {
        size_t i; free(database_url);
        for (i = 0; i < env_count; i++) free(env_names[i]);
        free(env_names);
    }
    free(data);
    return result;
}
