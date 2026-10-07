package codegen

import (
	"go/constant"
	"strconv"
	"strings"

	"tlang/ast"
	"tlang/types"
)

// DB and transaction calls and row descriptors (codegen design §10.3, plan
// D18, D19). db.execute/query/queryOne run in autocommit (the tlang_tx*
// argument is the literal NULL); tx.execute/query/queryOne use the handle
// local l_tx. The SQL is a compile-time constant string (NUL-checked); the
// parameters are a sized tlang_pg_param compound literal (or NULL, 0) built by
// the per-argument kind rule, with side-effecting arguments spilled
// left-to-right ahead of the literal. query/queryOne register their row type
// so region D emits its descriptor, which emitRowDescriptors writes.

// dbExecute lowers db.execute(sql, args…):
// tlang_db_execute(__fib, NULL, <sql>, <params>, K), may-fail (codegen design
// §10.3). The result is int64, materialized and checked unless discarded.
func (fc *funcCtx) dbExecute(e *ast.CallExpression, c *types.Call, discard bool) cval {
	sql, params, n := fc.sqlAndParams(e)
	code := "tlang_db_execute(__fib, NULL, " + sql + ", " + params + ", " + strconv.Itoa(n) + ")"
	return fc.mayfailResult(code, c.Builtin.Info().Result, discard)
}

// dbQuery lowers db.query<T>(sql, args…) and db.queryOne<T>(sql, args…)
// (codegen design §10.3): the autocommit NULL handle, the sql, the params,
// the count, and &__tl_td_<m> last, cast to the result C type, may-fail. one
// selects queryOne (result tl_<m>*) over query (result tlang_slice_<m>*).
func (fc *funcCtx) dbQuery(e *ast.CallExpression, c *types.Call, one, discard bool) cval {
	fn, cast, m := fc.queryShape(c, one)
	sql, params, n := fc.sqlAndParams(e)
	code := cast + fn + "(__fib, NULL, " + sql + ", " + params + ", " + strconv.Itoa(n) + ", &" + typeDescName(m) + ")"
	return fc.mayfailResult(code, fc.typ(e), discard)
}

// txExecute lowers tx.execute(sql, args…):
// tlang_tx_exec(__fib, <tx>, <sql>, <params>, K), may-fail; inside a
// transaction body its check targets __tx_rollback_N (codegen design §10.3,
// §7.1).
func (fc *funcCtx) txExecute(e *ast.CallExpression, c *types.Call, discard bool) cval {
	tx := fc.txHandle(c)
	sql, params, n := fc.sqlAndParams(e)
	code := "tlang_tx_exec(__fib, " + tx + ", " + sql + ", " + params + ", " + strconv.Itoa(n) + ")"
	return fc.mayfailResult(code, c.Builtin.Info().Result, discard)
}

// txQuery lowers tx.query<T>(sql, args…) and tx.queryOne<T>(sql, args…)
// (codegen design §10.3): the handle l_tx, the sql, the params, the count and
// &__tl_td_<m> last, cast to the result C type, may-fail.
func (fc *funcCtx) txQuery(e *ast.CallExpression, c *types.Call, one, discard bool) cval {
	fn, cast, m := fc.queryShape(c, one)
	fn = strings.Replace(fn, "tlang_db_query", "tlang_tx_query", 1)
	tx := fc.txHandle(c)
	sql, params, n := fc.sqlAndParams(e)
	code := cast + fn + "(__fib, " + tx + ", " + sql + ", " + params + ", " + strconv.Itoa(n) + ", &" + typeDescName(m) + ")"
	return fc.mayfailResult(code, fc.typ(e), discard)
}

// queryShape returns the runtime function name (the db form), the result
// cast and the row-type mangle for a query/queryOne call, and registers the
// row type for region D (plan D18). The row type is Call.TypeArgs[0], always
// a concrete *Named (checker-enforced), so Mangle and the cast never reach
// the panic surface.
func (fc *funcCtx) queryShape(c *types.Call, one bool) (fn, cast, m string) {
	if len(c.TypeArgs) == 0 {
		fc.g.fail(internalErr(fc.g.cur, "query call has no row type"))
	}
	row := c.TypeArgs[0]
	m = fc.g.mangle(row)
	fc.g.dbUsed[m] = true
	named, ok := row.(*types.Named)
	if !ok {
		fc.g.fail(internalErr(fc.g.cur, "query row type %v is not an interface", row))
	}
	if one {
		// queryOne returns T|null, whose C representation is tl_<m>*
		// (CType(named) already includes the trailing *).
		return "tlang_db_query_one", "(" + fc.g.ctype(named) + ")", m
	}
	return "tlang_db_query", "(" + fc.g.sliceName(row) + "*)", m
}

// txHandle returns the C handle of a tx.* data method: the lowered Call.Recv
// (the tx local, l_tx in the common case). It must be a stable local path.
func (fc *funcCtx) txHandle(c *types.Call) string {
	v := fc.value(c.Recv)
	if !v.stable {
		v = fc.spill(v, fc.typ(c.Recv))
	}
	return v.code
}

