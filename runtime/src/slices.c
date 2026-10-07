/*
 * slices.c - slice allocation and growth. docs/RUNTIME.md §4/§6 assigns these
 * functions here; the contracts are in tlang.h §11 and tlang_internal.h §11.
 *
 * Header fields are written through scalar types at the TLANG_SLICE_OFF_*
 * offsets (never through a "generic slice" struct type) so a header built here
 * can be read through any tlang_slice_<m> type without breaking strict
 * aliasing / TBAA after LTO (RUNTIME.md §6).
 */
#include "tlang_internal.h"

#include <stdlib.h>

void tlang_slice_hdr_init(void* hdr, void* items, int64_t len, int64_t cap, bool global) {
    char* base = (char*)hdr;
    memcpy(base + TLANG_SLICE_OFF_ITEMS, &items, sizeof items);
    memcpy(base + TLANG_SLICE_OFF_LEN, &len, sizeof len);
    memcpy(base + TLANG_SLICE_OFF_CAP, &cap, sizeof cap);
    memcpy(base + TLANG_SLICE_OFF_GLOBAL, &global, sizeof global);
}

void* tlang_slice_new(tlang_fiber* fib, size_t header_size, bool global) {
    void* hdr;
    if (global) {
        hdr = tlang_alloc_global_zeroed(fib, header_size);
    } else {
        hdr = tlang_alloc_zeroed(fib, header_size);
    }
    /* Zeroed memory already gives items=NULL, len=0, cap=0, global=false; set
     * the global flag through the scalar-typed path for the heap case. */
    tlang_slice_hdr_init(hdr, NULL, 0, 0, global);
    return hdr;
}

void* tlang_slice_grow(tlang_fiber* fib, void* items, int64_t len, int64_t* cap,
                       int64_t min_cap, bool global, size_t elem_size) {
    int64_t oldcap = *cap;
    int64_t newcap;
    uint64_t bytes;

    if (oldcap >= min_cap) return items;

    /* New capacity: max(min_cap, 2*cap, 4). Guard the doubling and the final
     * byte size against int64_t / size_t overflow (treated as OOM). */
    newcap = min_cap;
    if (oldcap > 0) {
        if (oldcap > INT64_MAX / 2) {
            tlang_alloc_failed(fib, SIZE_MAX);
        } else if (2 * oldcap > newcap) {
            newcap = 2 * oldcap;
        }
    }
    if (newcap < 4) newcap = 4;
    if (newcap < 0) tlang_alloc_failed(fib, SIZE_MAX);

    bytes = (uint64_t)newcap * (uint64_t)elem_size;
    if (elem_size != 0 && bytes / (uint64_t)elem_size != (uint64_t)newcap) {
        tlang_alloc_failed(fib, SIZE_MAX);
    }
    if (bytes > SIZE_MAX) tlang_alloc_failed(fib, SIZE_MAX);

    if (global) {
        void* p = realloc(items, (size_t)bytes);
        if (p == NULL && bytes != 0) tlang_alloc_failed(fib, (size_t)bytes);
        *cap = newcap;
        return p;
    } else {
        MemoryArena* a = fib->arena;
        ArenaChunk* c = a->current;
        size_t newbytes = (size_t)bytes;
        size_t oldbytes = (size_t)((uint64_t)oldcap * (uint64_t)elem_size);
        char* np;

        /* In-place growth when the current items are the arena's last
         * allocation and the chunk still has room for the larger block. */
        if (items != NULL && oldbytes != 0 && c != NULL) {
            size_t old_aligned = (oldbytes + (ARENA_ALIGNMENT - 1)) & ~(size_t)(ARENA_ALIGNMENT - 1);
            if (old_aligned <= c->offset &&
                (char*)(c->data + (c->offset - old_aligned)) == (char*)items) {
                size_t new_aligned = (newbytes + (ARENA_ALIGNMENT - 1)) & ~(size_t)(ARENA_ALIGNMENT - 1);
                size_t grow_by = new_aligned - old_aligned;
                if (grow_by <= c->capacity - c->offset) {
                    c->offset += grow_by;
                    *cap = newcap;
                    return items;
                }
            }
        }

        np = (char*)tlang_alloc_raw(fib, newbytes);
        if (len > 0 && items != NULL) {
            memcpy(np, items, (size_t)((uint64_t)len * (uint64_t)elem_size));
        }
        *cap = newcap;
        return np;
    }
}

void* tlang_slice_oob(tlang_fiber* fib, int64_t index, int64_t len, size_t elem_size) {
    (void)index;
    (void)len;
    tlang_throw_index(fib);
    /* Return a zeroed scratch block so the caller's read yields the zero value
     * and a write stays in bounds (and is discarded). */
    return tlang_alloc_zeroed(fib, elem_size == 0 ? 1 : elem_size);
}
