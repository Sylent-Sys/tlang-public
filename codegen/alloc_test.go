package codegen

import (
	"strings"
	"testing"
)

// TestNewInline checks that a new with no constructed field is an inline
// allocation (plan D23): new T() of a leaf interface, new T[](), and new
// Error all stay inline (no field-store sequence).
func TestNewInline(t *testing.T) {
	out := mustEmit(t, `interface C {
    x: int64;
}

fn f(): void {
    let c = new C();
    let cs = new C[]();
    let e = new Error("m");
    let e2 = new Error("m", 404);
}
`)
	for _, want := range []string{
		"l_c = (tl_C*)tlang_alloc_zeroed(__fib, sizeof(tl_C));",
		"l_cs = TLANG_SLICE_NEW(__fib, C);",
		`l_e = tlang_error_make(TLANG_STR("m"), 500);`,
		`l_e2 = tlang_error_make(TLANG_STR("m"), 404);`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestNewConstructedFields checks COVERAGE-NIT-1/2 (plan D23): a non-optional
// *Named field is a nested new in its own temporary, a non-optional array
// field is one empty slice (no element recursion), and optional fields are
// left zero.
func TestNewConstructedFields(t *testing.T) {
	out := mustEmit(t, `interface C {
    x: int64;
}

interface B {
    c: C;
    tags: string[];
    maybeC: C | null;
    maybeTags: string[] | null;
}

fn f(): void {
    let b = new B();
}
`)
	for _, want := range []string{
		"(tl_B*)tlang_alloc_zeroed(__fib, sizeof(tl_B));",
		"->f_c = (tl_C*)tlang_alloc_zeroed(__fib, sizeof(tl_C));",
		"->f_tags = TLANG_SLICE_NEW(__fib, str);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Optional fields are not constructed.
	if strings.Contains(out, "f_maybeC =") || strings.Contains(out, "f_maybeTags =") {
		t.Errorf("optional fields should not be constructed:\n%s", out)
	}
}

// TestNewGlobalAllocators checks plan D17: a new stored into a global
// initializer uses the global-heap allocators.
func TestNewGlobalAllocators(t *testing.T) {
	out := mustEmit(t, `interface C {
    x: int64;
}

interface B {
    c: C;
    tags: string[];
}

let shared: B = new global B();
let list: C[] = new global C[]();

fn main(): void {
}
`)
	for _, want := range []string{
		"(tl_B*)tlang_alloc_global_zeroed(__fib, sizeof(tl_B));",
		"->f_c = (tl_C*)tlang_alloc_global_zeroed(__fib, sizeof(tl_C));",
		"->f_tags = TLANG_SLICE_NEW_GLOBAL(__fib, str);",
		"g_list = TLANG_SLICE_NEW_GLOBAL(__fib, C);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestArrayLiteral checks the array-literal lowering (plan D17, D26): a fresh
// slice, a reserve of the element count, then one push per element, and the
// global allocator for a global let initializer.
func TestArrayLiteral(t *testing.T) {
	out := mustEmit(t, `let names: string[] = ["a", "b"];

fn f(): void {
    let xs = [1, 2, 3];
}

fn main(): void {
}
`)
	for _, want := range []string{
		"TLANG_SLICE_NEW(__fib, i64);",
		"TLANG_SLICE_RESERVE(__fib, __t1, 3);",
		"TLANG_SLICE_PUSH(__fib, __t1, 1);",
		"TLANG_SLICE_NEW_GLOBAL(__fib, str);", // global let initializer
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestObjectLiteral checks the object-literal lowering (plan D23, D7): new T()
// into a temporary then a field store per entry in source order, each value
// with its widening.
func TestObjectLiteral(t *testing.T) {
	out := mustEmit(t, `interface User {
    id: int64;
    name: string;
}

fn f(): void {
    let u: User = { name: "a", id: 1 };
}
`)
	for _, want := range []string{
		"(tl_User*)tlang_alloc_zeroed(__fib, sizeof(tl_User));",
		`->f_name = TLANG_STR("a");`,
		"->f_id = 1;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestMemberIndexAssign checks the four-step member/index store sequencing of
// plan D16: a side-effecting receiver is spilled, an index store is checked,
// and a may-fail compound index store reuses the location.
func TestMemberIndexAssign(t *testing.T) {
	out := mustEmit(t, `interface Node {
    n: int64;
}

fn obj(nd: Node): Node {
    return nd;
}

fn f(nd: Node, xs: int64[], d: int64): void {
    xs[0] = 1;
    nd.n = 5;
    obj(nd).n = 7;
    xs[d] %= d;
}
`)
	for _, want := range []string{
		"TLANG_SLICE_AT(__fib, l_xs, 0, int64_t) = 1;",
		"l_nd->f_n = 5;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The call receiver is spilled once and the store uses the temporary.
	if !strings.Contains(out, "tl_Node* __t1 = tl_f_obj(__fib, l_nd);") || !strings.Contains(out, "__t1->f_n = 7;") {
		t.Errorf("member store should spill a call receiver:\n%s", out)
	}
	// The may-fail compound index store reuses the location for read and write.
	if !strings.Contains(out, "tlang_mod_i64(__fib, TLANG_SLICE_AT(__fib, l_xs, l_d, int64_t), l_d)") {
		t.Errorf("compound index store should reuse the location:\n%s", out)
	}
}

// TestErrorMemberStore checks the Error member stores of plan D16 and the P5
// dead-store (void): a plain store into a spilled Error temporary is marked
// (void), a compound one is not.
func TestErrorMemberStore(t *testing.T) {
	out := mustEmit(t, `fn mkErr(): Error {
    return new Error("e", 400);
}

fn f(e: Error): void {
    e.status = 404;
    e.message += "!";
    mkErr().message = "discarded";
    mkErr().status += 1;
}
`)
	for _, want := range []string{
		"l_e.status = 404;",
		"l_e.message = tlang_str_concat(__fib, l_e.message, TLANG_STR(\"!\"));",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// A plain store into a spilled Error temporary is a dead store (P5).
	if !strings.Contains(out, `__t1.message = TLANG_STR("discarded");`) || !strings.Contains(out, "(void)__t1;") {
		t.Errorf("plain Error temp store should get (void) (P5):\n%s", out)
	}
	// A compound store reads the field, so no (void).
	if !strings.Contains(out, "__t2.status = tlang_add_i32(__t2.status, 1);") {
		t.Errorf("compound Error temp store should reuse the field:\n%s", out)
	}
	if strings.Contains(out, "(void)__t2;") {
		t.Errorf("compound Error temp store should not get (void):\n%s", out)
	}
}

// TestForOf checks the for-of lowering of plan D11: a counted C for over the
// slice pointer, a loop variable read as the first body statement, and a
// field-path iterable spilled once before the loop.
func TestForOf(t *testing.T) {
	out := mustEmit(t, `interface Bag {
    items: int64[];
}

fn f(xs: int64[], b: Bag): int64 {
    let total: int64 = 0;
    for (const x of xs) {
        total += x;
    }
    for (const y of b.items) {
        total += y;
    }
    return total;
}
`)
	// A plain local iterable is used directly.
	if !strings.Contains(out, "for (int64_t __i1 = 0; __i1 < l_xs->len; __i1++) {") {
		t.Errorf("local iterable should be used directly:\n%s", out)
	}
	if !strings.Contains(out, "l_x = l_xs->items[__i1];") {
		t.Errorf("loop variable should read the current element:\n%s", out)
	}
	// A field-path iterable is spilled once before the loop.
	if !strings.Contains(out, "tlang_slice_i64* __t2 = l_b->f_items;") {
		t.Errorf("field-path iterable should be spilled:\n%s", out)
	}
}

// TestGlobalRoot checks the plan D17 global-root predicate: an array literal
// in a global let initializer uses the global allocator, while a request-
// local initializer uses the request allocator.
func TestGlobalRoot(t *testing.T) {
	out := mustEmit(t, `let g: string[] = ["a", "b"];

fn f(): void {
    let local = [1];
}

fn main(): void {
}
`)
	// The global initializer uses the global allocator.
	if !strings.Contains(out, "__fib->globals->g_g = ") {
		t.Errorf("global initializer should store into the global:\n%s", out)
	}
	if !strings.Contains(out, "TLANG_SLICE_NEW_GLOBAL(__fib, str)") {
		t.Errorf("global initializer array literal should use the global allocator:\n%s", out)
	}
	// A request-local initializer uses the request allocator.
	if !strings.Contains(out, "TLANG_SLICE_NEW(__fib, i64)") {
		t.Errorf("local initializer array literal should use the request allocator:\n%s", out)
	}
}
