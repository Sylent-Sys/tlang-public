/*
 * ctxswitch.c - fiber context switch (tlang_internal.h §4, spec §7.4).
 *
 * Two implementations selected by TLANG_USE_UCONTEXT (tlang_internal.h §1):
 *
 *  - Assembly switch for x86_64 and aarch64. tlang_fctx_switch_asm saves the
 *    callee-saved registers plus the floating-point control words on the
 *    current stack, swaps the stack pointer, and restores the other context's
 *    saved registers. tlang_fctx_init lays a synthetic frame on a fresh stack
 *    so that the first switch "returns" into a bootstrap that calls
 *    entry(arg). No thread-local storage is used: entry and arg travel in
 *    register slots of the synthetic frame. C wrappers (tlang_fctx_switch /
 *    tlang_fctx_exit) add the ASan annotations around the raw asm switch.
 *
 *  - ucontext fallback (TCC, or non-x86_64/aarch64). makecontext takes int
 *    arguments only, so the 64-bit arg pointer is split into two int halves
 *    and reassembled by the trampoline.
 *
 * Under TLANG_ASAN every switch is wrapped with
 * __sanitizer_start_switch_fiber / __sanitizer_finish_switch_fiber;
 * tlang_fctx_exit passes NULL as the fake-stack save slot so the finished
 * run's fake stack is released. Inline assembly is allowed only in this file.
 */
#include "tlang_internal.h"

#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>

#if TLANG_ASAN
#include <sanitizer/common_interface_defs.h>
#endif

/* ------------------------------------------------------------------ */
/* ASan switch annotations.                                            */
/* ------------------------------------------------------------------ */
#if TLANG_ASAN
static inline void tl_asan_start(FiberContext* from, FiberContext* to, int final) {
    __sanitizer_start_switch_fiber(final ? NULL : &from->asan_fake_stack,
                                   to->asan_stack_bottom, to->asan_stack_size);
}
static inline void tl_asan_finish(FiberContext* self) {
    /* The output bounds describe the context that just yielded to us; it has
     * already recorded its own bounds, so we discard them (pass NULL). */
    __sanitizer_finish_switch_fiber(self->asan_fake_stack, NULL, NULL);
}
/* Called at the very top of a freshly started fiber run (from the fiber
 * trampoline), to complete the switch the spawner began. The fresh run has no
 * prior fake stack of its own, so NULL is passed as the save slot. */
void tlang_fctx_finish_switch(FiberContext* self) {
    (void)self;
    __sanitizer_finish_switch_fiber(NULL, NULL, NULL);
}
static void tl_asan_record_thread(FiberContext* ctx) {
    pthread_attr_t attr;
    void* base = NULL;
    size_t sz = 0;
    if (pthread_getattr_np(pthread_self(), &attr) == 0) {
        pthread_attr_getstack(&attr, &base, &sz);
        pthread_attr_destroy(&attr);
    }
    ctx->asan_fake_stack = NULL;
    ctx->asan_stack_bottom = base;
    ctx->asan_stack_size = sz;
}
#else
static inline void tl_asan_start(FiberContext* from, FiberContext* to, int final) {
    (void)from; (void)to; (void)final;
}
static inline void tl_asan_finish(FiberContext* self) { (void)self; }
/* No-op without ASan; the fiber trampoline calls it unconditionally. */
void tlang_fctx_finish_switch(FiberContext* self) { (void)self; }
#endif

/* ================================================================== */
#if defined(TLANG_USE_UCONTEXT)
/* ================================================================== */

typedef struct {
    void (*entry)(void* arg);
    void* arg;
} tl_uc_boot;

/* makecontext trampolines must be plain functions receiving int halves. The
 * boot info is stashed on the new stack by tlang_fctx_init and reassembled
 * here. */
