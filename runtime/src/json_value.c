/* Opaque generic JSON values for the Phase 2.5 logging surface. */
#include "tlang_internal.h"

#include <stdlib.h>

typedef struct { tlang_json_value** items; int64_t len; } json_array;

typedef struct {
    tlang_string key;
    tlang_json_value* value;
} json_member;

struct tlang_json_value {
    int kind;
    union {
        bool boolean;
        double number;
        tlang_string string;
        json_array array;
        struct { json_member* items; int64_t len; } object;
    } as;
};

bool tlang_json_is_object(const tlang_json_value* value) {
    return value != NULL && value->kind == TLANG_JSON_OBJECT;
}

bool tlang_json_is_null(const tlang_json_value* value) {
    return value == NULL || value->kind == TLANG_JSON_NULL;
}

static void json_write_member_string(tlang_buf* b, tlang_string s) { json_write_str_utf8(b, s); }
void tlang_json_write_value(tlang_buf* b, const tlang_json_value* value, int depth) {
    int64_t i;
    if (value == NULL) { json_write_null(b); return; }
    if (depth > json_max_depth()) { json_write_null(b); return; }
    switch (value->kind) {
    case TLANG_JSON_NULL: json_write_null(b); break;
    case TLANG_JSON_BOOL: json_write_bool(b, value->as.boolean); break;
    case TLANG_JSON_NUMBER: json_write_f64(b, value->as.number); break;
    case TLANG_JSON_STRING: json_write_str_utf8(b, value->as.string); break;
    case TLANG_JSON_ARRAY:
        tlang_buf_putc(b, '[');
        for (i = 0; i < value->as.array.len; i++) {
            if (i != 0) tlang_buf_putc(b, ',');
            tlang_json_write_value(b, value->as.array.items[i], depth + 1);
        }
        tlang_buf_putc(b, ']');
        break;
    case TLANG_JSON_OBJECT:
        tlang_buf_putc(b, '{');
        for (i = 0; i < value->as.object.len; i++) {
            if (i != 0) tlang_buf_putc(b, ',');
            json_write_member_string(b, value->as.object.items[i].key);
            tlang_buf_putc(b, ':');
            tlang_json_write_value(b, value->as.object.items[i].value, depth + 1);
        }
        tlang_buf_putc(b, '}');
        break;
    default: json_write_null(b); break;
    }
}

tlang_string tlang_json_describe(const tlang_json_value* value) {
    return tlang_json_kind(value);
}

static tlang_json_value* json_new(tlang_fiber* fib, int kind) {
    tlang_json_value* value = (tlang_json_value*)tlang_alloc_zeroed(fib, sizeof *value);
    value->kind = kind;
    return value;
}

tlang_json_value* tlang_json_null(tlang_fiber* fib) { return json_new(fib, TLANG_JSON_NULL); }
tlang_json_value* tlang_json_bool(tlang_fiber* fib, bool v) {
    tlang_json_value* value = json_new(fib, TLANG_JSON_BOOL); value->as.boolean = v; return value;
}
tlang_json_value* tlang_json_number(tlang_fiber* fib, double v) {
    tlang_json_value* value = json_new(fib, TLANG_JSON_NUMBER); value->as.number = v; return value;
}
tlang_json_value* tlang_json_string(tlang_fiber* fib, tlang_string v) {
    tlang_json_value* value = json_new(fib, TLANG_JSON_STRING); value->as.string = tlang_str_clone(fib, tlang_str_some(v)); return value;
}
tlang_json_value* tlang_json_array(tlang_fiber* fib, const tlang_json_value* const* values, int64_t len) {
    tlang_json_value* value;
    json_array* items;
    int64_t i;
    if (len < 0) {
        tlang_throw_typed(fib, 400, TLANG_STR("JsonValue.array length cannot be negative"),
                          TLANG_STR(TLANG_ERROR_INVALID_INPUT), TLANG_STR("json_invalid_array"));
        return NULL;
    }
    value = json_new(fib, TLANG_JSON_ARRAY);
    items = (json_array*)tlang_alloc_zeroed(fib, sizeof *items);
    if (len > 0) {
        if (values == NULL) {
            tlang_throw_typed(fib, 400, TLANG_STR("JsonValue.array requires values"),
                              TLANG_STR(TLANG_ERROR_INVALID_INPUT), TLANG_STR("json_invalid_array"));
            return NULL;
        }
        items->items = (tlang_json_value**)tlang_alloc_raw(fib, (size_t)len * sizeof *items->items);
        for (i = 0; i < len; i++) items->items[i] = (tlang_json_value*)values[i];
        items->len = len;
    }
    value->as.array = *items;
    return value;
}
tlang_json_value* tlang_json_object(tlang_fiber* fib, const tlang_string* keys,
                                   const tlang_json_value* const* values, int64_t len) {
    int64_t i, j;
    tlang_json_value* object;
    if (len < 0) {
        tlang_throw_typed(fib, 400, TLANG_STR("JsonValue.object length cannot be negative"),
                          TLANG_STR(TLANG_ERROR_INVALID_INPUT), TLANG_STR("json_invalid_object"));
        return NULL;
    }
    if (len > 0 && (keys == NULL || values == NULL)) {
        tlang_throw_typed(fib, 400, TLANG_STR("JsonValue.object requires matching keys and values"),
                          TLANG_STR(TLANG_ERROR_INVALID_INPUT), TLANG_STR("json_invalid_object"));
        return NULL;
    }
    if (len > INT64_MAX / (int64_t)sizeof(json_member)) {
        tlang_throw_typed(fib, 400, TLANG_STR("JsonValue.object is too large"),
                          TLANG_STR(TLANG_ERROR_LIMIT), TLANG_STR("json_object_too_large"));
        return NULL;
    }
    for (i = 0; i < len; i++) {
        for (j = 0; j < i; j++) if (tlang_str_eq(keys[i], keys[j])) {
            tlang_throw_fmt_typed(fib, 400, TLANG_STR(TLANG_ERROR_INVALID_INPUT), TLANG_STR("json_duplicate_key"), "duplicate JsonValue object key");
            return NULL;
        }
    }
    object = json_new(fib, TLANG_JSON_OBJECT);
    object->as.object.items = (json_member*)tlang_alloc_raw(fib, (size_t)(len == 0 ? 1 : len) * sizeof(json_member));
    object->as.object.len = len;
    for (i = 0; i < len; i++) {
        object->as.object.items[i].key = tlang_str_clone(fib, keys[i]);
        object->as.object.items[i].value = (tlang_json_value*)values[i];
    }
    return object;
}

