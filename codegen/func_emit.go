package codegen

import (
	"strings"

	"tlang/types"
)

// Function emission: signatures, region C prototypes, and the function body
// (codegen design §4.5, §5.1, §5.2, §5.4, §6.4, §3.5, plan D21, D24). The
// signature builder is shared by the prototypes and the definitions, so a
// prototype is always the definition's header text plus ";". The body is
// lowered into fc.body: the hoisted declarations and the parameter (void)
// fragment come first, then the guard prologue, then the statements, then the
// missing-return fallback and the failure and guard-denied epilogue labels.

// signature returns the C header of fn, "<CRet> <FuncCName>(<params>)",
// without the trailing " {" or ";" (codegen design §4.5). The hidden fiber
// parameter __fib comes first, then the receiver (a method), then the
// parameters, each named by its *Var through fc.locals so an instance
// parameter carries the origin parameter's name. A void parameter type is
// §12 item 3 (a value parameter cannot be void).
func (g *generator) signature(fc *funcCtx) string {
	fn := fc.fn
	ret := g.ctype(fn.Sig.Result)
	var b strings.Builder
	b.WriteString(ret)
	b.WriteByte(' ')
	b.WriteString(g.funcName(fn))
	b.WriteString("(tlang_fiber* __fib")
	if r := fn.Sig.Recv; r != nil {
		g.writeParam(&b, fc, r, "receiver")
	}
	for _, p := range fn.Sig.Params {
		g.writeParam(&b, fc, p, "parameter")
	}
	b.WriteByte(')')
	return b.String()
}

// writeParam appends ", <CType> <name>" for one parameter or receiver,
// rejecting a void type as §12 item 3 (what is "parameter x" / "receiver x").
func (g *generator) writeParam(b *strings.Builder, fc *funcCtx, v *types.Var, role string) {
	if types.IsVoid(v.Type) {
		g.fail(errVoidValue(v.Pos, role+" "+v.Name))
	}
	name, ok := fc.locals.name(v)
	if !ok {
		g.fail(internalErr(v.Pos, "parameter %s was not named", v.Name))
	}
	b.WriteString(", ")
	b.WriteString(g.ctype(v.Type))
	b.WriteByte(' ')
	b.WriteString(name)
}

// emitGuards writes the guard prologue of a guarded function (codegen design
// §6.4, plan D21): for each guard of the origin function, a call that jumps to
// __guard_denied when it returns false, with a may-fail check first when the
// guard may fail. The Context argument is the receiver of a Context method or
// else the first parameter. A function with no guards emits nothing.
func (g *generator) emitGuards(fc *funcCtx) {
	fn := fc.fn
	if fn == nil {
		return
	}
	guards := g.originGuards(fn)
	if len(guards) == 0 {
		return
	}
	fc.ctxArg = g.guardCtxArg(fc)
	fc.guardDenied = true
	for _, guard := range guards {
		gname := g.funcName(guard)
		if guard.MayFail {
			t := fc.temp()
			fc.body.linef("bool %s = %s(__fib, %s);", t, gname, fc.ctxArg)
			fc.body.linef("if (__fib->err) goto %s;", fc.failTarget())
			fc.body.linef("if (!%s) goto __guard_denied;", t)
			continue
		}
		fc.body.linef("if (!%s(__fib, %s)) goto __guard_denied;", gname, fc.ctxArg)
	}
}

// originGuards returns the guards of fn: fn.Guards for a plain function or
// method, fn.Origin.Guards for a generic instance (the shared origin carries
// the @Use annotation).
func (g *generator) originGuards(fn *types.Func) []*types.Func {
	if fn.Origin != nil {
		return fn.Origin.Guards
	}
	return fn.Guards
}

// originAfterHooks returns the @After hooks of fn: fn.AfterHooks for a plain
// function or method, fn.Origin.AfterHooks for a generic instance (the shared
// origin carries the @After annotation), consistent with originGuards.
func (g *generator) originAfterHooks(fn *types.Func) []*types.Func {
	if fn.Origin != nil {
		return fn.Origin.AfterHooks
	}
	return fn.AfterHooks
}