static void tl_uc_trampoline(unsigned hi, unsigned lo) {
    uintptr_t p = ((uintptr_t)hi << 32) | (uintptr_t)lo;
    tl_uc_boot* boot = (tl_uc_boot*)(void*)p;
    void (*entry)(void*) = boot->entry;
    void* arg = boot->arg;
    entry(arg);
    /* entry must never return (it ends with tlang_fctx_exit). */
    abort();
}

void tlang_fctx_init(FiberContext* ctx, void* stack_lo, size_t stack_size,
                     void (*entry)(void* arg), void* arg) {
    tl_uc_boot* boot;
    unsigned hi, lo;
    uintptr_t top;

    /* Stash the boot info at the very top of the stack, 16-byte aligned, and
     * hand the shrunk region to makecontext. */
    top = (uintptr_t)stack_lo + stack_size;
    top &= ~(uintptr_t)15;
    top -= sizeof(tl_uc_boot);
    top &= ~(uintptr_t)15;
    boot = (tl_uc_boot*)top;
    boot->entry = entry;
    boot->arg = arg;

    getcontext(&ctx->uc);
    ctx->uc.uc_stack.ss_sp = stack_lo;
    ctx->uc.uc_stack.ss_size = (size_t)(top - (uintptr_t)stack_lo);
    ctx->uc.uc_link = NULL;
    hi = (unsigned)(((uintptr_t)boot) >> 32);
    lo = (unsigned)(((uintptr_t)boot) & 0xffffffffu);
    makecontext(&ctx->uc, (void (*)(void))tl_uc_trampoline, 2, hi, lo);

#if TLANG_ASAN
    ctx->asan_fake_stack = NULL;
    ctx->asan_stack_bottom = stack_lo;
    ctx->asan_stack_size = stack_size;
#endif
}

void tlang_fctx_init_thread(FiberContext* ctx) {
    getcontext(&ctx->uc);
#if TLANG_ASAN
    tl_asan_record_thread(ctx);
#endif
}

void tlang_fctx_switch(FiberContext* from, FiberContext* to) {
    tl_asan_start(from, to, 0);
    swapcontext(&from->uc, &to->uc);
    tl_asan_finish(from);
}

_Noreturn void tlang_fctx_exit(FiberContext* from, FiberContext* to) {
    tl_asan_start(from, to, 1);
    swapcontext(&from->uc, &to->uc);
    /* The finished run is never resumed. */
    abort();
}

/* ================================================================== */
#elif defined(__x86_64__) || defined(__aarch64__)
/* ================================================================== */

/* Raw stack-swapping switch, defined in assembly below. It saves the
 * callee-saved registers of the caller on the current stack, stores the
 * stack pointer into from->sp, loads to->sp and restores. The first switch
 * into a context prepared by tlang_fctx_init "returns" into tl_asm_bootstrap. */
void tlang_fctx_switch_asm(void** from_sp, void* to_sp);
void tl_asm_bootstrap(void);

#if defined(__x86_64__)

/* Suspended frame written by the switch prologue (addresses increasing):
 *   sp+0  : r15
 *   sp+8  : r14
 *   sp+16 : r13
 *   sp+24 : r12
 *   sp+32 : rbx
 *   sp+40 : rbp
 *   sp+48 : [fcw:16][pad:16][mxcsr:32]  (16-byte fpu save area)
 *   sp+64 : return address
 * tlang_fctx_init builds this synthetic frame with r13=entry, r14=arg and the
 * return address pointing at tl_asm_bootstrap. */
