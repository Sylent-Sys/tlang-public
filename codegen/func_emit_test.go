package codegen

import (
	"strings"
	"testing"
)

// TestSignaturesAndFallback checks the signature builder for a function, a
// method, a Context method, an instance, and a primitive-optional result and
// parameter, plus the §5.2 missing-return zero-value fallback and that a
// prototype is the header plus ";".
func TestSignaturesAndFallback(t *testing.T) {
	out := mustEmit(t, `interface User {
    id: int64;
}

fn a(): int64 {
}

fn c(): float64 | null {
}

fn params(n: int32, m: int64 | null, s: string, u: User | null, xs: int64[], ctx: Context, flag: bool | null): void {
}

fn (u: User) twice(): int64 {
}

fn (c: Context) handle(): void {
}

fn v(): void {
    return;
}

fn main(): void {
}
`)
	cases := []struct{ proto, def string }{
		{"int64_t tl_f_a(tlang_fiber* __fib);", "int64_t tl_f_a(tlang_fiber* __fib) {"},
		{"tlang_opt_f64 tl_f_c(tlang_fiber* __fib);", "tlang_opt_f64 tl_f_c(tlang_fiber* __fib) {"},
		{
			"void tl_f_params(tlang_fiber* __fib, int32_t l_n, tlang_opt_i64 l_m, tlang_string l_s, tl_User* l_u, tlang_slice_i64* l_xs, tlang_ctx* l_ctx, tlang_opt_bool l_flag);",
			"void tl_f_params(tlang_fiber* __fib, int32_t l_n, tlang_opt_i64 l_m, tlang_string l_s, tl_User* l_u, tlang_slice_i64* l_xs, tlang_ctx* l_ctx, tlang_opt_bool l_flag) {",
		},
		{"int64_t tl_m_User__twice(tlang_fiber* __fib, tl_User* l_u);", "int64_t tl_m_User__twice(tlang_fiber* __fib, tl_User* l_u) {"},
		{"void tl_m_Context__handle(tlang_fiber* __fib, tlang_ctx* l_c);", "void tl_m_Context__handle(tlang_fiber* __fib, tlang_ctx* l_c) {"},
	}
	for _, c := range cases {
		if !strings.Contains(out, c.proto) {
			t.Errorf("missing prototype %q in:\n%s", c.proto, out)
		}
		if !strings.Contains(out, c.def) {
			t.Errorf("missing definition header %q in:\n%s", c.def, out)
		}
	}

	// Missing-return fallbacks: a non-void function that falls off its end
	// gets the zero value; a void function with a lone return keeps it; a
	// void function with no body gets no return.
	if !strings.Contains(out, "int64_t tl_f_a(tlang_fiber* __fib) {\n    return 0;\n}") {
		t.Errorf("int64 fallback missing in:\n%s", out)
	}
	if !strings.Contains(out, "tlang_opt_f64 tl_f_c(tlang_fiber* __fib) {\n    return (tlang_opt_f64){0};\n}") {
		t.Errorf("optional fallback missing in:\n%s", out)
	}
	if !strings.Contains(out, "void tl_f_v(tlang_fiber* __fib) {\n    return;\n}") {
		t.Errorf("explicit void return missing in:\n%s", out)
	}
	if !strings.Contains(out, "void tl_f_main(tlang_fiber* __fib) {\n}") {
		t.Errorf("empty void function must have no fallback return in:\n%s", out)
	}
}

// TestFuncBodyLowered checks that F3 lowers a simple body: a let with a
// constant initializer becomes a hoisted declaration and an assignment, with
// a deferred (void) because the local is never read.
func TestFuncBodyLowered(t *testing.T) {
	out := mustEmit(t, "fn main(): void {\n    let x: int64 = 1;\n}\n")
	want := "void tl_f_main(tlang_fiber* __fib) {\n    int64_t l_x;\n    l_x = 1;\n    (void)l_x;\n}"
	if !strings.Contains(out, want) {
		t.Fatalf("body not lowered as expected:\n%s", out)
	}
}

// TestVoidParamRejected checks §12 item 3 for a void parameter and receiver,
// via the signature builder's path.
func TestVoidParamRejected(t *testing.T) {
	prog, info := checkSrc(t, "fn f(x: void): void {\n}\nfn main(): void {\n}\n")
	if got := emitErr(t, prog, info); !strings.Contains(got, "void value in parameter x") {
		t.Fatalf("void parameter: got %s", got)
	}
}
