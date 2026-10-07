package codegen

import (
	"strconv"

	"tlang/token"
	"tlang/types"
)

// reservedLocalNames are the fixed names generated code uses inside every C
// function: the hidden fiber parameter and the failure-exit and
// guard-denied labels. They are reserved in each function scope. The
// numbered temporaries and labels (__t<N>, __tlang_catch_<K>, ...) share the
// "__" prefix, which no name the allocator produces (always "l_...") has.
var reservedLocalNames = []string{"__fib", "__fail", "__guard_denied", "__after"}

// funcCtx is the lowering state of one generated C function (codegen design
// §2.2): a plain function or method, a concrete generic instance, or
// tl__init_globals. Statement and expression lowering thread it through;
// they add the handler stack (catch and rollback labels with use tracking),
// the loop stack and the read/write tracking of plan D9-D11.
type funcCtx struct {
	g *generator
	// fn is the emitted function; nil for tl__init_globals.
	fn *types.Func
	// inst is fn when fn is a generic instance, else nil. It drives
	// Info.Concrete and Info.ConcreteFunc (codegen design §5.3).
	inst *types.Func
	// locals names the receiver, the parameters and the locals.
	locals *localNames
	// body is the function body; blk is the innermost open block.
	body, blk *block
	// tmp and lbl are the per-function counters of plan D6, both starting at
	// 1 in emission order: tmp numbers __t<N> and the other generated locals
	// (__jp<N>, __je<N>, __b<N>, __i<N>), lbl the catch, try-end and continue
	// labels.
	tmp, lbl int
	// result is the concrete result type of the function, used to lower a
	// return and the missing-return fallback. Void for tl__init_globals.
	result types.Type
	// handlers is the handler stack of plan D9, innermost last: the catch
	// and transaction-rollback labels a may-fail check or a throw jumps to.
	// An empty stack means the failure exit __fail. Every resolved jump
	// marks its handler used, which drives whether the label is emitted.
	handlers []*handler
	// failUsed records that some jump resolved to __fail, so the epilogue
	// emits the "__fail: ;" label and the zero return.
	failUsed bool
	// guardDenied records that the function has guards, so the epilogue emits
	// the "__guard_denied: ;" label, the one-liner and the zero return.
	guardDenied bool
	// hasAfter records that the function has @After hooks, so every exit is
	// routed through the single "__after:" convergence label where the hooks
	// fire LIFO before the one real return. When false, the @After machinery
	// is inert and output is byte-for-byte unchanged from a hook-less
	// function.
	hasAfter bool
	// afterRetName is the C name of the hoisted "__after_ret" local that
	// holds a non-void handler's return value across the goto to __after;
	// "" for a void handler or when hasAfter is false.
	afterRetName string
	// loops is the loop stack of plan D11, innermost last: a continue jumps
	// to the innermost loop's continue target (or is a plain C continue).
	loops []*loopCtx
	// read and written track, per local/parameter/receiver *Var, whether the
	// emitted C reads or writes it (plan D10, lookup only). A deferred
	// "(void)<name>;" fragment is emitted for a variable written but never
	// read.
	read, written map[*types.Var]bool
	// ctxArg is the C name of the Context argument a guard and the
	// guard-denied one-liner pass (plan D21): the receiver when it is a
	// Context method, else the first parameter. Set by emitGuards for a
	// guarded function.
	ctxArg string
}

// handler is one entry of the handler stack (plan D9): the C label a may-fail
// check or a throw inside its region jumps to, and whether any jump resolved
// to it. kind distinguishes a catch target from a transaction rollback for
// debugging only; the lowering treats them the same.
type handler struct {
	label string
	used  bool
}

// loopCtx is one entry of the loop stack (plan D11). contLabel is the
// continue target of a loop whose post needs statements (__cont_<K>); when it
// is "" a continue is a plain C continue. contUsed records that some continue
// jumped to contLabel, so the label is emitted.
type loopCtx struct {
	contLabel string
	contUsed  bool
}

// cval is a lowered expression value (plan D7): the C text, whether the value
// is stable (cannot change if more code runs before it is used, so it need
// not be spilled) and its precedence, so an operand is parenthesized only
// when its context needs it (plan D2, §13 rule 15).
type cval struct {
	code   string
	stable bool
	prec   prec
}

// prec is the C precedence class of a cval, used to decide parenthesization
// (plan D2). precAtom is a primary expression (identifier, literal, call,
// parenthesized or compound-literal form) that never needs parentheses;
// precOp is a binary, ternary or unary expression that is parenthesized when
// it becomes an operand of another operator.
type prec int

const (
	precOp   prec = iota // a composed expression: parenthesize as an operand
	precAtom             // a primary expression: never parenthesized
)

// atom returns a stable primary-expression cval with the given text.
func atom(code string) cval { return cval{code: code, stable: true, prec: precAtom} }

// opVal returns a cval for a composed expression (binary, unary, ternary);
// stable says whether it is stable.
func opVal(code string, stable bool) cval { return cval{code: code, stable: stable, prec: precOp} }

// operand returns v's text parenthesized when v is a composed expression, so
// it can be used as an operand of a C operator without changing the parse.
func (v cval) operand() string {
	if v.prec == precAtom {
		return v.code
	}
	return "(" + v.code + ")"
}