tlang_string tlang_json_kind(const tlang_json_value* value) {
    static const tlang_string kinds[] = {TLANG_STR_INIT("null"), TLANG_STR_INIT("bool"), TLANG_STR_INIT("number"), TLANG_STR_INIT("string"), TLANG_STR_INIT("array"), TLANG_STR_INIT("object")};
    return value == NULL || value->kind < 0 || value->kind > TLANG_JSON_OBJECT ? TLANG_STR("") : kinds[value->kind];
}
tlang_opt_bool tlang_json_as_bool(const tlang_json_value* value) {
    if (value == NULL || value->kind != TLANG_JSON_BOOL) return TLANG_NONE(bool);
    return TLANG_SOME(bool, value->as.boolean);
}
tlang_opt_f64 tlang_json_as_number(const tlang_json_value* value) {
    if (value == NULL || value->kind != TLANG_JSON_NUMBER) return TLANG_NONE(f64);
    return TLANG_SOME(f64, value->as.number);
}
tlang_opt_string tlang_json_as_string(const tlang_json_value* value) {
    if (value == NULL || value->kind != TLANG_JSON_STRING) return (tlang_opt_string){false, TLANG_STR("")};
    return (tlang_opt_string){true, value->as.string};
}
tlang_slice_opt_JsonValue* tlang_json_array_values(tlang_fiber* fib, const tlang_json_value* value) {
    tlang_slice_opt_JsonValue* values; int64_t i;
    if (value == NULL || value->kind != TLANG_JSON_ARRAY) return NULL;
    values = (tlang_slice_opt_JsonValue*)tlang_slice_new(fib, sizeof(tlang_slice_opt_JsonValue), false);
    values->items = (tlang_opt_json_value*)tlang_slice_grow(fib, NULL, 0, &values->cap, value->as.array.len, false, sizeof *values->items);
    for (i = 0; i < value->as.array.len; i++) values->items[i] = (tlang_opt_json_value){true, value->as.array.items[i]};
    values->len = value->as.array.len;
    return values;
}
tlang_slice_str* tlang_json_object_keys(tlang_fiber* fib, const tlang_json_value* value) {
    tlang_slice_str* keys; int64_t i;
    if (value == NULL || value->kind != TLANG_JSON_OBJECT) return NULL;
    keys = (tlang_slice_str*)tlang_slice_new(fib, sizeof(tlang_slice_str), false);
    keys->items = (tlang_string*)tlang_slice_grow(fib, NULL, 0, &keys->cap, value->as.object.len, false, sizeof *keys->items);
    for (i = 0; i < value->as.object.len; i++) keys->items[i] = value->as.object.items[i].key;
    keys->len = value->as.object.len;
    return keys;
}
tlang_slice_opt_JsonValue* tlang_json_object_values(tlang_fiber* fib, const tlang_json_value* value) {
    tlang_slice_opt_JsonValue* values; int64_t i;
    if (value == NULL || value->kind != TLANG_JSON_OBJECT) return NULL;
    values = (tlang_slice_opt_JsonValue*)tlang_slice_new(fib, sizeof(tlang_slice_opt_JsonValue), false);
    values->items = (tlang_opt_json_value*)tlang_slice_grow(fib, NULL, 0, &values->cap, value->as.object.len, false, sizeof *values->items);
    for (i = 0; i < value->as.object.len; i++) values->items[i] = (tlang_opt_json_value){true, value->as.object.items[i].value};
    values->len = value->as.object.len;
    return values;
}
tlang_json_value* tlang_json_get(tlang_fiber* fib, const tlang_json_value* value, tlang_string key) {
    int64_t i;
    (void)fib;
    if (value == NULL || value->kind != TLANG_JSON_OBJECT) return NULL;
    for (i = 0; i < value->as.object.len; i++)
        if (tlang_str_eq(value->as.object.items[i].key, key)) return value->as.object.items[i].value;
    return NULL;
}
