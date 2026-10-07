package checker

import (
	"testing"

	"tlang/types"
)

// entry_test.go covers pass 6: @Use guard resolution and its E-DECORATOR
// cases, and entry-point selection (main-only, route_dispatcher-only, both,
// neither, wrong signature).

func TestEntryMainOnly(t *testing.T) {
	src := `fn main(): void {}`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if info.Kind != types.ProgramScript || info.Entry == nil || info.Entry.Name != "main" {
		t.Fatalf("want ProgramScript with main entry, got kind %v entry %v", info.Kind, info.Entry)
	}
}

func TestEntryDispatcherOnly(t *testing.T) {
	src := `fn route_dispatcher(ctx: Context): void {}`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if info.Kind != types.ProgramServer || info.Entry == nil || info.Entry.Name != "route_dispatcher" {
		t.Fatalf("want ProgramServer with route_dispatcher entry, got kind %v entry %v", info.Kind, info.Entry)
	}
}

func TestEntryBoth(t *testing.T) {
	src := `
fn main(): void {}
fn route_dispatcher(ctx: Context): void {}
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-ENTRY") {
		t.Fatalf("want E-ENTRY when both main and route_dispatcher are declared, got %s", diags.Error())
	}
}

func TestEntryMainWrongSignature(t *testing.T) {
	src := `fn main(x: int64): void {}`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-ENTRY") {
		t.Fatalf("want E-ENTRY for main with parameters, got %s", diags.Error())
	}
}

func TestEntryDispatcherWrongSignature(t *testing.T) {
	src := `fn route_dispatcher(): void {}`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-ENTRY") {
		t.Fatalf("want E-ENTRY for route_dispatcher with the wrong signature, got %s", diags.Error())
	}
}

func TestUseGuardResolved(t *testing.T) {
	src := `
fn guard(ctx: Context): bool { return true; }
@Use(guard)
fn route_dispatcher(ctx: Context): void {}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if info.Entry == nil || len(info.Entry.Guards) != 1 || info.Entry.Guards[0].Name != "guard" {
		t.Fatalf("want route_dispatcher with one guard, got %+v", info.Entry)
	}
	// Info.Uses records the guard argument identifier.
	foundUse := false
	for _, obj := range info.Uses {
		if f, ok := obj.(*types.Func); ok && f.Name == "guard" {
			foundUse = true
		}
	}
	if !foundUse {
		t.Fatal("the @Use argument should be recorded in Info.Uses")
	}
}

func TestUnknownDecorator(t *testing.T) {
	src := `
@Cache(x)
fn route_dispatcher(ctx: Context): void {}
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-DECORATOR") {
		t.Fatalf("want E-DECORATOR for an unknown decorator, got %s", diags.Error())
	}
}

func TestUseOnNonContext(t *testing.T) {
	// @Use requires a Context receiver or first parameter.
	src := `
fn guard(ctx: Context): bool { return true; }
@Use(guard)
fn helper(n: int64): void {}
fn main(): void {}
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-DECORATOR") {
		t.Fatalf("want E-DECORATOR for @Use on a non-Context function, got %s", diags.Error())
	}
}

func TestUseInvalidGuard(t *testing.T) {
	// A guard argument that is not a (ctx: Context): bool function is
	// E-DECORATOR.
	src := `
fn notaguard(n: int64): int64 { return n; }
@Use(notaguard)
fn route_dispatcher(ctx: Context): void {}
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-DECORATOR") {
		t.Fatalf("want E-DECORATOR for an invalid guard signature, got %s", diags.Error())
	}
}

// ---------------------------------------------------------------------------
// @After hooks (this design)

func TestAfterHookResolved(t *testing.T) {
	src := `
fn hook(ctx: Context): void {}
@After(hook)
fn route_dispatcher(ctx: Context): void {}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if info.Entry == nil || len(info.Entry.AfterHooks) != 1 || info.Entry.AfterHooks[0].Name != "hook" {
		t.Fatalf("want route_dispatcher with one after hook, got %+v", info.Entry)
	}
	// Info.Uses records the hook argument identifier like a guard.
	foundUse := false
	for _, obj := range info.Uses {
		if f, ok := obj.(*types.Func); ok && f.Name == "hook" {
			foundUse = true
		}
	}
	if !foundUse {
		t.Fatal("the @After argument should be recorded in Info.Uses")
	}
}

