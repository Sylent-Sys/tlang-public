package codegen

import (
	"strings"
	"testing"
)

// TestNarrowingPayload checks the payload-extraction guard of plan D28: a
// narrowed read of a primitive optional reads .v, a string/reference optional
// reads the value unchanged.
func TestNarrowingPayload(t *testing.T) {
	out := mustEmit(t, `interface User {
    id: int64;
}

fn f(a: int64 | null, s: string | null, u: User | null): int64 {
    let total: int64 = 0;
    if (a != null) {
        total = total + a;
    }
    if (s != null) {
        total = total + s.len;
    }
    if (u != null) {
        total = total + u.id;
    }
    return total;
}
`)
	for _, want := range []string{
		"l_a.has",                       // present test
		"tlang_add_i64(l_total, l_a.v)", // primitive payload .v
		"(int64_t)l_s.len",              // string value unchanged
		"l_u->f_id",                     // reference value unchanged
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestNonNull checks x! (plan D28): a concrete-optional operand unwraps and
// checks; a result that stays optional (the generic T := U|null case) is a
// no-op with no unwrap and no check.
func TestNonNull(t *testing.T) {
	out := mustEmit(t, `fn f(a: int64 | null, s: string | null): int64 {
    let x = a!;
    let y = s!;
    return x + y.len;
}

fn force<T>(x: T | null): T {
    return x!;
}

fn main(): void {
    let s: string | null = "x";
    let r = force<string | null>(s);
}
`)
	for _, want := range []string{
		"tlang_unwrap_i64(__fib, l_a)",
		"tlang_unwrap_str(__fib, l_s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The generic T := U|null instance returns its operand unchanged.
	if !strings.Contains(out, "tl_f_force__opt_str(tlang_fiber* __fib, tlang_string l_x) {\n    return l_x;") {
		t.Errorf("force<string|null> should be a no-op x!:\n%s", out)
	}
}

// TestNullish checks x ?? y (plan D7, D28): the inline conditional form, the
// if-form for a may-fail fallback, the present test by representation, and the
// generic no-op when the result stays optional.
func TestNullish(t *testing.T) {
	out := mustEmit(t, `fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn f(a: int64 | null, s: string | null, x: int64, y: int64): int64 {
    let p = a ?? 0;
    let q = s ?? "none";
    let r = a ?? div(x, y);
    return p + r;
}

fn orElse<T>(x: T | null, d: T): T {
    return x ?? d;
}

fn main(): void {
    let s: string | null = "x";
    let e: string | null = null;
    let o = orElse<string | null>(s, e);
}
`)
	for _, want := range []string{
		"l_a.has ? l_a.v : 0",                 // primitive inline
		"l_s.data != NULL ? l_s : TLANG_STR(", // string inline present test
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The may-fail fallback uses the if-form.
	if !strings.Contains(out, "if (l_a.has) {") || !strings.Contains(out, "tl_f_div(__fib,") {
		t.Errorf("may-fail ?? should use the if-form:\n%s", out)
	}
	// The generic result-optional instance is a no-op yielding x.
	if !strings.Contains(out, "tl_f_orElse__opt_str(tlang_fiber* __fib, tlang_string l_x, tlang_string l_d) {\n    return l_x;") {
		t.Errorf("orElse<string|null> should be a no-op ??:\n%s", out)
	}
}

// TestBuiltinMemberReads checks the field-like SelBuiltin reads of §8.8: a
// string length casts to int64_t, an array length is ->len, Error fields are
// .message/.status, and a Context field reads through the receiver.
func TestBuiltinMemberReads(t *testing.T) {
	out := mustEmit(t, `fn f(s: string, xs: int64[], e: Error): int64 {
    let a = s.len;
    let b = xs.len;
    let m = e.message;
    let st = e.status;
    return a + b + int64(st);
}
`)
	for _, want := range []string{
		"(int64_t)l_s.len",
		"l_xs->len",
		"l_e.message",
		"l_e.status",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestValueBuiltins checks the §8.8a value-member methods: pure string
// helpers, the fiber-taking clone/toString, the may-fail toInt, and push.
func TestValueBuiltins(t *testing.T) {
	out := mustEmit(t, `fn f(s: string, t: string, xs: int64[], n: int64): int64 {
    let e = s.eq(t);
    let sl = s.slice(1, 3);
    let i = s.indexOf("x");
    let c = s.clone();
    let p = s.toInt();
    let str = n.toString();
    xs.push(n);
    return i + p;
}
`)
	for _, want := range []string{
		"tlang_str_eq(l_s, l_t)",
		"tlang_str_slice(l_s, 1, 3)",
		`tlang_str_index_of(l_s, TLANG_STR("x"))`,
		"tlang_str_clone(__fib, l_s)",
		"tlang_str_to_int(__fib, l_s)",
		"tlang_i64_to_string(__fib, l_n)",
		"TLANG_SLICE_PUSH(__fib, l_xs, l_n);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// toInt is may-fail: a checked temporary.
	if !strings.Contains(out, "tlang_str_to_int(__fib, l_s);\n    if (__fib->err) goto __fail;") {
		t.Errorf("toInt should be a checked temporary:\n%s", out)
	}
}

// TestIndexRead checks SEMANTIC-NIT-2: an index with a side-effecting index
// spills the slice before the index call, so the pre-call slice is read, and
// the read is a checked temporary.
func TestIndexRead(t *testing.T) {
	out := mustEmit(t, `fn pos(i: int64): int64 {
    return i;
}

fn f(xs: int64[]): int64 {
    return xs[pos(1)];
}
`)
	// The slice is spilled before the index call (SEMANTIC-NIT-2).
	if !strings.Contains(out, "tlang_slice_i64* __t1 = l_xs;") {
		t.Errorf("slice should be spilled before a side-effecting index:\n%s", out)
	}
	if !strings.Contains(out, "TLANG_SLICE_AT(__fib, __t1,") {
		t.Errorf("index read should use the spilled slice:\n%s", out)
	}
}
