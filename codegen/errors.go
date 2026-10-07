package codegen

import (
	"fmt"
	"strings"

	"tlang/token"
	"tlang/types"
)

// Every error Emit returns starts with one of these prefixes (plan D1):
// prefixUnsupported for an input the parser and checker accept but that has
// no valid C lowering (codegen design §12), prefixInternal for a codegen
// invariant violation (a recovered panic included), and
// prefixNotImplemented for a construct a later implementation stage lowers.
// The per-item phrases built below are matched as substrings by the
// rejection goldens: never reword them.
const (
	prefixUnsupported    = "unsupported input: "
	prefixInternal       = "internal error: "
	prefixNotImplemented = prefixInternal + "not implemented yet: "
)

// The four forms of §12 item 1: a node the parser substituted for source it
// could not parse, or an expression or type the checker recorded as
// Invalid. The checker skips a BadStatement and records a BadExpression or
// BadType as Invalid without a diagnostic, so such a tree reaches Emit when
// only the parser reported errors, or with no error at all: the parser
// builds a BadType for the field type of "interface X { f: ; }" without
// reporting one.
const (
	badStatement  = "bad statement"
	badExpression = "bad expression"
	badType       = "bad type"
	invalidType   = "invalid type"
)

// maxJSONFields is the widest interface a generated JSON parser supports: its
// seen-mask is one uint64_t, so field index 63 uses the top bit and a 65th
// field would need UINT64_C(1) << 64 (codegen design §10.4, §12 item 10).
const maxJSONFields = 64

// cgError is an error returned by Emit: a reason that starts with one of the
// prefixes above, and the position of the construct it is about. Error
// renders "<file>:<line>:<col>: <reason>", leaving out the parts that are
// unknown, so the driver can print it after "codegen: ".
type cgError struct {
	// file is the source file name (ast.Program.File); "" when unknown.
	file string
	// pos is the position of the construct; the zero Position when the error
	// is about the whole file or the position is unknown.
	pos token.Position
	// msg is the reason.
	msg string
}

// Error formats e as "file:line:col: reason", "file: reason" or "reason".
func (e *cgError) Error() string {
	var b strings.Builder
	if e.file != "" {
		b.WriteString(e.file)
		b.WriteString(":")
	}
	if e.pos.IsValid() {
		b.WriteString(e.pos.String())
		b.WriteString(":")
	}
	if b.Len() > 0 {
		b.WriteString(" ")
	}
	b.WriteString(e.msg)
	return b.String()
}

// bailout is the panic payload that carries an error from anywhere inside
// the lowering to Emit, which recovers it (codegen design §1.2). Lowering
// code calls generator.fail instead of returning errors through every layer.
type bailout struct{ err *cgError }

// fail aborts Emit with err. The source file is filled in when err has none.
func (g *generator) fail(err *cgError) {
	if err.file == "" {
		err.file = g.file
	}
	panic(bailout{err})
}

// recovered converts a value recovered from a panic inside Emit into the
// error Emit returns. A bailout carries its own error. Anything else is a
// codegen bug (for example a runtime error, or a panic of a types name
// helper on a type that should never reach it): it becomes an internal error
// at the position of the construct being processed, so one malformed input
// can never crash the driver.
func (g *generator) recovered(r any) *cgError {
	var err *cgError
	switch r := r.(type) {
	case bailout:
		err = r.err
	case *cgError:
		err = r
	}
	if err == nil {
		err = &cgError{pos: g.cur, msg: prefixInternal + fmt.Sprint(r)}
	}
	if err.file == "" {
		err.file = g.file
	}
	return err
}

// unsupported returns a §12 rejection: the parser and the checker accepted
// the construct at pos, but it has no valid C lowering.
func unsupported(pos token.Position, format string, args ...any) *cgError {
	return &cgError{pos: pos, msg: prefixUnsupported + fmt.Sprintf(format, args...)}
}

// internalErr returns a codegen invariant violation at pos.
func internalErr(pos token.Position, format string, args ...any) *cgError {
	return &cgError{pos: pos, msg: prefixInternal + fmt.Sprintf(format, args...)}
}

// notImplemented returns the error for a construct that a later
// implementation stage lowers: Emit never writes placeholder C.
func notImplemented(pos token.Position, what string) *cgError {
	return &cgError{pos: pos, msg: prefixNotImplemented + what}
}

