package codegen

import (
	"strings"
	"testing"
)

func TestCwriterLines(t *testing.T) {
	var w cwriter
	w.line("int64_t tl_f_f(tlang_fiber* __fib);")
	w.gap()
	w.gap() // a second gap adds nothing
	w.open("int64_t tl_f_f(tlang_fiber* __fib)")
	w.linef("int64_t %s;", "l_x")
	w.open("if (l_x == 0)")
	w.line("goto __fail;")
	w.reopen("} else {")
	w.line("l_x = 1;")
	w.close("")
	w.line("return l_x;")
	w.label("__fail")
	w.line("")
	w.line("return 0;")
	w.close("")
	w.writef("%s\n", "/* raw */")
	want := strings.Join([]string{
		"int64_t tl_f_f(tlang_fiber* __fib);",
		"",
		"int64_t tl_f_f(tlang_fiber* __fib) {",
		"    int64_t l_x;",
		"    if (l_x == 0) {",
		"        goto __fail;",
		"    } else {",
		"        l_x = 1;",
		"    }",
		"    return l_x;",
		"    __fail: ;",
		"",
		"    return 0;",
		"}",
		"/* raw */",
		"",
	}, "\n")
	if got := string(w.bytes()); got != want {
		t.Fatalf("writer output:\n%s\nwant:\n%s", got, want)
	}
}

func TestCwriterGap(t *testing.T) {
	var w cwriter
	w.gap() // an empty section gets no leading empty line
	if len(w.bytes()) != 0 {
		t.Fatalf("gap on an empty section wrote %q", w.bytes())
	}
	w.open("struct tl_A")
	w.line("int64_t f_x;")
	w.close(";")
	w.gap()
	w.open("struct tl_B")
	w.line("char __empty;")
	w.close(";")
	want := "struct tl_A {\n    int64_t f_x;\n};\n\nstruct tl_B {\n    char __empty;\n};\n"
	if got := string(w.bytes()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	var e cwriter
	e.open("")
	e.close("")
	if got := string(e.bytes()); got != "{\n}\n" {
		t.Fatalf("empty head: got %q", got)
	}
}

func TestCwriterMisuse(t *testing.T) {
	for _, bad := range []string{"a\nb", " x", "x ", "\tx", "x\r"} {
		mustPanic(t, "malformed C line", func() {
			var w cwriter
			w.line(bad)
		})
	}
	mustPanic(t, "close without open", func() {
		var w cwriter
		w.close("")
	})
	mustPanic(t, "reopen without open", func() {
		var w cwriter
		w.reopen("} else {")
	})
	mustPanic(t, "before it was finished", func() {
		var w cwriter
		w.writeBlock(newBlock())
	})
}

func TestBlock(t *testing.T) {
	body := newBlock()
	body.decl("int64_t l_a;")
	readA := false
	body.deferred(func() string {
		if readA {
			return ""
		}
		return "(void)l_a;"
	})
	body.line("l_a = 1;")
	body.line("if (l_c) {")
	inner := body.child()
	inner.decl("int64_t l_a__2;")
	inner.line("l_a__2 = 2;")
	readInner := true
	inner.deferred(func() string {
		if readInner {
			return ""
		}
		return "(void)l_a__2;"
	})
	inner.open("for (;;)")
	inner.line("break;")
	inner.close("")
	inner.finish()
	body.line("}")
	body.label("__fail")
	body.linef("return %d;", 0)
	body.finish()

	var w cwriter
	w.open("int64_t tl_f_g(tlang_fiber* __fib, bool l_c)")
	w.writeBlock(body)
	w.close("")
	want := strings.Join([]string{
		"int64_t tl_f_g(tlang_fiber* __fib, bool l_c) {",
		"    int64_t l_a;",
		"    (void)l_a;",
		"    l_a = 1;",
		"    if (l_c) {",
		"        int64_t l_a__2;",
		"        l_a__2 = 2;",
		"        for (;;) {",
		"            break;",
		"        }",
		"    }",
		"    __fail: ;",
		"    return 0;",
		"}",
		"",
	}, "\n")
	if got := string(w.bytes()); got != want {
		t.Fatalf("block output:\n%s\nwant:\n%s", got, want)
	}
	if inner.parent != body || body.parent != nil {
		t.Fatalf("child blocks must link to their parent")
	}
}

// TestBlockDeferredBothWays checks that a deferred fragment is decided when
// its block is finished, from state that lowering may still change before
// that.
func TestBlockDeferredBothWays(t *testing.T) {
	for _, read := range []bool{false, true} {
		b := newBlock()
		used := false
		b.line("l_x = 5;")
		b.deferred(func() string {
			if used {
				return ""
			}
			return "(void)l_x;"
		})
		b.line("l_y = 1;")
		used = read // a later read is still taken into account
		b.finish()
		var w cwriter
		w.writeBlock(b)
		want := "l_x = 5;\n(void)l_x;\nl_y = 1;\n"
		if read {
			want = "l_x = 5;\nl_y = 1;\n"
		}
		if got := string(w.bytes()); got != want {
			t.Errorf("read=%v: got %q, want %q", read, got, want)
		}
	}
}

func TestBlockMisuse(t *testing.T) {
	mustPanic(t, "content added after finish", func() {
		b := newBlock()
		b.finish()
		b.line("x;")
	})
	mustPanic(t, "declaration added after finish", func() {
		b := newBlock()
		b.finish()
		b.decl("int64_t l_x;")
	})
	mustPanic(t, "finished twice", func() {
		b := newBlock()
		b.finish()
		b.finish()
	})
	mustPanic(t, "before a nested block", func() {
		b := newBlock()
		b.child()
		b.finish()
	})
	mustPanic(t, "unclosed open", func() {
		b := newBlock()
		b.open("while (true)")
		b.finish()
	})
	mustPanic(t, "close without open", func() {
		newBlock().close("")
	})
	mustPanic(t, "malformed C line", func() {
		b := newBlock()
		b.deferred(func() string { return "x\n" })
		b.finish()
	})
}

func TestAssemble(t *testing.T) {
	g := testGenerator()
	g.section(secInclude).line(`#include "tlang.h"`)
	g.section(secTypes).line("typedef struct tl_A tl_A;")
	g.section(secProgram).line("int main(void) { return 0; }")
	want := "#include \"tlang.h\"\n\ntypedef struct tl_A tl_A;\n\nint main(void) { return 0; }\n"
	if got := string(g.assemble()); got != want {
		t.Fatalf("assemble:\n%q\nwant:\n%q", got, want)
	}
	if got := testGenerator().assemble(); len(got) != 0 {
		t.Fatalf("assemble of no sections = %q, want empty", got)
	}

	// A section that ends with an empty line (or without a line end) is an
	// internal error, not a stray empty line in the output.
	for _, text := range []string{"x\n\n", "x"} {
		g := testGenerator()
		g.section(secFuncs).writef("%s", text)
		err := catch(g, func() { g.assemble() })
		if err == nil || !strings.Contains(err.Error(), "does not end with exactly one line feed") {
			t.Errorf("section %q: got %v", text, err)
		}
	}
}