__asm__(
    ".text\n"
    ".globl tlang_fctx_switch_asm\n"
    ".type tlang_fctx_switch_asm,@function\n"
    "tlang_fctx_switch_asm:\n"       /* rdi = &from->sp, rsi = to_sp */
    "    pushq %rbp\n"
    "    pushq %rbx\n"
    "    pushq %r12\n"
    "    pushq %r13\n"
    "    pushq %r14\n"
    "    pushq %r15\n"
    "    subq $16, %rsp\n"
    "    fnstcw (%rsp)\n"
    "    stmxcsr 4(%rsp)\n"
    "    movq %rsp, (%rdi)\n"        /* *from_sp = rsp */
    "    movq %rsi, %rsp\n"          /* rsp = to_sp */
    "    ldmxcsr 4(%rsp)\n"
    "    fldcw (%rsp)\n"
    "    addq $16, %rsp\n"
    "    popq %r15\n"
    "    popq %r14\n"
    "    popq %r13\n"
    "    popq %r12\n"
    "    popq %rbx\n"
    "    popq %rbp\n"
    "    ret\n"
    ".size tlang_fctx_switch_asm,.-tlang_fctx_switch_asm\n"
);

/* Bootstrap entered by the first switch: r13 = entry, r14 = arg. */
__asm__(
    ".text\n"
    ".globl tl_asm_bootstrap\n"
    ".type tl_asm_bootstrap,@function\n"
    "tl_asm_bootstrap:\n"
    "    movq %r14, %rdi\n"          /* arg */
    "    xorl %ebp, %ebp\n"
    "    callq *%r13\n"              /* entry(arg); rsp 16-byte aligned here */
    "    ud2\n"
    ".size tl_asm_bootstrap,.-tl_asm_bootstrap\n"
);

static void* tl_build_frame(void* stack_lo, size_t stack_size,
                            void (*entry)(void* arg), void* arg) {
    uintptr_t top = ((uintptr_t)stack_lo + stack_size) & ~(uintptr_t)15;
    uint64_t* sp = (uint64_t*)top;
    uint16_t* fcw;
    uint32_t* mxcsr;

    /* The return address slot sits at (top - 8). After the switch epilogue
     * does `ret`, rsp becomes `top` (16-byte aligned), so the bootstrap can
     * `call entry` with the ABI-required 16-byte alignment. */
    *--sp = (uint64_t)(uintptr_t)tl_asm_bootstrap;  /* return address */
    *--sp = 0;                                      /* rbp */
    *--sp = 0;                                      /* rbx */
    *--sp = 0;                                      /* r12 */
    *--sp = (uint64_t)(uintptr_t)entry;             /* r13 = entry */
    *--sp = (uint64_t)(uintptr_t)arg;               /* r14 = arg */
    *--sp = 0;                                      /* r15 */
    sp = (uint64_t*)((char*)sp - 16);               /* fpu/mxcsr save area */
    fcw = (uint16_t*)sp;
    mxcsr = (uint32_t*)((char*)sp + 4);
    *fcw = 0x037f;
    *mxcsr = 0x1f80;
    return sp;
}

#else  /* __aarch64__ */

/* Frame (addresses increasing from sp):
 *   sp+0   x19   sp+8   x20
 *   sp+16  x21   sp+24  x22
 *   sp+32  x23   sp+40  x24
 *   sp+48  x25   sp+56  x26
 *   sp+64  x27   sp+72  x28
 *   sp+80  x29   sp+88  x30 (lr)
 *   sp+96  d8  ... sp+152 d15
 * tlang_fctx_init sets x19=entry, x20=arg, x30=tl_asm_bootstrap. */
