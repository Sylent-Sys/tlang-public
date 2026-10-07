package codegen

import (
	"strconv"
	"strings"

	"tlang/ast"
	"tlang/types"
)

// Builtin dispatch (codegen design §8.8, §10, plan D8, API sheet (c)).
// callBuiltin lowers a CallBuiltin by its BuiltinID; builtinNeedsStmts makes
// the matching statement-need prediction for the operand rule (plan D7).
// F3 provides the dispatch skeleton and the handler-stack plumbing (the
// may-fail check targets the innermost handler); the value builtins (string
// and number members, push), the console and ctx builtins, and the db and tx
// builtins are lowered by F4, F5 and F6, which replace the notImplemented
// arms below. Conversions (int32/int64/float64) are a CallConversion, not a
// CallBuiltin, and are lowered in expr.go.

// callBuiltin lowers a builtin call to a value (codegen design §8.8). discard
// is set by the statement path for a builtin called for its effect only. In
// F3 no golden reaches a value or statement builtin, so every ID is deferred
// to the feature that owns it; the dispatch and the MayFail gate are in place
// for F4-F6.
func (fc *funcCtx) callBuiltin(e *ast.CallExpression, c *types.Call, discard bool) cval {
	switch c.Builtin {
	case types.BuiltinDBTransaction:
		// Reached only as a CallExpression, which pass 1 rejected (§12 item
		// 5); a TransactionStatement never comes here.
		fc.g.fail(errTxCall(e.Pos()))
	case types.BuiltinArrayPush:
		return fc.pushBuiltin(e, c, discard)
	case types.BuiltinStringEq:
		return fc.strPure2(e, c, "tlang_str_eq", discard)
	case types.BuiltinStringStartsWith:
		return fc.strPure2(e, c, "tlang_str_starts_with", discard)
	case types.BuiltinStringEndsWith:
		return fc.strPure2(e, c, "tlang_str_ends_with", discard)
	case types.BuiltinStringIndexOf:
		return fc.strPure2(e, c, "tlang_str_index_of", discard)
	case types.BuiltinStringSlice:
		return fc.strSlice(e, c, discard)
	case types.BuiltinStringClone:
		return fc.strFib1(e, c, "tlang_str_clone", discard)
	case types.BuiltinStringCloneGlobal:
		return fc.strFib1(e, c, "tlang_str_clone_global", discard)
	case types.BuiltinStringToInt:
		return fc.strToInt(e, c, discard)
	case types.BuiltinInt32ToString:
		return fc.toString(e, c, "tlang_i32_to_string", discard)
	case types.BuiltinInt64ToString:
		return fc.toString(e, c, "tlang_i64_to_string", discard)
	case types.BuiltinFloat64ToString:
		return fc.toString(e, c, "tlang_f64_to_string", discard)
	case types.BuiltinBoolToString:
		return fc.toString(e, c, "tlang_bool_to_string", discard)
	case types.BuiltinConsoleLog:
		return fc.consoleBuiltin(e, c, "tlang_console_log")
	case types.BuiltinConsoleError:
		return fc.consoleBuiltin(e, c, "tlang_console_error")
	case types.BuiltinCtxHeader:
		return fc.ctxPure(e, c, "tlang_ctx_header", false, discard)
	case types.BuiltinCtxQuery:
		return fc.ctxPure(e, c, "tlang_ctx_query", true, discard)
	case types.BuiltinCtxParam:
		return fc.ctxPure(e, c, "tlang_ctx_param", false, discard)
	case types.BuiltinCtxParamInt:
		return fc.ctxParamInt(e, c, discard)
	case types.BuiltinCtxMatch:
		return fc.ctxMatch(e, c, discard)
	case types.BuiltinCtxText:
		return fc.ctxText(e, c)
	case types.BuiltinCtxSetHeader:
		return fc.ctxSetHeader(e, c)
	case types.BuiltinDBExecute:
		return fc.dbExecute(e, c, discard)
	case types.BuiltinDBQuery:
		return fc.dbQuery(e, c, false, discard)
	case types.BuiltinDBQueryOne:
		return fc.dbQuery(e, c, true, discard)
	case types.BuiltinTxExecute:
		return fc.txExecute(e, c, discard)
	case types.BuiltinTxQuery:
		return fc.txQuery(e, c, false, discard)
	case types.BuiltinTxQueryOne:
		return fc.txQuery(e, c, true, discard)
	case types.BuiltinCtxBindJSON:
		return fc.bindJSON(e, c, discard)
	case types.BuiltinCtxJSON:
		return fc.ctxJSON(e, c)
	}
	fc.g.fail(notImplemented(e.Pos(), "builtin "+c.Builtin.String()))
	return cval{}
}

