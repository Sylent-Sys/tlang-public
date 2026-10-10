package checker

import (
	"strings"

	"tlang/ast"
	"tlang/types"
)

// builtincall.go types builtin member and namespace calls (DESIGN.md
// §2.6-§2.11) via BuiltinID.Info(): fixed Params/Result for ordinary ones and
// bespoke checks for the Special ones (console variadic, push, ctx.match
// routes, bindJson/json JSON demand, db/tx SQL checks). It records
// Info.Calls[e] and seeds Info.Routes/DBTypes and the JSON demand set.

// callBuiltin types a builtin member or namespace call.
func (c *checker) callBuiltin(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	id := sel.Builtin
	if id.Info().Special {
		return c.callSpecialBuiltin(sc, e, m, sel, facts)
	}
	return c.callOrdinaryBuiltin(sc, e, m, sel, facts)
}

// callOrdinaryBuiltin types a builtin with fixed Params/Result: arity +
// assignable args (with conversion recording); result the table Result.
func (c *checker) callOrdinaryBuiltin(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	in := sel.Builtin.Info()
	if len(e.Arguments) != len(in.Params) {
		c.errorf(e.Lparen, "E-TYPE", "%s expects %d argument(s), got %d", sel.Builtin, len(in.Params), len(e.Arguments))
	}
	for i, a := range e.Arguments {
		if i < len(in.Params) {
			tv := c.expr(sc, a, facts, in.Params[i])
			c.assign(sc, a, tv, in.Params[i])
		} else {
			c.expr(sc, a, facts, nil)
		}
	}
	result := in.Result
	if result == nil {
		result = types.Typ[types.Void]
	}
	c.recordBuiltinCall(e, m, sel.Builtin, nil)
	return c.record(e, result, nil)
}

// callSpecialBuiltin dispatches the special-cased builtins.
func (c *checker) callSpecialBuiltin(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	switch sel.Builtin {
	case types.BuiltinConsoleInfoFields, types.BuiltinConsoleErrorFields,
		types.BuiltinConsoleDebugFields, types.BuiltinConsoleWarnFields:
		return c.callConsole(sc, e, m, sel, facts)
	case types.BuiltinJsonBool, types.BuiltinJsonNumber,
		types.BuiltinJsonString, types.BuiltinJsonArray, types.BuiltinJsonObject,
		types.BuiltinJsonAsBool, types.BuiltinJsonAsNumber, types.BuiltinJsonAsString,
		types.BuiltinJsonArrayValues, types.BuiltinJsonObjectKeys,
		types.BuiltinJsonObjectValues, types.BuiltinJsonGet:
		return c.callJson(sc, e, m, sel, facts)
	case types.BuiltinArrayPush:
		return c.callPush(sc, e, m, sel, facts)
	case types.BuiltinCtxMatch:
		return c.callMatch(sc, e, m, sel, facts)
	case types.BuiltinCtxBindJSON:
		return c.callBindJSON(sc, e, m, sel, facts)
	case types.BuiltinCtxJSON:
		return c.callJSON(sc, e, m, sel, facts)
	case types.BuiltinDBExecute, types.BuiltinTxExecute:
		return c.callDBExecute(sc, e, m, sel, facts)
	case types.BuiltinDBQuery, types.BuiltinTxQuery:
		return c.callDBQuery(sc, e, m, sel, facts, false)
	case types.BuiltinDBQueryOne, types.BuiltinTxQueryOne:
		return c.callDBQuery(sc, e, m, sel, facts, true)
	}
	// Should not happen; recover.
	c.checkArgsLoose(sc, e, facts)
	c.recordBuiltinCall(e, m, sel.Builtin, nil)
	return c.record(e, types.Typ[types.Void], nil)
}

