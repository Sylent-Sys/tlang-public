package codegen

import (
	"strings"
	"testing"
)

// TestShadowingNames checks the scope-aware allocator through lowering (plan
// D5): a nested shadow of x gets l_x__2, two sibling scopes both reuse l_x__2.
func TestShadowingNames(t *testing.T) {
	out := mustEmit(t, `fn f(x: int64, c: bool): int64 {
    let total = x;
    {
        let x = 1;
        total = total + x;
    }
    if (c) {
        let x = 2;
        total = total + x;
    } else {
        let x = 3;
        total = total + x;
    }
    return total;
}
`)
	// The inner declarations shadow the parameter l_x, so they are l_x__2;
	// the two sibling if/else scopes both reuse l_x__2 (released on pop).
	if strings.Count(out, "int64_t l_x__2;") != 3 {
		t.Errorf("expected three l_x__2 declarations (nested + two siblings):\n%s", out)
	}
	if strings.Contains(out, "l_x__3") {
		t.Errorf("sibling scopes must reuse names, not escalate to l_x__3:\n%s", out)
	}
}

// TestVoidCallStatement checks the plan D8 statement forms: a void call is a
// bare statement plus a check; a discarded non-void call casts to void.
func TestVoidCallStatement(t *testing.T) {
	out := mustEmit(t, `fn v(n: int64): void {
}

fn r(n: int64): int64 {
    return n;
}

fn f(n: int64): void {
    v(n);
    r(n);
}
`)
	if !strings.Contains(out, "tl_f_v(__fib, l_n);\n    if (__fib->err) goto __fail;") {
		t.Errorf("void call should be a bare statement + check:\n%s", out)
	}
	if !strings.Contains(out, "(void)tl_f_r(__fib, l_n);\n    if (__fib->err) goto __fail;") {
		t.Errorf("discarded non-void call should cast to void + check:\n%s", out)
	}
}

// TestLoopContinueLabel checks that a continue in a loop with a may-fail post
// becomes a goto to the loop's __cont label, emitted only because a continue
// uses it (plan D11).
func TestLoopContinueLabel(t *testing.T) {
	out := mustEmit(t, `fn step(n: int64): int64 {
    return n + 1;
}

fn f(n: int64): void {
    for (let i = 0; i < n; i = step(i)) {
        if (i == 0) {
            continue;
        }
    }
}
`)
	if !strings.Contains(out, "goto __cont_1;") || !strings.Contains(out, "__cont_1: ;") {
		t.Errorf("may-fail post + continue should use a __cont label:\n%s", out)
	}
}

// TestPlainContinue checks that a continue in a loop whose post is in the
// header is a plain C continue, with no label.
func TestPlainContinue(t *testing.T) {
	out := mustEmit(t, `fn f(n: int64): void {
    for (let i = 0; i < n; i++) {
        if (i == 0) {
            continue;
        }
    }
}
`)
	if !strings.Contains(out, "continue;") {
		t.Errorf("pure post should allow a plain continue:\n%s", out)
	}
	if strings.Contains(out, "__cont_") {
		t.Errorf("pure post should need no continue label:\n%s", out)
	}
}