// builtinOperands lowers the receiver and arguments of a value-member builtin
// left-to-right with the operand rule (codegen design §8.8a, plan D7): the
// receiver first, then the arguments; an earlier unstable operand is spilled
// when a later one needs statements. Returns the receiver value and the
// argument values.
func (fc *funcCtx) builtinOperands(e *ast.CallExpression, c *types.Call) (recv cval, args []cval) {
	exprs := make([]ast.Expression, 0, 1+len(e.Arguments))
	exprs = append(exprs, c.Recv)
	exprs = append(exprs, e.Arguments...)
	vals := fc.lowerOperands(exprs)
	return vals[0], vals[1:]
}

// value returns the single result cval of a value-member builtin, or discards
// it as a statement when discard is set (a builtin called for effect only).
func (fc *funcCtx) builtinResult(v cval, discard bool) cval {
	if discard {
		fc.blk.linef("(void)(%s);", v.code)
		return cval{}
	}
	return v
}

// strPure2 lowers a pure two-string builtin that takes no fiber (eq,
// startsWith, endsWith, indexOf): fn(<recv>, <arg>) (codegen design §8.8a).
func (fc *funcCtx) strPure2(e *ast.CallExpression, c *types.Call, fn string, discard bool) cval {
	recv, args := fc.builtinOperands(e, c)
	v := opVal(fn+"("+recv.code+", "+args[0].code+")", recv.stable && args[0].stable).atomize()
	return fc.builtinResult(v, discard)
}

// strSlice lowers s.slice(a, b): tlang_str_slice(<s>, <a>, <b>), pure
// (codegen design §8.8a).
func (fc *funcCtx) strSlice(e *ast.CallExpression, c *types.Call, discard bool) cval {
	recv, args := fc.builtinOperands(e, c)
	stable := recv.stable && args[0].stable && args[1].stable
	v := opVal("tlang_str_slice("+recv.code+", "+args[0].code+", "+args[1].code+")", stable).atomize()
	return fc.builtinResult(v, discard)
}

// strFib1 lowers a pure string builtin that takes the fiber and the receiver
// only (clone, clone_global): fn(__fib, <recv>) (codegen design §8.8a).
func (fc *funcCtx) strFib1(e *ast.CallExpression, c *types.Call, fn string, discard bool) cval {
	recv, _ := fc.builtinOperands(e, c)
	v := atom(fn + "(__fib, " + recv.code + ")")
	return fc.builtinResult(v, discard)
}

// strToInt lowers s.toInt(): tlang_str_to_int(__fib, <s>), may-fail 400, so
// the result is materialized and checked (codegen design §8.8a, plan D8).
func (fc *funcCtx) strToInt(e *ast.CallExpression, c *types.Call, discard bool) cval {
	recv, _ := fc.builtinOperands(e, c)
	code := "tlang_str_to_int(__fib, " + recv.code + ")"
	if discard {
		fc.blk.linef("(void)(%s);", code)
		fc.check()
		return cval{}
	}
	res := fc.spill(opVal(code, false), c.Builtin.Info().Result)
	fc.check()
	return res
}

// toString lowers n.toString() / b.toString(): fn(__fib, <recv>), pure
// (codegen design §8.8a). The variant is chosen by the caller from the
// receiver's concrete type.
func (fc *funcCtx) toString(e *ast.CallExpression, c *types.Call, fn string, discard bool) cval {
	recv, _ := fc.builtinOperands(e, c)
	v := atom(fn + "(__fib, " + recv.code + ")")
	return fc.builtinResult(v, discard)
}

// pushBuiltin lowers xs.push(v) (codegen design §8.8a, plan D26): the
// statement TLANG_SLICE_PUSH(__fib, <xs>, <v>). The slice must be a stable
// side-effect-free path, so an unstable receiver is spilled; a value with a
// top-level comma is parenthesized (D26). push returns void, so it is always
// a statement.
func (fc *funcCtx) pushBuiltin(e *ast.CallExpression, c *types.Call, discard bool) cval {
	recv, args := fc.builtinOperands(e, c)
	if !recv.stable {
		recv = fc.spill(recv, fc.typ(c.Recv))
	}
	fc.blk.linef("TLANG_SLICE_PUSH(__fib, %s, %s);", recv.code, macroArg(args[0]))
	return cval{}
}