// callConsole types console.log/error: any number of string, number or bool
// arguments; result void.
func (c *checker) callConsole(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	if len(e.Arguments) != 2 {
		c.errorf(e.Lparen, "E-TYPE", "%s expects a message and optional fields", sel.Builtin)
	}
	if len(e.Arguments) > 0 {
		tv := c.expr(sc, e.Arguments[0], facts, types.Typ[types.String])
		c.assign(sc, e.Arguments[0], tv, types.Typ[types.String])
	}
	if len(e.Arguments) > 1 {
		want := types.NewOptional(types.Typ[types.JsonValue])
		tv := c.expr(sc, e.Arguments[1], facts, want)
		c.assign(sc, e.Arguments[1], tv, want)
	}
	for i := 2; i < len(e.Arguments); i++ {
		c.expr(sc, e.Arguments[i], facts, nil)
	}
	c.recordBuiltinCall(e, m, sel.Builtin, nil)
	return c.record(e, types.Typ[types.Void], nil)
}

func (c *checker) callJson(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	in := sel.Builtin.Info()
	paramTypes := in.Params
	if len(e.Arguments) != len(paramTypes) {
		c.errorf(e.Lparen, "E-TYPE", "%s expects %d argument(s), got %d", sel.Builtin, len(paramTypes), len(e.Arguments))
	}
	for i, a := range e.Arguments {
		want := types.Type(nil)
		if sel.Builtin.Info().Recv == types.RecvJsonValue {
			if i < len(paramTypes) {
				want = paramTypes[i]
			}
		} else if sel.Builtin.Info().Recv == types.RecvJsonType {
			if i < len(paramTypes) {
				want = paramTypes[i]
			}
		} else if i < len(paramTypes) {
			want = paramTypes[i]
		}
		if want != nil {
			tv := c.expr(sc, a, facts, want)
			c.assign(sc, a, tv, want)
		} else {
			c.expr(sc, a, facts, nil)
		}
	}
	c.recordBuiltinCall(e, m, sel.Builtin, nil)
	return c.record(e, in.Result, nil)
}

// callPush types xs.push(v): one argument assignable to the array element
// type; result void.
func (c *checker) callPush(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	arr, _ := sel.Recv.(*types.Array)
	if len(e.Arguments) != 1 {
		c.errorf(e.Lparen, "E-TYPE", "push takes one argument")
		c.checkArgsLoose(sc, e, facts)
	} else if arr != nil {
		tv := c.expr(sc, e.Arguments[0], facts, arr.Elem)
		c.assign(sc, e.Arguments[0], tv, arr.Elem)
	} else {
		c.expr(sc, e.Arguments[0], facts, nil)
	}
	c.recordBuiltinCall(e, m, sel.Builtin, nil)
	return c.record(e, types.Typ[types.Void], nil)
}

// callMatch types ctx.match(method, pattern): both arguments must be string
// literals; the method is validated with ValidRouteMethod and the pattern
// with ParseRoutePattern (E-ROUTE on failure). It appends a types.Route to
// Info.Routes (ID = index+1) and sets Call.Route; result bool.
func (c *checker) callMatch(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	var route *types.Route
	if len(e.Arguments) < 1 || len(e.Arguments) > 2 {
		c.errorf(e.Lparen, "E-ROUTE", "match takes a method and a pattern")
		c.checkArgsLoose(sc, e, facts)
	} else {
		mtv := c.expr(sc, e.Arguments[0], facts, types.Typ[types.String])
		ptv := c.expr(sc, e.Arguments[1], facts, types.Typ[types.String])
		method, mok := constStringArg(mtv)
		pattern, pok := constStringArg(ptv)
		if !mok {
			c.errorf(e.Arguments[0].Pos(), "E-ROUTE", "match method must be a string literal")
		}
		if !pok {
			c.errorf(e.Arguments[1].Pos(), "E-ROUTE", "match pattern must be a string literal")
		}
		if mok && pok {
			route = c.buildRoute(e, method, pattern)
		}
	}
	c.recordBuiltinCall(e, m, sel.Builtin, route)
	return c.record(e, types.Typ[types.Bool], nil)
}

// buildRoute validates a method and pattern and, on success, appends a Route
// to Info.Routes and returns it. On a validation failure it reports E-ROUTE
// and returns nil.
func (c *checker) buildRoute(e *ast.CallExpression, method, pattern string) *types.Route {
	if !types.ValidRouteMethod(method) {
		c.errorf(e.Arguments[0].Pos(), "E-ROUTE", "invalid route method %q", method)
		return nil
	}
	segs, err := types.ParseRoutePattern(pattern)
	if err != nil {
		c.errorf(e.Arguments[1].Pos(), "E-ROUTE", "%s", err.Error())
		return nil
	}
	route := &types.Route{
		ID:       len(c.info.Routes) + 1,
		Call:     e,
		Method:   method,
		Pattern:  pattern,
		Segments: segs,
	}
	c.info.Routes = append(c.info.Routes, route)
	return route
}

