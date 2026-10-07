/*
 * arena.c - the bump-allocator arena lifecycle and the out-of-memory handler
 * (tlang.h §4/§7, tlang_internal.h §3). docs/RUNTIME.md §6.
 *
 * arena_alloc / tlang_alloc_raw / tlang_alloc_zeroed are inline in tlang.h and
 * call into arena_grow / tlang_alloc_failed here. On OOM (or an overflowing
 * size) tlang_alloc_failed aborts the current request (503) through the
 * request-abort path when a fiber is armed, else logs and exits(1).
 */
#include "tlang_internal.h"

#include <stdlib.h>

/* Allocates a chunk with capacity max(ARENA_CHUNK_SIZE, n), appends it after
 * a->current (always the last chunk) and makes it current. Returns NULL (arena
 * unchanged) when malloc fails or sizeof(ArenaChunk) + capacity overflows. */
ArenaChunk* arena_grow(MemoryArena* a, size_t n) {
    size_t capacity = n > ARENA_CHUNK_SIZE ? n : (size_t)ARENA_CHUNK_SIZE;
    ArenaChunk* c;

    if (capacity > SIZE_MAX - sizeof(ArenaChunk)) return NULL;
    c = (ArenaChunk*)malloc(sizeof(ArenaChunk) + capacity);
    if (c == NULL) return NULL;
    c->next = NULL;
    c->capacity = capacity;
    c->offset = 0;
    a->current->next = c;
    a->current = c;
    return c;
}

/* Frees every chunk after the first, resets the first chunk and makes it
 * current again. */
void arena_reset(MemoryArena* a) {
    ArenaChunk* c = a->first->next;
    while (c != NULL) {
        ArenaChunk* next = c->next;
        free(c);
        c = next;
    }
    a->first->next = NULL;
    a->first->offset = 0;
    a->current = a->first;
}

/* Out-of-memory handler. Inside a request (f->abort_armed) it aborts with 503
 * through the request-abort path; otherwise it logs the size and exits(1).
 * fib may be NULL (then it always exits). */
_Noreturn void tlang_alloc_failed(tlang_fiber* fib, size_t size) {
    if (fib != NULL) {
        Fiber* f = tlang_fiber_of(fib);
        if (f->abort_armed) {
            tlang_request_abort(f, TLANG_STATUS_UNAVAILABLE, TLANG_MSG_OUT_OF_MEMORY);
        }
    }
    tlang_log_error("out of memory (allocating %zu bytes)", size);
    exit(1);
}

/* new global T(): zeroed heap memory, at least 16-byte aligned. Aborts on OOM
 * (exits outside a request). fib may be NULL. */
void* tlang_alloc_global_zeroed(tlang_fiber* fib, size_t size) {
    void* p = calloc(1, size == 0 ? 1 : size);
    if (p == NULL) tlang_alloc_failed(fib, size);
    return p;
}

/* Allocates the first chunk (ARENA_CHUNK_SIZE) and makes it current. Returns
 * 0, or -1 when malloc fails (*a is then {NULL, NULL}). */
int tlang_arena_init(MemoryArena* a) {
    ArenaChunk* c = (ArenaChunk*)malloc(sizeof(ArenaChunk) + (size_t)ARENA_CHUNK_SIZE);
    if (c == NULL) {
        a->first = NULL;
        a->current = NULL;
        return -1;
    }
    c->next = NULL;
    c->capacity = (size_t)ARENA_CHUNK_SIZE;
    c->offset = 0;
    a->first = c;
    a->current = c;
    return 0;
}

/* Frees every chunk, including the first; *a becomes {NULL, NULL}. Safe on a
 * {NULL, NULL} arena. */
void tlang_arena_destroy(MemoryArena* a) {
    ArenaChunk* c = a->first;
    while (c != NULL) {
        ArenaChunk* next = c->next;
        free(c);
        c = next;
    }
    a->first = NULL;
    a->current = NULL;
}