// consoleBuiltin lowers console.log(...) / console.error(...) (codegen
// design §10.1, SEMANTIC-NIT-1): a sized tlang_value array of the K
// arguments, each wrapped by its post-narrowing concrete kind, then
// fn(__fib, <array>, K). Zero arguments emit NULL, 0 (never a zero-length
// compound literal). console returns void, so it is always a statement.
// Side-effecting arguments are spilled left-to-right with their err checks
// before the compound literal is built (plan D7, §6.5).
func (fc *funcCtx) consoleBuiltin(e *ast.CallExpression, c *types.Call, fn string) cval {
	n := len(e.Arguments)
	if n == 0 {
		fc.blk.linef("%s(__fib, NULL, 0);", fn)
		return cval{}
	}
	vals := fc.lowerOperands(e.Arguments)
	parts := make([]string, n)
	for i, a := range e.Arguments {
		parts[i] = valWrap(fc.typ(a), vals[i])
	}
	fc.blk.linef("%s(__fib, (tlang_value[%d]){ %s }, %d);", fn, n, strings.Join(parts, ", "), n)
	return cval{}
}

// valWrap wraps a lowered console argument value in its TLANG_VAL_* macro by
// the argument's post-narrowing concrete type (codegen design §10.1). A
// console argument is never optional at the wrap site (the checker reduces an
// optional to its non-optional T first), so the kind is one of the five base
// kinds.
func valWrap(t types.Type, v cval) string {
	macro := "TLANG_VAL_" + valKind(t)
	return macro + "(" + macroArg(v) + ")"
}

// valKind returns the TLANG_VAL_/TLANG_PG_ base-kind suffix of the concrete
// non-optional scalar type t (I32, I64, F64, BOOL, STR).
func valKind(t types.Type) string {
	switch {
	case types.IsBasic(t, types.Int32):
		return "I32"
	case types.IsBasic(t, types.Int64):
		return "I64"
	case types.IsFloat(t):
		return "F64"
	case types.IsBool(t):
		return "BOOL"
	case types.IsString(t):
		return "STR"
	}
	return "I64"
}

// ctxReceiver lowers the Context receiver of a ctx builtin and spills it when
// it needs statements or is unstable before a statement-needing argument
// (codegen design §10.2, plan D7). In the common case the receiver is a plain
// local (l_ctx), used directly.
func (fc *funcCtx) ctxOperands(e *ast.CallExpression, c *types.Call) (recv cval, args []cval) {
	return fc.builtinOperands(e, c)
}

// ctxPure lowers a pure Context builtin: header(n), query(n) and param(n).
// query takes the fiber (it allocates in the request arena) but is not
// may-fail, so no check follows (codegen design §10.2, API sheet (c)). header
// and param take only the Context.
func (fc *funcCtx) ctxPure(e *ast.CallExpression, c *types.Call, fn string, takesFib, discard bool) cval {
	recv, args := fc.ctxOperands(e, c)
	var code string
	if takesFib {
		code = fn + "(__fib, " + recv.code + ", " + args[0].code + ")"
	} else {
		code = fn + "(" + recv.code + ", " + args[0].code + ")"
	}
	return fc.builtinResult(atom(code), discard)
}

// ctxParamInt lowers ctx.paramInt(n): tlang_ctx_param_int(__fib, <ctx>, <n>),
// may-fail 400, so the result is materialized and checked (codegen design
// §10.2, plan D8).
func (fc *funcCtx) ctxParamInt(e *ast.CallExpression, c *types.Call, discard bool) cval {
	recv, args := fc.ctxOperands(e, c)
	code := "tlang_ctx_param_int(__fib, " + recv.code + ", " + args[0].code + ")"
	if discard {
		fc.blk.linef("(void)(%s);", code)
		fc.check()
		return cval{}
	}
	res := fc.spill(opVal(code, false), c.Builtin.Info().Result)
	fc.check()
	return res
}

// ctxText lowers ctx.text(status, body): tlang_ctx_text(__fib, <ctx>, <s>,
// <b>), a void statement, not may-fail (codegen design §10.2).
func (fc *funcCtx) ctxText(e *ast.CallExpression, c *types.Call) cval {
	recv, args := fc.ctxOperands(e, c)
	fc.blk.linef("tlang_ctx_text(__fib, %s, %s, %s);", recv.code, args[0].code, args[1].code)
	return cval{}
}