// errBad is §12 item 1; what is badStatement, badExpression, badType or
// invalidType.
func errBad(pos token.Position, what string) *cgError {
	return unsupported(pos, "%s", what)
}

// errGlobalNoType is §12 item 2 for a global: an un-annotated global
// initialized from a later un-annotated global has a nil type.
func errGlobalNoType(pos token.Position, name string) *cgError {
	return unsupported(pos, "global %s has no type", name)
}

// errExprNoType is §12 item 2 for an expression whose recorded type is nil.
func errExprNoType(pos token.Position) *cgError {
	return unsupported(pos, "expression has no type")
}

// errVoidValue is §12 item 3: a binding, temporary, field, parameter or array
// element whose concrete type is void. what names it: "field X.f",
// "parameter x", "global g", "local x", "ternary" or "array of void".
func errVoidValue(pos token.Position, what string) *cgError {
	return unsupported(pos, "void value in %s", what)
}

// errLeavesTx is §12 item 4: a source break or continue (keyword) whose
// nearest enclosing loop is outside the nearest enclosing transaction body.
func errLeavesTx(pos token.Position, keyword string) *cgError {
	return unsupported(pos, "%s leaves a transaction body", keyword)
}

// errTxCall is §12 item 5: db.transaction called with a non-arrow argument,
// an ordinary builtin call with no runtime counterpart.
func errTxCall(pos token.Position) *cgError {
	return unsupported(pos, "db.transaction needs an arrow function argument")
}

// errBuiltinStore is §12 item 6: a store to a builtin field-like member
// other than Error.message and Error.status (xs.len = 5, ctx.path = "/x").
func errBuiltinStore(pos token.Position, id types.BuiltinID) *cgError {
	return unsupported(pos, "cannot assign to builtin member %s", id)
}

// errDupParam is §12 item 7 for two parameters (or the receiver and a
// parameter) of one function with the same name.
func errDupParam(pos token.Position, name string) *cgError {
	return unsupported(pos, "duplicate parameter name %s", name)
}

// errDunderName is §12 item 7 for a parameter or receiver (role) whose name
// contains "__": parameters bypass types.CheckDeclName, and l_a__2 could
// then collide with a shadowing local's suffixed name.
func errDunderName(pos token.Position, role, name string) *cgError {
	return unsupported(pos, "%s name %s contains \"__\"", role, name)
}

// errCollision is §12 item 8 for two user declarations with one C name,
// first and second in source order; it is reported at the second.
func errCollision(cname string, first, second nameDecl) *cgError {
	return unsupported(second.pos, "C name collision: %s (%s at %s and %s at %s)",
		cname, first.what, first.pos, second.what, second.pos)
}

// errReservedName is §12 item 8 for a user declaration whose C name is one
// generated code reserves (tl_globals, tl__init_globals, __tl_program).
func errReservedName(cname string, user nameDecl) *cgError {
	return unsupported(user.pos, "C name collision: %s is reserved for generated code (%s at %s)",
		cname, user.what, user.pos)
}

// errMissingInstance is §12 item 9: Info.ConcreteFunc found no instance of
// callee for the type arguments of inst, a checker bug.
func errMissingInstance(pos token.Position, callee, inst *types.Func) *cgError {
	return internalErr(pos, "missing instance of %s in %s", callee.FullName(), inst.FullName())
}

// errJSONTooWide is §12 item 10: a JSON-demanded interface t with n >
// maxJSONFields fields, reported at its declaration.
func errJSONTooWide(pos token.Position, t types.Type, n int) *cgError {
	return unsupported(pos, "JSON type %s has %d fields; at most %d are supported", t, n, maxJSONFields)
}

// errSQLNul is §12 item 11: the runtime uses a SQL constant as a
// NUL-terminated C string, so an embedded NUL byte would cut the statement.
// generator.sqlExpr applies it.
func errSQLNul(pos token.Position) *cgError {
	return unsupported(pos, "SQL string contains a NUL byte")
}

// checkJSONWidth fails with §12 item 10, at the interface's declaration,
// when the JSON-demanded interface n has more fields than its parser's
// one-word seen-mask supports.
func (g *generator) checkJSONWidth(n *types.Named) {
	if k := len(n.Fields()); k > maxJSONFields {
		g.fail(errJSONTooWide(namedPos(n), n, k))
	}
}