// callBindJSON types ctx.bindJson(obj): obj is an interface value whose type
// seeds a JSON parse demand (E-JSON when not JSONEncodable); result bool.
func (c *checker) callBindJSON(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	if len(e.Arguments) != 1 {
		c.errorf(e.Lparen, "E-TYPE", "bindJson takes one argument")
		c.checkArgsLoose(sc, e, facts)
	} else {
		tv := c.expr(sc, e.Arguments[0], facts, nil)
		if !types.IsInvalid(tv.Type) {
			if _, ok := tv.Type.(*types.Named); !ok {
				c.errorf(e.Arguments[0].Pos(), "E-JSON", "bindJson requires an interface value, got %s", tv.Type)
			} else if !types.JSONEncodable(tv.Type) {
				c.errorf(e.Arguments[0].Pos(), "E-JSON", "%s is not JSON-encodable", tv.Type)
			} else {
				c.demandJSON(tv.Type, true, false)
			}
		}
	}
	c.recordBuiltinCall(e, m, sel.Builtin, nil)
	return c.record(e, types.Typ[types.Bool], nil)
}

// callJSON types ctx.json(status, v): status int32, v an interface or array
// of serializable values seeding a JSON write demand (E-JSON otherwise);
// result void.
func (c *checker) callJSON(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	if len(e.Arguments) < 1 || len(e.Arguments) > 2 {
		c.errorf(e.Lparen, "E-TYPE", "json takes a status and a value")
		c.checkArgsLoose(sc, e, facts)
	} else {
		stv := c.expr(sc, e.Arguments[0], facts, types.Typ[types.Int32])
		c.assign(sc, e.Arguments[0], stv, types.Typ[types.Int32])
		vtv := c.expr(sc, e.Arguments[1], facts, nil)
		if !types.IsInvalid(vtv.Type) {
			switch vtv.Type.(type) {
			case *types.Named, *types.Array:
				if !types.JSONEncodable(vtv.Type) {
					c.errorf(e.Arguments[1].Pos(), "E-JSON", "%s is not JSON-encodable", vtv.Type)
				} else {
					c.demandJSON(vtv.Type, false, true)
				}
			default:
				c.errorf(e.Arguments[1].Pos(), "E-JSON", "json requires an interface or array value, got %s", vtv.Type)
			}
		}
	}
	c.recordBuiltinCall(e, m, sel.Builtin, nil)
	return c.record(e, types.Typ[types.Void], nil)
}

// demandJSON seeds a JSON parse and/or write demand for a concrete type,
// merging into an existing Info.JSONTypes entry when present.
func (c *checker) demandJSON(t types.Type, parse, write bool) {
	for _, j := range c.info.JSONTypes {
		if types.Identical(j.Type, t) {
			j.Parse = j.Parse || parse
			j.Write = j.Write || write
			return
		}
	}
	c.info.JSONTypes = append(c.info.JSONTypes, &types.JSONType{Type: t, Parse: parse, Write: write})
}

// callDBExecute types db.execute/tx.execute(sql, args...): sql a string
// literal, remaining args IsDBScalar; result int64.
func (c *checker) callDBExecute(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	c.checkSQLArgs(sc, e, sel.Builtin, facts)
	c.recordBuiltinCall(e, m, sel.Builtin, nil)
	return c.record(e, types.Typ[types.Int64], nil)
}