// ctxSetHeader lowers ctx.setHeader(name, value):
// tlang_ctx_set_header(__fib, <ctx>, <n>, <v>), may-fail, so a check follows
// (codegen design §10.2, plan D8).
func (fc *funcCtx) ctxSetHeader(e *ast.CallExpression, c *types.Call) cval {
	recv, args := fc.ctxOperands(e, c)
	fc.blk.linef("tlang_ctx_set_header(__fib, %s, %s, %s);", recv.code, args[0].code, args[1].code)
	fc.check()
	return cval{}
}

// bindJSON lowers ctx.bindJson(obj) (codegen design §10.4, plan D20): the
// cursor setup over the request body and a bool result that is the parse
// succeeding AND no trailing data. <obj> is the already-allocated tl_<m>*
// passed directly (no &, no allocation); <m> is the concrete *Named type of
// the argument. bindJson is not may-fail, so no err check follows; its bool
// value is the result. It always emits statements (the three-line sequence),
// so it is lowered into the current block and its __tM temporary returned.
func (fc *funcCtx) bindJSON(e *ast.CallExpression, c *types.Call, discard bool) cval {
	recv, args := fc.ctxOperands(e, c)
	obj := args[0]
	m := fc.g.mangle(fc.typ(e.Arguments[0]))
	parse := fc.g.jsonParseName(fc.typ(e.Arguments[0]))
	_ = m

	n := fc.nextTmp()
	jp := "__jp" + strconv.Itoa(n)
	je := "__je" + strconv.Itoa(n)
	tm := fc.temp()
	fc.blk.linef("const char* %s = %s->body.data;", jp, recv.code)
	fc.blk.linef("const char* %s = %s + %s->body.len;", je, jp, recv.code)
	fc.blk.linef("bool %s = %s(__fib, &%s, %s, %s, 0) && json_skip_ws(%s, %s) == %s;",
		tm, parse, jp, je, obj.code, jp, je, je)
	if discard {
		fc.blk.linef("(void)%s;", tm)
		return cval{}
	}
	return atom(tm)
}

// ctxJSON lowers ctx.json(status, v) (codegen design §10.4, plan D20): a
// braced block that builds the JSON into a fiber buffer with the writer
// chosen by the concrete type of v (the scalar writer for a *Named, the
// array variant for an *Array; the checker rejects an optional value, P11),
// then hands it to tlang_ctx_json. json is void and not may-fail, so it is
// always a statement with no check.
func (fc *funcCtx) ctxJSON(e *ast.CallExpression, c *types.Call) cval {
	recv, args := fc.ctxOperands(e, c)
	status := args[0]
	v := args[1]
	writer := fc.g.jsonWriteName(fc.typ(e.Arguments[1]))

	n := fc.nextTmp()
	buf := "__b" + strconv.Itoa(n)
	fc.blk.open("")
	fc.blk.linef("tlang_buf %s;", buf)
	fc.blk.linef("tlang_buf_init(__fib, &%s, 256);", buf)
	fc.blk.linef("%s(__fib, &%s, %s);", writer, buf, v.code)
	fc.blk.linef("tlang_ctx_json(__fib, %s, %s, tlang_buf_string(&%s));", recv.code, status.code, buf)
	fc.blk.close("")
	return cval{}
}

// builtinNeedsStmts predicts whether lowering the builtin call e emits
// statements (plan D7): a may-fail builtin always does (it is materialized
// and checked), as does any builtin with an operand that needs statements.
// This mirrors callBuiltin so the operand rule spills correctly.
func (fc *funcCtx) builtinNeedsStmts(e *ast.CallExpression, c *types.Call) bool {
	if c.Builtin.MayFail() {
		return true
	}
	// bindJson and json are not may-fail but lower to a statement sequence
	// (the cursor setup and the parse-and-trailing-check for bindJson, the
	// buffer block for json), so they always need statements (plan D20).
	if c.Builtin == types.BuiltinCtxBindJSON || c.Builtin == types.BuiltinCtxJSON {
		return true
	}
	if c.Recv != nil && fc.needsStmts(c.Recv) {
		return true
	}
	for _, a := range e.Arguments {
		if fc.needsStmts(a) {
			return true
		}
	}
	return false
}
