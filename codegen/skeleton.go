package codegen

import (
	"tlang/ast"
	"tlang/types"
)

// Skeleton: the fixed scaffolding of a translation unit (codegen design §3.2,
// §3.4, §11, plan D2, D3, D27). emitInclude writes the include and the
// guarded diagnostic pragma; emitGlobals writes region B (struct tl_globals);
// emitProgramStruct writes region G (tl__init_globals, __tl_program, main).

// emitInclude writes the include line and the guarded diagnostic pragma that
// opens every unit (codegen design §3.2). The pragma is emitted verbatim and
// unconditionally; it silences the one -Werror tautology family the §8.5
// casting trick cannot reach, and its __clang__/__GNUC__ guard keeps a clang
// option name out of a GCC pragma (which -Werror=pragmas would reject).
func (g *generator) emitInclude() {
	w := g.section(secInclude)
	w.line(`#include "tlang.h"`)

	w = g.section(secPragma)
	w.line("#if defined(__clang__)")
	w.line(`#pragma clang diagnostic ignored "-Wself-assign"`)
	w.line(`#pragma clang diagnostic ignored "-Wtautological-compare"`)
	w.line("#elif defined(__GNUC__)")
	w.line(`#pragma GCC diagnostic ignored "-Wtautological-compare"`)
	w.line("#endif")
}

// emitGlobals writes region B, struct tl_globals, when the program has
// globals (codegen design §3.4): one member per Info.Globals entry in
// declaration order. A nil global type is §12 item 2, a void global type §12
// item 3, rejected before the member is written. With no globals the region
// is omitted entirely (the header leaves the tag incomplete).
func (g *generator) emitGlobals() {
	if len(g.info.Globals) == 0 {
		return
	}
	w := g.section(secGlobals)
	w.open("struct tl_globals")
	for _, v := range g.info.Globals {
		g.checkGlobalType(v)
		w.linef("%s %s;", g.ctype(v.Type), globalName(v))
	}
	w.close(";")
}

// checkGlobalType rejects a global whose type is nil (§12 item 2) or void
// (§12 item 3).
func (g *generator) checkGlobalType(v *types.Var) {
	if v.Type == nil {
		g.fail(errGlobalNoType(v.Pos, v.Name))
	}
	if types.IsVoid(v.Type) {
		g.fail(errVoidValue(v.Pos, "global "+v.Name))
	}
}

// emitInitGlobals writes tl__init_globals into region G when the program has
// globals (codegen design §3.4, plan D27): one assignment per global with an
// initializer, in Info.Globals order. The zeroed globals block already holds
// the correct zero for a global without an initializer, so such a global
// emits no statement. In F2 only a constant initializer (foldable per D13 and
// carrying no widening conversion) is supported; anything else is deferred.
func (g *generator) emitInitGlobals() {
	if len(g.info.Globals) == 0 {
		return
	}
	fc := g.newFuncCtx(nil)
	for _, v := range g.info.Globals {
		let, _ := v.Decl.(*ast.LetStatement)
		if let == nil || let.Value == nil {
			continue
		}
		g.cur = let.Value.Pos()
		val := fc.storeValue(let.Value, true)
		fc.blk.linef("__fib->globals->%s = %s;", globalName(v), val.code)
	}

	// Failure exit (plan D27): a may-fail initializer jumps to __fail, which
	// is a bare "return;" (tl__init_globals is void).
	if fc.failUsed {
		fc.body.label("__fail")
		fc.body.line("return;")
	}
	fc.body.finish()

	w := g.section(secProgram)
	w.gap()
	w.open("static void tl__init_globals(tlang_fiber* __fib, void* __globals)")
	w.writeBlock(fc.body)
	w.close("")
}

// emitProgramStruct writes __tl_program and main into region G (codegen
// design §3.4, §11, plan D2): designated initializers so unlisted members
// are zero, the entry function pointers from Info.Kind/Info.Entry, and
// Info.UsesDB. ProgramUnknown leaves both .dispatcher and .main NULL; codegen
// does not enforce the entry rule (the driver does).
func (g *generator) emitProgramStruct() {
	w := g.section(secProgram)

	initGlobals, globalsSize := "NULL", "0"
	if len(g.info.Globals) > 0 {
		initGlobals = "tl__init_globals"
		globalsSize = "sizeof(struct tl_globals)"
	}

	dispatcher, main := "NULL", "NULL"
	switch g.info.Kind {
	case types.ProgramServer:
		dispatcher = g.funcName(g.info.Entry)
	case types.ProgramScript:
		main = g.funcName(g.info.Entry)
	}

	usesDB := "false"
	if g.info.UsesDB {
		usesDB = "true"
	}

	w.gap()
	w.open("static const tlang_program __tl_program =")
	w.linef(".init_globals = %s,", initGlobals)
	w.linef(".globals_size = %s,", globalsSize)
	w.linef(".dispatcher   = %s,", dispatcher)
	w.linef(".main         = %s,", main)
	w.linef(".uses_db      = %s,", usesDB)
	w.close(";")
	w.line("int main(int argc, char** argv) { return tlang_main(argc, argv, &__tl_program); }")
}

// checkInstanceGuard is the plan D4 step 6 post-emission guard: a concrete
// interface instance created after type collection has no struct body in
// region A, which would be broken C, so it is reported as an internal error
// instead (codegen design §3.1 step 2).
func (g *generator) checkInstanceGuard() {
	all := g.info.Instances.NamedInstances()
	if len(all) == g.instCount {
		return
	}
	seen := make(map[string]bool, len(g.structs))
	for _, n := range g.structs {
		seen[g.mangle(n)] = true
	}
	for _, n := range all[g.instCount:] {
		if n.Origin != nil && types.IsConcrete(n) && !seen[g.mangle(n)] {
			g.fail(internalErr(namedPos(n), "interface instance %s created after type collection", n))
		}
	}
}
