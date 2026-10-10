package codegen

import (
	"strings"
	"testing"
)

// TestTryCatchBinding checks the catch binding forms (codegen design §6.3,
// plan D9): catch (e) reads tlang_take_error into a named Error local;
// catch {} clears the error; an unread binding gets (void); a discarded try
// body (no may-fail op, throw or transaction) emits no catch label.
func TestTryCatchBinding(t *testing.T) {
	out := mustEmit(t, `fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn f(a: int64, b: int64): int64 {
    let r: int64 = 0;
    try {
        r = div(a, b);
    } catch (e) {
        r = int64(e.status);
    }
    try {
        r = div(a, b);
    } catch {
        r = -1;
    }
    try {
        r = div(a, b);
    } catch (unused) {
        r = 0;
    }
    try {
        r = r + 1;
    } catch (e) {
        r = -2;
    }
    return r;
}
`)
	// catch (e) read: the binding is taken and read, no (void).
	if !strings.Contains(out, "l_e = tlang_take_error(__fib);") {
		t.Error("missing catch binding read")
	}
	// catch {} clears the error, no binding.
	if !strings.Contains(out, "tlang_clear_error(__fib);") {
		t.Error("missing catch {} clear")
	}
	// catch (unused): the binding is never read, so it gets a (void).
	if !strings.Contains(out, "(void)l_unused;") {
		t.Error("missing (void) for an unread catch binding")
	}
	// The try with no may-fail op (r = r + 1) is emitted inline; its catch is
	// discarded, so no catch label 4 and no "r = -2" store appear.
	if strings.Contains(out, "r = -2") {
		t.Error("a discarded catch body was emitted")
	}
}

// TestThrowForms checks the throw lowering (codegen design §7, plan D9): a
// string becomes tlang_throw(500, ...), new Error(m, s) becomes
// tlang_throw(s, m), an Error value becomes tlang_throw_value, each followed
// by a goto to the handler.
func TestThrowForms(t *testing.T) {
	out := mustEmit(t, `fn f(kind: int64, s: string): void {
    if (kind == 0) {
        throw "plain";
    }
    if (kind == 1) {
        throw new Error("one", 418);
    }
    if (kind == 2) {
        let e = new Error("two");
        throw e;
    }
    throw "x" + s;
}
`)
	if !strings.Contains(out, `tlang_throw_typed(__fib, 500, TLANG_STR("plain"), TLANG_STR("internal"), TLANG_STR("internal"));`) {
		t.Error("missing string throw")
	}
	if !strings.Contains(out, `tlang_throw_typed(__fib, 418, TLANG_STR("one"), TLANG_STR("internal"), TLANG_STR("internal"));`) {
		t.Error("missing new Error throw")
	}
	if !strings.Contains(out, "tlang_throw_value(__fib, l_e);") {
		t.Error("missing Error value throw")
	}
}

// TestHandlerNesting checks that a may-fail op inside a nested try targets
// the inner catch, and that a transaction inside a try routes __tx_fail to
// the try's catch label while its body's checks target the rollback label
// (codegen design §6.2, §7.1, plan D9).
func TestHandlerNesting(t *testing.T) {
	out := mustEmit(t, `fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn f(n: int64): void {
    try {
        db.transaction((tx) => {
            tx.execute("UPDATE a SET x = $1", n);
        });
    } catch (e) {
                console.error(e.message, JsonValue.string(e.message));
    }
}
`)
	// The tx body's data method targets the rollback label.
	if !strings.Contains(out, "if (__fib->err) goto __tx_rollback_1;") {
		t.Error("tx data method does not target the rollback label")
	}
	// __tx_fail falls through to the enclosing try's catch label.
	if !strings.Contains(out, "__tx_fail_1: ;\n            goto __tlang_catch_1;") {
		t.Error("__tx_fail does not target the enclosing catch")
	}
	// begin failure jumps straight to __tx_fail.
	if !strings.Contains(out, "if (!tlang_tx_begin(__fib, &__tx_1)) goto __tx_fail_1;") {
		t.Error("missing tx begin")
	}
}