// callDBQuery types db.query<T>/queryOne<T> (and the tx forms): one type arg
// T a concrete interface of IsDBScalar fields (appended to Info.DBTypes), sql
// a string literal, remaining args IsDBScalar; result T[] or T | null.
func (c *checker) callDBQuery(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet, one bool) types.TypeAndValue {
	rowType := c.dbRowType(sc, e)
	c.checkSQLArgs(sc, e, sel.Builtin, facts)

	var result types.Type
	if rowType == nil {
		result = types.Typ[types.Invalid]
	} else if one {
		result = types.NewOptional(rowType)
	} else {
		result = types.NewArray(rowType)
	}
	call := &types.Call{Kind: types.CallBuiltin, Builtin: sel.Builtin, Recv: m.Object}
	if rowType != nil {
		call.TypeArgs = []types.Type{rowType}
	}
	call.Route = nil
	c.info.Calls[e] = call
	return c.record(e, result, nil)
}

// dbRowType resolves and validates the single type argument of a db/tx query:
// it must be present and resolve to a concrete interface whose fields are all
// IsDBScalar. It appends the row type to Info.DBTypes (dedup) and returns it,
// or nil on an error (reported E-DB).
func (c *checker) dbRowType(sc *scope, e *ast.CallExpression) *types.Named {
	if len(e.TypeArgs) != 1 {
		c.errorf(e.Function.Pos(), "E-DB", "a query needs a single row type argument")
		return nil
	}
	t := c.resolveTypeIn(sc, e.TypeArgs[0])
	if types.IsInvalid(t) {
		return nil
	}
	named, ok := t.(*types.Named)
	if !ok || named.IsGeneric() || !types.IsConcrete(named) {
		c.errorf(e.TypeArgs[0].Pos(), "E-DB", "query row type %s must be a concrete interface", t)
		return nil
	}
	for _, f := range named.Fields() {
		if !types.IsDBScalar(f.Type) {
			c.errorf(e.TypeArgs[0].Pos(), "E-DB", "row field %s of %s is not a database scalar", f.Name, named)
			return nil
		}
	}
	c.addDBType(named)
	return named
}

// addDBType appends named to Info.DBTypes if not already present.
func (c *checker) addDBType(named *types.Named) {
	for _, d := range c.info.DBTypes {
		if d == named {
			return
		}
	}
	c.info.DBTypes = append(c.info.DBTypes, named)
}

// checkSQLArgs checks that the first argument is a string literal and each
// remaining argument is a database scalar (E-DB otherwise).
func (c *checker) checkSQLArgs(sc *scope, e *ast.CallExpression, id types.BuiltinID, facts factSet) {
	if len(e.Arguments) == 0 {
		c.errorf(e.Lparen, "E-DB", "%s needs a SQL string literal", id)
		return
	}
	sqltv := c.expr(sc, e.Arguments[0], facts, types.Typ[types.String])
	if sql, ok := constStringArg(sqltv); !ok {
		c.errorf(e.Arguments[0].Pos(), "E-DB", "SQL argument must be a string literal")
	} else if strings.IndexByte(sql, 0) >= 0 {
		c.errorf(e.Arguments[0].Pos(), "E-DB", "SQL string literal contains a NUL byte")
	}
	for _, a := range e.Arguments[1:] {
		tv := c.expr(sc, a, facts, nil)
		if !types.IsInvalid(tv.Type) && !types.IsDBScalar(types.Default(tv.Type)) {
			c.errorf(a.Pos(), "E-DB", "database argument %s is not a scalar", tv.Type)
		}
	}
}

// recordBuiltinCall records Info.Calls[e] for a builtin member or namespace
// call, setting Recv for a value-member call (not for a namespace call) and
// the compiled Route for ctx.match.
func (c *checker) recordBuiltinCall(e *ast.CallExpression, m *ast.MemberExpression, id types.BuiltinID, route *types.Route) {
	call := &types.Call{Kind: types.CallBuiltin, Builtin: id, Route: route}
	// A namespace member (console.*, db.*) has no value receiver.
	if !c.isNamespaceObject(m.Object) {
		call.Recv = m.Object
	}
	c.info.Calls[e] = call
}

// isNamespaceObject reports whether o is the console or db namespace
// identifier.
func (c *checker) isNamespaceObject(o ast.Expression) bool {
	switch o := o.(type) {
	case *ast.Identifier:
		_, ok := c.info.Uses[o].(*types.Builtin)
		return ok
	case *ast.MemberExpression:
		sel := c.info.Selections[o]
		return sel != nil && sel.Kind == types.SelNamespace
	}
	return false
}