// mayfailResult materializes a may-fail db/tx call into a temporary and
// checks it, or discards it as a (void) statement with a check (codegen
// design §10.3, plan D8). result is the concrete result type.
func (fc *funcCtx) mayfailResult(code string, result types.Type, discard bool) cval {
	if discard {
		fc.blk.linef("(void)(%s);", code)
		fc.check()
		return cval{}
	}
	res := fc.spill(opVal(code, false), result)
	fc.check()
	return res
}

// sqlAndParams lowers the SQL argument and the parameter arguments of a db/tx
// call (codegen design §10.3): the SQL is the constant string value of the
// first argument (NUL-checked, long-literal form when needed); the parameters
// are a sized (tlang_pg_param[K]){…} compound literal (NULL with K == 0),
// each wrapped by the per-argument declared kind, with side-effecting
// arguments spilled left-to-right ahead of the literal (plan D7). It returns
// the SQL expression, the params expression and the parameter count K.
func (fc *funcCtx) sqlAndParams(e *ast.CallExpression) (sql, params string, n int) {
	if len(e.Arguments) == 0 {
		fc.g.fail(internalErr(e.Pos(), "db call has no SQL argument"))
	}
	sqlArg := e.Arguments[0]
	tv, ok := fc.g.info.Types[sqlArg]
	if !ok || !tv.IsConstant() || tv.Value == nil {
		fc.g.fail(internalErr(sqlArg.Pos(), "SQL argument is not a constant string"))
	}
	sql = fc.g.sqlExpr(sqlArg.Pos(), constant.StringVal(tv.Value))

	paramArgs := e.Arguments[1:]
	n = len(paramArgs)
	if n == 0 {
		return sql, "NULL", 0
	}
	vals := fc.lowerOperands(paramArgs)
	parts := make([]string, n)
	for i, a := range paramArgs {
		parts[i] = fc.pgWrap(fc.typ(a), vals[i])
	}
	params = "(tlang_pg_param[" + strconv.Itoa(n) + "]){ " + strings.Join(parts, ", ") + " }"
	return sql, params, n
}

// pgWrap wraps a lowered db parameter value in its TLANG_PG_* macro by the
// argument's declared concrete type, keeping the optional representation
// (codegen design §10.3): a non-optional scalar uses TLANG_PG_<kind>, a
// primitive optional TLANG_PG_OPT_<kind>, a string|null TLANG_PG_OPT_STR.
func (fc *funcCtx) pgWrap(t types.Type, v cval) string {
	if types.IsOptional(t) {
		elem := types.NonOptional(t)
		return "TLANG_PG_OPT_" + valKind(elem) + "(" + macroArg(v) + ")"
	}
	return "TLANG_PG_" + valKind(t) + "(" + macroArg(v) + ")"
}

// emitRowDescriptors writes region D's row descriptors: for every
// Info.DBTypes entry whose mangle was referenced by an emitted db/tx query,
// a field-descriptor array (omitted for a zero-field row) and a type
// descriptor (codegen design §10.3, plan D18, D19, COVERAGE-NIT-5). The
// column name is the TLang field name, the type name is n.Name().
func (g *generator) emitRowDescriptors(w *cwriter) {
	for _, n := range g.info.DBTypes {
		m := g.mangle(n)
		if !g.dbUsed[m] {
			continue
		}
		w.gap()
		g.emitRowDescriptor(w, n, m)
	}
}

// emitRowDescriptor writes one row type's field-descriptor array and type
// descriptor (codegen design §10.3, plan D19). A zero-field row has no
// __tl_fd_<m> array; its descriptor uses 0, NULL for nfields, fields (P10).
func (g *generator) emitRowDescriptor(w *cwriter, n *types.Named, m string) {
	fields := n.Fields()
	k := len(fields)
	st := g.structName(n)
	fieldsArg := "NULL"
	if k > 0 {
		fieldsArg = fieldDescName(m)
		w.open("static const tlang_field_desc " + fieldsArg + "[" + strconv.Itoa(k) + "] =")
		for i, f := range fields {
			line := "{ " + g.strInit(f.Name) + ", " + fieldKind(f.Type) + ", offsetof(" + st + ", " + fieldName(f.Name) + ") }"
			if i < k-1 {
				line += ","
			}
			w.line(line)
		}
		w.close(";")
	}
	w.linef("static const tlang_type_desc %s = { %s, sizeof(%s), %d, %s, NULL };",
		typeDescName(m), g.strInit(n.Name()), st, k, fieldsArg)
}

// fieldKind returns the TLANG_KIND_* constant of a DB row field type (codegen
// design §10.3): the base kind for a non-optional scalar, TLANG_KIND_OPT_* for
// an optional. Every DB row field is IsDBScalar (checker-enforced).
func fieldKind(t types.Type) string {
	if types.IsOptional(t) {
		return "TLANG_KIND_OPT_" + valKind(types.NonOptional(t))
	}
	return "TLANG_KIND_" + valKind(t)
}