// newFuncCtx returns the lowering state of fn (nil for tl__init_globals),
// with reservedLocalNames reserved and the receiver and parameters named in
// the function scope, in order. For a generic instance each origin
// parameter *types.Var is aliased to its instance parameter, because the
// body, shared by all instances, refers to the origin's parameters (codegen
// design §5.2 step 1).
func (g *generator) newFuncCtx(fn *types.Func) *funcCtx {
	fc := &funcCtx{
		g:       g,
		fn:      fn,
		locals:  newLocalNames(),
		body:    newBlock(),
		read:    map[*types.Var]bool{},
		written: map[*types.Var]bool{},
		result:  types.Typ[types.Void],
	}
	fc.blk = fc.body
	for _, name := range reservedLocalNames {
		fc.locals.reserve(name)
	}
	if fn == nil {
		return fc
	}
	if fn.Sig == nil {
		g.fail(internalErr(fn.Pos, "function %s has no signature", fn.FullName()))
	}
	if fn.IsInstance() {
		fc.inst = fn
	}
	fc.result = fc.concrete(fn.Sig.Result)
	vars := fn.Sig.Params
	if r := fn.Sig.Recv; r != nil {
		vars = append([]*types.Var{r}, vars...)
	}
	for _, v := range vars {
		fc.locals.declare(v)
		if v.Origin != nil {
			fc.locals.alias(v.Origin, v)
		}
	}
	return fc
}

// enter opens a nested C block, with its own name scope, as the next content
// of the innermost block, and returns it. The braces around it are the
// caller's lines.
func (fc *funcCtx) enter() *block {
	fc.blk = fc.blk.child()
	fc.locals.push()
	return fc.blk
}

// leave finishes the innermost block, which resolves its deferred fragments
// and releases its names, and returns to the enclosing block.
func (fc *funcCtx) leave() {
	b := fc.blk
	if b.parent == nil {
		fc.g.fail(internalErr(fc.g.cur, "leave without enter"))
	}
	b.finish()
	fc.locals.pop()
	fc.blk = b.parent
}

// declare names the local v in the innermost scope and hoists its
// declaration "<ctype> <name>;", without an initializer, to the top of the
// innermost block, so that a forward goto (a failure exit, a catch or a
// rollback label) never jumps over an initialization (codegen design §5.2
// step 2). It returns the name.
func (fc *funcCtx) declare(v *types.Var, ctype string) string {
	name := fc.locals.declare(v)
	fc.blk.decl(ctype + " " + name + ";")
	return name
}

// concrete returns t as seen in the function being emitted: inside a
// generic instance its type parameters are replaced by the instance's type
// arguments (Info.Concrete, codegen design §5.3); elsewhere t is unchanged.
func (fc *funcCtx) concrete(t types.Type) types.Type {
	return fc.g.info.Concrete(t, fc.inst)
}

// concreteFunc returns the canonical concrete instance of callee, a Call.Func
// recorded at pos inside the function being emitted (Info.ConcreteFunc):
// callee itself outside generic code. No instance is §12 item 9, a checker
// bug.
func (fc *funcCtx) concreteFunc(callee *types.Func, pos token.Position) *types.Func {
	if callee == nil {
		fc.g.fail(internalErr(pos, "call without a callee"))
	}
	f := fc.g.info.ConcreteFunc(callee, fc.inst)
	if f == nil {
		fc.g.fail(errMissingInstance(pos, callee, fc.inst))
	}
	return f
}

// nextTmp advances the temporary counter and returns its value.
func (fc *funcCtx) nextTmp() int {
	fc.tmp++
	return fc.tmp
}

// temp returns the name of a new temporary, __t<N>.
func (fc *funcCtx) temp() string { return "__t" + strconv.Itoa(fc.nextTmp()) }

// nextLabel advances the label counter and returns its value.
func (fc *funcCtx) nextLabel() int {
	fc.lbl++
	return fc.lbl
}

// pushHandler pushes a catch or rollback label onto the handler stack and
// returns it (plan D9). The caller pops it with popHandler once its region
// has been lowered, and reads handler.used to decide whether to emit it.
func (fc *funcCtx) pushHandler(label string) *handler {
	h := &handler{label: label}
	fc.handlers = append(fc.handlers, h)
	return h
}

// popHandler removes the innermost handler.
func (fc *funcCtx) popHandler() {
	fc.handlers = fc.handlers[:len(fc.handlers)-1]
}

// failTarget returns the C label a may-fail check or a throw jumps to from
// the current position (plan D9): the innermost handler, marked used, or the
// function failure exit __fail when the handler stack is empty.
func (fc *funcCtx) failTarget() string {
	if n := len(fc.handlers); n > 0 {
		h := fc.handlers[n-1]
		h.used = true
		return h.label
	}
	fc.failUsed = true
	return "__fail"
}

// check appends the may-fail check of plan D8 to the innermost block: a jump
// to the current failure target when the fiber has an error. Every user call
// and every may-fail builtin is followed by one.
func (fc *funcCtx) check() {
	fc.blk.linef("if (__fib->err) goto %s;", fc.failTarget())
}

// pushLoop pushes a loop with the given continue target (empty when a
// continue is a plain C continue) and returns it.
func (fc *funcCtx) pushLoop(contLabel string) *loopCtx {
	l := &loopCtx{contLabel: contLabel}
	fc.loops = append(fc.loops, l)
	return l
}

// popLoop removes the innermost loop.
func (fc *funcCtx) popLoop() {
	fc.loops = fc.loops[:len(fc.loops)-1]
}

// markRead records that the emitted C reads the local, parameter or receiver
// v, so no "(void)v;" is emitted for it (plan D10).
func (fc *funcCtx) markRead(v *types.Var) { fc.read[v] = true }

// markWritten records that the emitted C writes the local, parameter or
// receiver v (plan D10).
func (fc *funcCtx) markWritten(v *types.Var) { fc.written[v] = true }
