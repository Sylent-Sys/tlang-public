package codegen

import (
	"strings"
	"testing"
)

// TestSkeletonInclude checks the include line and the verbatim guarded
// diagnostic pragma lead every unit, in order.
func TestSkeletonInclude(t *testing.T) {
	out := mustEmit(t, "fn main(): void {\n}\n")
	head := strings.Join([]string{
		`#include "tlang.h"`,
		"",
		"#if defined(__clang__)",
		`#pragma clang diagnostic ignored "-Wself-assign"`,
		`#pragma clang diagnostic ignored "-Wtautological-compare"`,
		"#elif defined(__GNUC__)",
		`#pragma GCC diagnostic ignored "-Wtautological-compare"`,
		"#endif",
		"",
	}, "\n")
	if !strings.HasPrefix(out, head) {
		t.Fatalf("unit does not start with the include and pragma:\n%s", out)
	}
}

// TestProgramStruct checks the __tl_program designated initializer and main
// for script, server and unknown programs, with and without globals.
func TestProgramStruct(t *testing.T) {
	script := mustEmit(t, "let limit: int64 = 100;\n\nfn main(): void {\n}\n")
	for _, want := range []string{
		"struct tl_globals {",
		"int64_t g_limit;",
		"static void tl__init_globals(tlang_fiber* __fib, void* __globals) {",
		"__fib->globals->g_limit = 100;",
		".init_globals = tl__init_globals,",
		".globals_size = sizeof(struct tl_globals),",
		".dispatcher   = NULL,",
		".main         = tl_f_main,",
		".uses_db      = false,",
		"int main(int argc, char** argv) { return tlang_main(argc, argv, &__tl_program); }",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script program missing %q in:\n%s", want, script)
		}
	}

	server := mustEmit(t, "fn route_dispatcher(c: Context): void {\n}\n")
	for _, want := range []string{
		".dispatcher   = tl_f_route_dispatcher,",
		".main         = NULL,",
	} {
		if !strings.Contains(server, want) {
			t.Errorf("server program missing %q in:\n%s", want, server)
		}
	}
	// No globals: no struct tl_globals and no tl__init_globals.
	if strings.Contains(server, "struct tl_globals {") || strings.Contains(server, "tl__init_globals") {
		t.Errorf("a program with no globals must omit region B and init_globals:\n%s", server)
	}
	if !strings.Contains(server, ".init_globals = NULL,") || !strings.Contains(server, ".globals_size = 0,") {
		t.Errorf("no-globals program struct must zero init_globals/globals_size:\n%s", server)
	}

	unknown := mustEmit(t, "interface Point {\n    x: int64;\n}\n\nfn helper(p: Point): void {\n}\n")
	if !strings.Contains(unknown, ".dispatcher   = NULL,") || !strings.Contains(unknown, ".main         = NULL,") {
		t.Errorf("ProgramUnknown must leave both entry pointers NULL:\n%s", unknown)
	}
}

// TestGlobalConstInits checks region B/G constant initializers: the int ".0"
// rule, a negative, a folded int32 conversion, escapes, and that an
// uninitialized global emits no assignment.
func TestGlobalConstInits(t *testing.T) {
	out := mustEmit(t, `let counter: int64;
let negative: int64 = -9;
let small: int32 = 7;
let smallNeg: int32 = int32(-5);
let ratio: float64 = 2;
let label: string = "svc\t\"x\"\\";
let enabled: bool = true;

fn main(): void {
}
`)
	for _, want := range []string{
		"__fib->globals->g_negative = -9;",
		"__fib->globals->g_small = 7;",
		"__fib->globals->g_smallNeg = -5;",
		"__fib->globals->g_ratio = 2.0;",
		`__fib->globals->g_label = TLANG_STR("svc\011\"x\"\\");`,
		"__fib->globals->g_enabled = true;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "g_counter =") {
		t.Errorf("an uninitialized global must emit no assignment:\n%s", out)
	}
}
