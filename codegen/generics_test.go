package codegen

import (
	"strings"
	"testing"

	"tlang/types"
)

// TestMissingInstance checks §12 item 9 (plan D1, codegen design §5.3): when
// ConcreteFunc finds no instance of a callee for the type arguments of the
// function being emitted, codegen reports an internal error rather than
// writing a call to a function that was never instantiated. The condition is
// a checker bug, so it is provoked by replacing the instance cache with an
// empty one after checking a program that does instantiate: lowering an
// instance body then calls ConcreteFunc for a callee the empty cache cannot
// resolve.
func TestMissingInstance(t *testing.T) {
	prog, info := checkSrc(t, `fn id<T>(x: T): T {
    return x;
}

fn twice<T>(x: T): T {
    let y = id<T>(x);
    return id(y);
}

fn main(): void {
    let a = twice<int64>(1);
}
`)
	// Keep the instantiated FuncInstances list (so twice<int64> is emitted),
	// but blank the cache ConcreteFunc consults, so the id<int64> call inside
	// twice<int64> resolves to nothing.
	info.Instances = types.NewInstanceCache()

	_, err := Emit(prog, info)
	if err == nil {
		t.Fatal("Emit succeeded, want a missing-instance error")
	}
	if !strings.Contains(err.Error(), "internal error: missing instance of") {
		t.Fatalf("got %q, want a missing-instance internal error", err)
	}
}

// TestInstanceParamNames checks that an instance body uses the instance's
// parameter C names, not the origin's, through the alias of codegen design
// §5.2 step 1: the generic twice<int64> and twice<str> both spell l_x.
func TestInstanceParamNames(t *testing.T) {
	out := mustEmit(t, `fn id<T>(x: T): T {
    return x;
}

fn twice<T>(x: T): T {
    return id(x);
}

fn main(): void {
    let a = twice<int64>(1);
    let b = twice<string>("s");
}
`)
	for _, want := range []string{
		"int64_t tl_f_twice__i64(tlang_fiber* __fib, int64_t l_x) {",
		"tlang_string tl_f_twice__str(tlang_fiber* __fib, tlang_string l_x) {",
		"return __t1;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