// guardCtxArg returns the C name of the Context argument the guards receive
// (plan D21): the receiver when it is a Context method, else the first
// parameter.
func (g *generator) guardCtxArg(fc *funcCtx) string {
	fn := fc.fn
	if r := fn.Sig.Recv; r != nil && types.IsBasic(r.Type, types.Context) {
		name, _ := fc.locals.name(r)
		fc.markRead(r)
		return name
	}
	if len(fn.Sig.Params) > 0 {
		p := fn.Sig.Params[0]
		name, _ := fc.locals.name(p)
		fc.markRead(p)
		return name
	}
	g.fail(internalErr(fn.Pos, "guarded function %s has no Context argument", fn.FullName()))
	return ""
}

// emitProtos writes region C: a prototype for every Info.Funcs entry then
// every Info.FuncInstances entry (codegen design §3.5). Each is the
// function's signature header plus ";". JSON prototypes are added by F6.
func (g *generator) emitProtos() {
	w := g.section(secProtos)
	for _, f := range g.info.Funcs {
		w.line(g.signature(g.newFuncCtx(f)) + ";")
	}
	for _, f := range g.info.FuncInstances {
		w.line(g.signature(g.newFuncCtx(f)) + ";")
	}
	g.emitJSONProtos(w)
}

// emitFuncs writes region F: the definition of every Info.Funcs entry then
// every Info.FuncInstances entry (codegen design §3.1 step 4, §5.2). F2 only
// handles a frame with no statement or a single "return;".
func (g *generator) emitFuncs() {
	w := g.section(secFuncs)
	for _, f := range g.info.Funcs {
		w.gap()
		g.emitFunc(w, f)
	}
	for _, f := range g.info.FuncInstances {
		w.gap()
		g.emitFunc(w, f)
	}
}

// emitFunc writes one function definition: the signature header, the lowered
// body, and the epilogue (codegen design §5.2, §6.4, plan D24).
func (g *generator) emitFunc(w *cwriter, fn *types.Func) {
	fc := g.newFuncCtx(fn)
	head := g.signature(fc)

	// @After setup (this design, §5.2): detect the hooks and hoist the
	// non-void return holder before any statement, so a forward "goto
	// __after" never jumps over an initializer. Must run before stmts so
	// returnStmt sees fc.hasAfter.
	g.setupAfter(fc)

	// Parameter (void) fragment (plan D10, P4): right after the hoisted
	// declarations, a deferred "(void)<name>;" for every parameter or
	// receiver written but never read, resolved when the body closes.
	g.deferParamVoids(fc)

	// Guard prologue (plan D21): emitted before the statements so a guard's
	// may-fail check can target the handler stack.
	g.emitGuards(fc)

	returned := fc.stmts(fn.Decl.Body.Statements)

	// Missing-return fallback (§5.2 step 6, §5.4, plan D24): a function that
	// can fall off its end needs a return. A non-void function always does
	// (it would be -Werror=return-type); a void function needs one only when
	// an epilogue label follows, so the label is not the last statement.
	// Under @After the single real return lives after the __after label, and
	// a fall-through handler routes to it with "goto __after" instead.
	needsEpilogue := fc.failUsed || fc.guardDenied || fc.hasAfter
	if !returned {
		if fc.hasAfter {
			if !types.IsVoid(fc.result) {
				fc.body.linef("%s = %s;", fc.afterRetName, g.zeroValue(fc.result))
			}
			fc.body.line("goto __after;")
		} else if !types.IsVoid(fc.result) {
			fc.body.linef("return %s;", g.zeroValue(fc.result))
		} else if needsEpilogue {
			fc.body.line("return;")
		}
	}

	// Failure exit (plan D9, D24): the target of every unhandled may-fail
	// check, emitted only when used. Under @After it carries the pending
	// error straight to __after (deciding no response here) so the runtime
	// turns the pending error into the real status after dispatch (§5.3).
	if fc.failUsed {
		fc.body.label("__fail")
		if fc.hasAfter {
			fc.body.line("goto __after;")
		} else {
			g.emitEpilogueReturn(fc)
		}
	}

	// Guard-denied exit (plan D21, D24): the §6.4 one-liner and a zero
	// return, emitted only for a guarded function. Under @After the 403
	// one-liner still runs only on this path, then control falls to __after.
	if fc.guardDenied {
		fc.body.label("__guard_denied")
		fc.body.linef("if (!%s->response_sent) tlang_ctx_text(__fib, %s, 403, TLANG_STR(\"Forbidden\"));", fc.ctxArg, fc.ctxArg)
		if fc.hasAfter {
			fc.body.line("goto __after;")
		} else {
			g.emitEpilogueReturn(fc)
		}
	}

	// @After convergence (this design, §5.3-§5.5): every exit meets here; the
	// hooks fire LIFO with per-hook save/swallow/restore, then the one real
	// return runs.
	if fc.hasAfter {
		g.emitAfterHooks(fc)
	}

	fc.body.finish()

	w.open(head)
	w.writeBlock(fc.body)
	w.close("")
}

