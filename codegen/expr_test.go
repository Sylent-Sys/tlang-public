package codegen

import (
	"strings"
	"testing"
)

// TestNeedsStmtsAndStable exercises the plan D7 prediction and the operand
// rule through observable output: a pure expression produces no spill, a
// may-fail operand forces earlier unstable operands into temporaries, and a
// nonzero-constant divisor stays inline while a variable divisor is checked.
func TestNeedsStmtsAndStable(t *testing.T) {
	// A nonzero-constant divisor is pure (no temporary, no check); a variable
	// divisor is a may-fail temporary.
	out := mustEmit(t, `fn f(a: int64, b: int64): int64 {
    let x = a / 7;
    let y = a / b;
    return x + y;
}
`)
	if !strings.Contains(out, "l_x = tlang_div_i64(__fib, l_a, 7);") {
		t.Errorf("constant divisor should be inline and unchecked:\n%s", out)
	}
	if !strings.Contains(out, "int64_t __t1 = tlang_div_i64(__fib, l_a, l_b);\n    if (__fib->err) goto __fail;") {
		t.Errorf("variable divisor should be a checked temporary:\n%s", out)
	}
}

// TestOperandSpill checks that a global read before a may-fail call is
// spilled into a temporary, so the pre-call value is read (plan D7, P12).
func TestOperandSpill(t *testing.T) {
	out := mustEmit(t, `let g: int64 = 0;

fn call(n: int64): int64 {
    return n + 1;
}

fn use(): int64 {
    return g + call(g);
}

fn main(): void {
}
`)
	// g is read into a temp, then call(g) runs, then the add uses the temp.
	if !strings.Contains(out, "int64_t __t1 = __fib->globals->g_g;") {
		t.Errorf("global read should be spilled before the call:\n%s", out)
	}
}

// TestFoldingAndWrap checks constant folding (plan D13): an all-representable
// expression folds, a negated min literal folds to INT64_MIN, and an
// overflowing operation re-emits its operands through the wrapping helper.
func TestFoldingAndWrap(t *testing.T) {
	out := mustEmit(t, `fn f(): int64 {
    let folded = 2 * 3 + 4;
    let minv = -9223372036854775808;
    let wrapped = 9223372036854775807 + 1;
    return folded + minv + wrapped;
}
`)
	for _, want := range []string{
		"l_folded = 10;",
		"l_minv = INT64_MIN;",
		"l_wrapped = tlang_add_i64(9223372036854775807, 1);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestNullAndOptional checks the null forms and the ConvToOptional widening
// of plan D28 through return values of every representation.
func TestNullAndOptional(t *testing.T) {
	out := mustEmit(t, `interface User {
    id: int64;
}

fn a(x: int64, c: bool): int64 | null {
    if (c) {
        return null;
    }
    return x;
}

fn b(s: string, c: bool): string | null {
    if (c) {
        return null;
    }
    return s;
}

fn u(x: User, c: bool): User | null {
    if (c) {
        return null;
    }
    return x;
}

fn main(): void {
}
`)
	for _, want := range []string{
		"return TLANG_NONE(i64);",
		"return TLANG_SOME(i64, l_x);",
		"return TLANG_STR_NULL;",
		"return tlang_str_some(l_s);",
		"return NULL;",
		"return l_x;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestSelfForms checks the §8.5 self-compare cast and the plan D25 self-assign
// forms: a scalar self-assign re-casts, a struct self-assign is a (void).
func TestSelfForms(t *testing.T) {
	out := mustEmit(t, `fn f(x: int64, s: string): string {
    let eq = x == x;
    x = x;
    s = s;
    return s;
}
`)
	for _, want := range []string{
		"l_eq = (int64_t)l_x == l_x;",
		"l_x = (int64_t)l_x;",
		"(void)l_s;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