__asm__(
    ".text\n"
    ".globl tlang_fctx_switch_asm\n"
    ".type tlang_fctx_switch_asm,%function\n"
    "tlang_fctx_switch_asm:\n"       /* x0 = &from->sp, x1 = to_sp */
    "    sub sp, sp, #160\n"
    "    stp x19, x20, [sp, #0]\n"
    "    stp x21, x22, [sp, #16]\n"
    "    stp x23, x24, [sp, #32]\n"
    "    stp x25, x26, [sp, #48]\n"
    "    stp x27, x28, [sp, #64]\n"
    "    stp x29, x30, [sp, #80]\n"
    "    stp d8, d9,   [sp, #96]\n"
    "    stp d10, d11, [sp, #112]\n"
    "    stp d12, d13, [sp, #128]\n"
    "    stp d14, d15, [sp, #144]\n"
    "    mov x2, sp\n"
    "    str x2, [x0]\n"             /* *from_sp = sp */
    "    mov sp, x1\n"               /* sp = to_sp */
    "    ldp x19, x20, [sp, #0]\n"
    "    ldp x21, x22, [sp, #16]\n"
    "    ldp x23, x24, [sp, #32]\n"
    "    ldp x25, x26, [sp, #48]\n"
    "    ldp x27, x28, [sp, #64]\n"
    "    ldp x29, x30, [sp, #80]\n"
    "    ldp d8, d9,   [sp, #96]\n"
    "    ldp d10, d11, [sp, #112]\n"
    "    ldp d12, d13, [sp, #128]\n"
    "    ldp d14, d15, [sp, #144]\n"
    "    add sp, sp, #160\n"
    "    ret\n"
    ".size tlang_fctx_switch_asm,.-tlang_fctx_switch_asm\n"
);

__asm__(
    ".text\n"
    ".globl tl_asm_bootstrap\n"
    ".type tl_asm_bootstrap,%function\n"
    "tl_asm_bootstrap:\n"
    "    mov x0, x20\n"             /* arg */
    "    mov x29, #0\n"
    "    blr x19\n"                 /* entry(arg) */
    "    brk #0\n"
    ".size tl_asm_bootstrap,.-tl_asm_bootstrap\n"
);

static void* tl_build_frame(void* stack_lo, size_t stack_size,
                            void (*entry)(void* arg), void* arg) {
    uintptr_t top = ((uintptr_t)stack_lo + stack_size) & ~(uintptr_t)15;
    uint64_t* sp = (uint64_t*)top;
    sp -= 20;                                 /* 160-byte frame */
    sp[0] = (uint64_t)(uintptr_t)entry;       /* x19 */
    sp[1] = (uint64_t)(uintptr_t)arg;         /* x20 */
    sp[2] = 0;  sp[3] = 0;                    /* x21,x22 */
    sp[4] = 0;  sp[5] = 0;                    /* x23,x24 */
    sp[6] = 0;  sp[7] = 0;                    /* x25,x26 */
    sp[8] = 0;  sp[9] = 0;                    /* x27,x28 */
    sp[10] = 0;                               /* x29 */
    sp[11] = (uint64_t)(uintptr_t)tl_asm_bootstrap; /* x30 = lr */
    sp[12] = 0; sp[13] = 0;                   /* d8,d9 */
    sp[14] = 0; sp[15] = 0;                   /* d10,d11 */
    sp[16] = 0; sp[17] = 0;                   /* d12,d13 */
    sp[18] = 0; sp[19] = 0;                   /* d14,d15 */
    return sp;
}

#endif  /* arch */

void tlang_fctx_init(FiberContext* ctx, void* stack_lo, size_t stack_size,
                     void (*entry)(void* arg), void* arg) {
    ctx->sp = tl_build_frame(stack_lo, stack_size, entry, arg);
#if TLANG_ASAN
    ctx->asan_fake_stack = NULL;
    ctx->asan_stack_bottom = stack_lo;
    ctx->asan_stack_size = stack_size;
#endif
}

void tlang_fctx_init_thread(FiberContext* ctx) {
    ctx->sp = NULL;
#if TLANG_ASAN
    tl_asan_record_thread(ctx);
#endif
}

void tlang_fctx_switch(FiberContext* from, FiberContext* to) {
    tl_asan_start(from, to, 0);
    tlang_fctx_switch_asm(&from->sp, to->sp);
    tl_asan_finish(from);
}

_Noreturn void tlang_fctx_exit(FiberContext* from, FiberContext* to) {
    tl_asan_start(from, to, 1);
    tlang_fctx_switch_asm(&from->sp, to->sp);
    /* Never resumed. */
    abort();
}

/* ================================================================== */
#else
#error "no context-switch implementation for this architecture"
#endif