// setupAfter detects @After hooks on fn and, when present, sets fc.hasAfter
// and hoists the "__after_ret" return holder for a non-void handler (this
// design, §5.2). The holder is declared and zero-initialized at the top of
// the body so the __fail path, which stores nothing before "goto __after",
// returns the correct zero value. A hook-less function leaves fc.hasAfter
// false and emits nothing here, so its output is byte-for-byte unchanged.
func (g *generator) setupAfter(fc *funcCtx) {
	fn := fc.fn
	if fn == nil || len(g.originAfterHooks(fn)) == 0 {
		return
	}
	fc.hasAfter = true
	if !types.IsVoid(fc.result) {
		fc.afterRetName = "__after_ret"
		fc.locals.reserve(fc.afterRetName)
		fc.body.linef("%s %s = %s;", g.ctype(fc.result), fc.afterRetName, g.zeroValue(fc.result))
	}
}

// emitAfterHooks emits the __after convergence label and the hook sequence
// (this design, §5.4-§5.5). The hooks run last-to-first (LIFO), each in its
// own brace scope that saves the fiber error, calls the hook, logs a thrown
// error to stderr via tlang_console_error, and restores the fiber error so a
// hook can neither become the response nor clear a pending error. The single
// real return follows: "return;" for a void handler, "return __after_ret;"
// otherwise. The Context argument is discovered here (not assumed from
// emitGuards) so an @After-only handler names it correctly.
func (g *generator) emitAfterHooks(fc *funcCtx) {
	hooks := g.originAfterHooks(fc.fn)
	ctxArg := g.guardCtxArg(fc)
	fc.body.label("__after")
	for i := len(hooks) - 1; i >= 0; i-- {
		hname := g.funcName(hooks[i])
		fc.body.open("")
		fc.body.line("int __after_err = __fib->err;")
		fc.body.line("tlang_error __after_e = __fib->error;")
		fc.body.linef("%s(__fib, %s);", hname, ctxArg)
		fc.body.line("if (__fib->err) tlang_console_error(__fib, (tlang_value[1]){ TLANG_VAL_STR(__fib->error.message) }, 1);")
		fc.body.line("__fib->err = __after_err;")
		fc.body.line("__fib->error = __after_e;")
		fc.body.close("")
	}
	if types.IsVoid(fc.result) {
		fc.body.line("return;")
		return
	}
	fc.body.linef("return %s;", fc.afterRetName)
}

// emitEpilogueReturn appends the zero return of an epilogue label: "return;"
// for a void function, "return <zero>;" otherwise (codegen design §5.4, plan
// D24, SEMANTIC-NIT-3).
func (g *generator) emitEpilogueReturn(fc *funcCtx) {
	if types.IsVoid(fc.result) {
		fc.body.line("return;")
		return
	}
	fc.body.linef("return %s;", g.zeroValue(fc.result))
}

// deferParamVoids inserts, at the top of the function body, a deferred
// "(void)<name>;" fragment for every parameter and receiver (plan D10, P4),
// resolved to a line only when that variable was written but never read in
// the emitted C (-Wunused-but-set-parameter is not covered by
// -Wno-unused-parameter).
func (g *generator) deferParamVoids(fc *funcCtx) {
	fn := fc.fn
	if fn == nil {
		return
	}
	vars := fn.Sig.Params
	if r := fn.Sig.Recv; r != nil {
		vars = append([]*types.Var{r}, vars...)
	}
	for _, v := range vars {
		name, ok := fc.locals.name(v)
		if !ok {
			continue
		}
		fc.deferVoid(fc.body, v, name)
	}
}