func TestAfterSplitEqualsGrouped(t *testing.T) {
	// @After(a) @After(b) must resolve to the same ordered hook list as
	// @After(a, b): [a, b] in source order.
	split := `
fn a(ctx: Context): void {}
fn b(ctx: Context): void {}
@After(a) @After(b)
fn route_dispatcher(ctx: Context): void {}
`
	grouped := `
fn a(ctx: Context): void {}
fn b(ctx: Context): void {}
@After(a, b)
fn route_dispatcher(ctx: Context): void {}
`
	_, si, sd := checkProg(t, split)
	wantCodes(t, sd)
	_, gi, gd := checkProg(t, grouped)
	wantCodes(t, gd)
	names := func(hs []*types.Func) []string {
		out := make([]string, len(hs))
		for i, h := range hs {
			out[i] = h.Name
		}
		return out
	}
	sn := names(si.Entry.AfterHooks)
	gn := names(gi.Entry.AfterHooks)
	if len(sn) != 2 || sn[0] != "a" || sn[1] != "b" {
		t.Fatalf("split order = %v, want [a b]", sn)
	}
	if len(gn) != 2 || gn[0] != "a" || gn[1] != "b" {
		t.Fatalf("grouped order = %v, want [a b]", gn)
	}
}

func TestUseAndAfterTogether(t *testing.T) {
	// @Use and @After may both appear on one function; the lists are
	// independent.
	src := `
fn g(ctx: Context): bool { return true; }
fn h(ctx: Context): void {}
@Use(g) @After(h)
fn route_dispatcher(ctx: Context): void {}
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if len(info.Entry.Guards) != 1 || info.Entry.Guards[0].Name != "g" {
		t.Fatalf("want one guard g, got %+v", info.Entry.Guards)
	}
	if len(info.Entry.AfterHooks) != 1 || info.Entry.AfterHooks[0].Name != "h" {
		t.Fatalf("want one after hook h, got %+v", info.Entry.AfterHooks)
	}
}

func TestAfterNonVoidReturn(t *testing.T) {
	// D3: a hook that returns bool (guard-shaped) is not (ctx: Context):
	// void and is rejected.
	src := `
fn notahook(ctx: Context): bool { return true; }
@After(notahook)
fn route_dispatcher(ctx: Context): void {}
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-DECORATOR") {
		t.Fatalf("want E-DECORATOR for a non-void after hook, got %s", diags.Error())
	}
}

func TestAfterWrongParamType(t *testing.T) {
	// D3: a hook whose single parameter is not Context is rejected.
	src := `
fn notahook(n: int64): void {}
@After(notahook)
fn route_dispatcher(ctx: Context): void {}
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-DECORATOR") {
		t.Fatalf("want E-DECORATOR for an after hook with a non-Context parameter, got %s", diags.Error())
	}
}

func TestAfterNoContextParam(t *testing.T) {
	// D3: a hook with no parameters (so no Context) is rejected.
	src := `
fn notahook(): void {}
@After(notahook)
fn route_dispatcher(ctx: Context): void {}
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-DECORATOR") {
		t.Fatalf("want E-DECORATOR for an after hook with no Context parameter, got %s", diags.Error())
	}
}

func TestAfterArgNotFunction(t *testing.T) {
	// D2: an @After argument that names a non-function is rejected.
	src := `
let hook: int64 = 1;
@After(hook)
fn route_dispatcher(ctx: Context): void {}
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-DECORATOR") {
		t.Fatalf("want E-DECORATOR for an @After argument that is not a function, got %s", diags.Error())
	}
}

func TestAfterOnNonContext(t *testing.T) {
	// D1: @After requires a Context receiver or first parameter.
	src := `
fn hook(ctx: Context): void {}
@After(hook)
fn helper(n: int64): void {}
fn main(): void {}
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-DECORATOR") {
		t.Fatalf("want E-DECORATOR for @After on a non-Context function, got %s", diags.Error())
	}
}

func TestAfterHookDoesNotMarkHandlerMayFail(t *testing.T) {
	// A handler decorated with a throwing @After hook must NOT be marked
	// may-fail through the swallowed-hook path (§4.4); the hook function
	// itself is may-fail.
	src := `
fn hook(ctx: Context): void { throw "boom"; }
@After(hook)
fn (ctx: Context) handler(): void {}
fn route_dispatcher(ctx: Context): void { ctx.handler(); }
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	var handler, hook *types.Func
	for _, f := range info.Funcs {
		switch f.Name {
		case "handler":
			handler = f
		case "hook":
			hook = f
		}
	}
	if handler == nil || hook == nil {
		t.Fatalf("missing handler (%v) or hook (%v)", handler, hook)
	}
	if handler.MayFail {
		t.Error("handler should NOT be may-fail through a swallowed @After hook")
	}
	if !hook.MayFail {
		t.Error("the throwing hook function itself should be may-fail")
	}
}
