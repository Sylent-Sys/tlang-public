package checker

import (
	"go/constant"

	"tlang/ast"
	"tlang/types"
)

// builtins.go resolves member selections and call expressions (DESIGN.md
// §2.6-§2.11): user fields/methods, builtin members via MemberOf, namespaces
// via NamespaceMember, numeric conversions via ConversionOf, generic user
// calls via Infer+InstantiateFunc, and the special-cased builtins (console,
// push, ctx.match routes, bindJson/json JSON demand, db/tx SQL checks). It
// fills Info.Selections/Calls and seeds Info.Routes/DBTypes/UsesDB and the
// JSON demand set.

// ---------------------------------------------------------------------------
// Member selections (not callees)

// exprMember types a member expression used as a value (not a call callee),
// recording Info.Selections[e] and returning the selected value's type. A
// method or callee-only builtin used as a value is E-TYPE.
func (c *checker) exprMember(sc *scope, e *ast.MemberExpression, facts factSet) types.TypeAndValue {
	sel := c.resolveSelection(sc, e, facts, false)
	if sel == nil {
		return c.invalid(e)
	}
	switch sel.Kind {
	case types.SelField:
		return c.record(e, sel.Type, nil)
	case types.SelModuleValue:
		return c.record(e, sel.Type, nil)
	case types.SelNamespace:
		c.errorf(e.Property.NamePos, "E-TYPE", "%s is a namespace, not a value", e.Property.Name)
		return c.invalid(e)
	case types.SelBuiltin:
		if sel.Type == nil {
			c.errorf(e.Property.NamePos, "E-TYPE", "%s must be called", e.Property.Name)
			return c.invalid(e)
		}
		return c.record(e, sel.Type, nil)
	case types.SelMethod:
		if sel.Recv == nil {
			// A module-qualified function used as a value ("m.foo" without a
			// call): a function is not a value (DESIGN.md §2.6).
			c.errorf(e.Property.NamePos, "E-TYPE", "%s is a function, not a value", e.Property.Name)
			return c.invalid(e)
		}
		c.errorf(e.Property.NamePos, "E-TYPE", "method %s must be called", e.Property.Name)
	}
	return c.invalid(e)
}

// resolveSelection resolves a member expression and fills Info.Selections[m].
// asCallee reports that m is the callee of a call (so method-like and
// callee-only builtins are valid). It returns the Selection, or nil on an
// error that was reported.
func (c *checker) resolveSelection(sc *scope, m *ast.MemberExpression, facts factSet, asCallee bool) *types.Selection {
	if obj, id := c.namespaceObject(sc, m.Object, facts); obj != nil {
		switch ns := obj.(type) {
		case *types.Builtin:
			return c.selectNamespace(m, id, ns, asCallee)
		case *types.ModuleNS:
			return c.selectModuleNS(m, id, ns, asCallee)
		}
	}

	tv := c.expr(sc, m.Object, facts, nil)
	recv := tv.Type
	if types.IsInvalid(recv) {
		return nil
	}
	if types.IsOptional(recv) {
		c.errorf(m.Property.NamePos, "E-TYPE", "%s has no members; narrow first", recv)
		return nil
	}

	// User interface receiver: a field or a user method.
	if named, ok := recv.(*types.Named); ok {
		if f := named.Field(m.Property.Name); f != nil {
			sel := &types.Selection{Kind: types.SelField, Recv: recv, Field: f, Type: f.Type}
			c.info.Selections[m] = sel
			return sel
		}
		if method := named.Method(m.Property.Name); method != nil {
			sel := &types.Selection{Kind: types.SelMethod, Recv: recv, Func: method}
			c.info.Selections[m] = sel
			return sel
		}
		// Fall through to builtin members (e.g. none for interfaces) below.
	}

	// Context user methods are kept in the checker's side table.
	if types.IsBasic(recv, types.Context) {
		if method := c.methods[m.Property.Name]; method != nil {
			sel := &types.Selection{Kind: types.SelMethod, Recv: recv, Func: method}
			c.info.Selections[m] = sel
			return sel
		}
	}

	// Builtin member of the value (defaults the receiver, so 5.toString()
	// works).
	id := types.MemberOf(recv, m.Property.Name)
	if id == types.BuiltinInvalid {
		c.errorf(m.Property.NamePos, "E-TYPE", "%s has no field or method %s", recv, m.Property.Name)
		return nil
	}
	sel := &types.Selection{Kind: types.SelBuiltin, Recv: recv, Builtin: id}
	in := id.Info()
	if !in.Method {
		sel.Type = in.Result // field-like: s.len, ctx.path, err.status
	}
	c.info.Selections[m] = sel
	return sel
}

func (c *checker) namespaceObject(sc *scope, e ast.Expression, facts factSet) (types.Object, *ast.Identifier) {
	switch e := e.(type) {
	case *ast.Identifier:
		obj := sc.lookup(e.Name)
		switch obj.(type) {
		case *types.Builtin, *types.ModuleNS:
			return obj, e
		}
	case *ast.MemberExpression:
		sel := c.info.Selections[e]
		if sel == nil {
			sel = c.resolveSelection(sc, e, facts, false)
		}
		if sel != nil && sel.Kind == types.SelNamespace {
			return sel.Namespace, nil
		}
		if sel != nil {
			return nil, nil
		}
		return c.namespaceObject(sc, e.Object, facts)
	}
	return nil, nil
}

// selectModuleNS resolves a member of a module namespace binding ("m.X"
// where m is "import * as m from ..."). It resolves X against ns's export set
// (DESIGN-modules.md §9) with the FR-16 ("not exported") vs FR-17 ("no such
// member") distinction, records a DIRECT reference to the target object
// (qualified-access lowering: codegen emits only the target's tagged name),
// and returns the Selection. A value export yields SelModuleValue; a function
// export yields a receiver-less SelMethod (callMember lowers it to CallFunc);
// a type export used in value position is E-TYPE.
func (c *checker) selectModuleNS(m *ast.MemberExpression, id *ast.Identifier, ns *types.ModuleNS, asCallee bool) *types.Selection {
	if id != nil {
		c.info.Uses[id] = ns
	}
	target, ok := ns.Exports[m.Property.Name]
	if !ok {
		// FR-16 vs FR-17: a name that the module declares but does not export
		// is "not exported"; a name it has no declaration for is "no such
		// member". The export set only holds exported names, so from here we
		// cannot tell the two apart precisely; a module namespace reports the
		// uniform "no exported member" wording (the import-table path makes
		// the finer FR-16/FR-17 distinction at import time).
		c.errorf(m.Property.NamePos, "E-IMPORT",
			"no exported member %s in module %s", m.Property.Name, ns.Name)
		return nil
	}
	switch obj := target.(type) {
	case *types.Builtin, *types.ModuleNS:
		sel := &types.Selection{Kind: types.SelNamespace, Namespace: obj}
		c.info.Selections[m] = sel
		return sel
	case *types.Func:
		// A receiver-less method selection: callMember lowers it to a direct
		// CallFunc on the target, so codegen uses the target's tagged name.
		sel := &types.Selection{Kind: types.SelMethod, Func: obj}
		c.info.Selections[m] = sel
		return sel
	case *types.Var:
		sel := &types.Selection{Kind: types.SelModuleValue, Field: obj, Type: obj.Type}
		c.info.Selections[m] = sel
		return sel
	case *types.TypeName:
		c.errorf(m.Property.NamePos, "E-TYPE", "%s is a type, not a value", m.Property.Name)
		return nil
	}
	c.errorf(m.Property.NamePos, "E-TYPE", "%s is not a value", m.Property.Name)
	return nil
}

// selectNamespace resolves a member of the console or db namespace.
func (c *checker) selectNamespace(m *ast.MemberExpression, id *ast.Identifier, b *types.Builtin, asCallee bool) *types.Selection {
	member := types.StandardNamespaceMember(b.ID, m.Property.Name)
	if member == types.BuiltinInvalid {
		c.errorf(m.Property.NamePos, "E-TYPE", "%s has no member %s", b.Name, m.Property.Name)
		return nil
	}
	if id != nil {
		c.info.Uses[id] = b
	}
	sel := &types.Selection{Kind: types.SelBuiltin, Builtin: member}
	c.info.Selections[m] = sel
	return sel
}

// ---------------------------------------------------------------------------
// Calls

// exprCall types a call expression, dispatching on the callee: a user
// function or conversion name, a user/Context method or builtin member, or
// an error for any other callee shape.
func (c *checker) exprCall(sc *scope, e *ast.CallExpression, facts factSet, want types.Type) types.TypeAndValue {
	switch callee := e.Function.(type) {
	case *ast.Identifier:
		return c.callIdent(sc, e, callee, facts, want)
	case *ast.MemberExpression:
		return c.callMember(sc, e, callee, facts)
	}
	c.errorf(e.Function.Pos(), "E-TYPE", "expression is not callable")
	for _, a := range e.Arguments {
		c.expr(sc, a, facts, nil)
	}
	return c.invalid(e)
}

// callIdent types a call whose callee is an identifier: a user function
// (CallFunc, with generic inference), or a universe numeric conversion
// (CallConversion). Anything else is E-TYPE.
func (c *checker) callIdent(sc *scope, e *ast.CallExpression, callee *ast.Identifier, facts factSet, want types.Type) types.TypeAndValue {
	obj := sc.lookup(callee.Name)
	if obj == nil {
		c.errorf(callee.NamePos, "E-NAME", "undefined: %s", callee.Name)
		c.checkArgsLoose(sc, e, facts)
		return c.invalid(e)
	}
	switch obj := obj.(type) {
	case *types.Func:
		c.info.Uses[callee] = obj
		return c.callFunc(sc, e, obj, facts)
	case *types.TypeName:
		if conv := types.ConversionOf(obj.Type); conv != types.BuiltinInvalid {
			c.info.Uses[callee] = obj
			return c.callConversion(sc, e, conv, facts)
		}
		c.errorf(callee.NamePos, "E-TYPE", "%s is not callable", callee.Name)
	default:
		c.errorf(callee.NamePos, "E-TYPE", "%s is not callable", callee.Name)
	}
	c.checkArgsLoose(sc, e, facts)
	return c.invalid(e)
}

// callConversion types int32(x) / int64(x) / float64(x): exactly one numeric
// argument; result the target type; never may-fail.
func (c *checker) callConversion(sc *scope, e *ast.CallExpression, conv types.BuiltinID, facts factSet) types.TypeAndValue {
	result := conv.Info().Result
	if len(e.Arguments) != 1 {
		c.errorf(e.Lparen, "E-TYPE", "%s conversion takes one argument", conv)
		c.checkArgsLoose(sc, e, facts)
		c.info.Calls[e] = &types.Call{Kind: types.CallConversion, Builtin: conv}
		return c.record(e, result, nil)
	}
	tv := c.expr(sc, e.Arguments[0], facts, nil)
	if !types.IsInvalid(tv.Type) && !types.IsNumeric(tv.Type) {
		c.errorf(e.Arguments[0].Pos(), "E-TYPE", "%s requires a numeric argument, got %s", conv, tv.Type)
	}
	c.info.Calls[e] = &types.Call{Kind: types.CallConversion, Builtin: conv}
	// Fold a constant conversion when representable.
	if cv, ok := types.Representable(tv.Value, result); ok {
		return c.record(e, result, cv)
	}
	return c.record(e, result, nil)
}

// callFunc types a user function call: non-generic arity + assignable args,
// or generic inference (Infer + ValidTypeArg + InstantiateFunc) with args
// checked against the substituted parameters. It records Info.Calls[e].
func (c *checker) callFunc(sc *scope, e *ast.CallExpression, fn *types.Func, facts factSet) types.TypeAndValue {
	if fn.Sig == nil {
		c.checkArgsLoose(sc, e, facts)
		return c.invalid(e)
	}
	sig := fn.Sig
	// Evaluate arguments once (pre-default form kept in Info.Types).
	argTVs := make([]types.TypeAndValue, len(e.Arguments))
	for i, a := range e.Arguments {
		argTVs[i] = c.expr(sc, a, facts, nil)
	}

	if !fn.IsGeneric() {
		return c.callNonGeneric(sc, e, fn, sig, argTVs, facts)
	}
	return c.callGeneric(sc, e, fn, sig, argTVs, facts)
}

// callNonGeneric checks a non-generic user call's arity and argument
// assignability (recording conversions), and records the Call.
func (c *checker) callNonGeneric(sc *scope, e *ast.CallExpression, fn *types.Func, sig *types.Signature, argTVs []types.TypeAndValue, facts factSet) types.TypeAndValue {
	if len(e.TypeArgs) > 0 {
		c.errorf(e.Langle, "E-GENERIC", "%s is not generic", fn.Name)
	}
	if len(e.Arguments) != len(sig.Params) {
		c.errorf(e.Lparen, "E-TYPE", "%s expects %d argument(s), got %d", fn.Name, len(sig.Params), len(e.Arguments))
	}
	n := min2(len(e.Arguments), len(sig.Params))
	for i := 0; i < n; i++ {
		want := sig.Params[i].Type
		// Re-record the argument against the parameter so an untyped constant
		// takes the parameter type and conversions are recorded.
		tv := c.expr(sc, e.Arguments[i], facts, want)
		c.assign(sc, e.Arguments[i], tv, want)
	}
	c.info.Calls[e] = &types.Call{Kind: types.CallFunc, Func: fn}
	return c.record(e, sig.Result, nil)
}

// callGeneric infers the type arguments of a generic user call (from explicit
// type args or from Infer), validates them, instantiates the function, checks
// each argument against the substituted parameter, and records the Call with
// the canonical instance and type arguments.
func (c *checker) callGeneric(sc *scope, e *ast.CallExpression, fn *types.Func, sig *types.Signature, argTVs []types.TypeAndValue, facts factSet) types.TypeAndValue {
	explicit := c.typeArgs(sc, e.TypeArgs)
	paramTypes := make([]types.Type, len(sig.Params))
	for i, p := range sig.Params {
		paramTypes[i] = p.Type
	}
	argTypes := make([]types.Type, len(argTVs))
	for i := range argTVs {
		argTypes[i] = c.inferArgType(e.Arguments[i], argTVs[i])
	}
	targs, err := types.Infer(sig.TypeParams, explicit, paramTypes, argTypes)
	if err != nil {
		c.errorf(e.Function.Pos(), "E-GENERIC", "%s", err.Error())
		return c.invalid(e)
	}
	for i, a := range targs {
		if !types.ValidTypeArg(a) {
			pos := e.Function.Pos()
			if i < len(e.TypeArgs) {
				pos = e.TypeArgs[i].Pos()
			}
			c.errorf(pos, "E-GENERIC", "%s is not a valid type argument", a)
			return c.invalid(e)
		}
	}
	inst, ok := c.instantiateFunc(fn, targs, e.Function.Pos())
	if !ok {
		return c.invalid(e)
	}
	if len(e.Arguments) != len(inst.Sig.Params) {
		c.errorf(e.Lparen, "E-TYPE", "%s expects %d argument(s), got %d", fn.Name, len(inst.Sig.Params), len(e.Arguments))
	}
	n := min2(len(e.Arguments), len(inst.Sig.Params))
	for i := 0; i < n; i++ {
		want := inst.Sig.Params[i].Type
		// Re-record the argument against the substituted parameter so an
		// untyped constant takes the concrete type and conversions are
		// recorded; the first eval (want nil) only served inference.
		tv := c.expr(sc, e.Arguments[i], facts, want)
		c.assign(sc, e.Arguments[i], tv, want)
	}
	c.info.Calls[e] = &types.Call{Kind: types.CallFunc, Func: inst, TypeArgs: inst.TypeArgs}
	return c.record(e, inst.Sig.Result, nil)
}

// inferArgType returns the type to feed to types.Infer for a call argument:
// the pre-default untyped type for a bare constant literal (so Infer can
// default it through its own untyped pass), else the recorded type. The
// first argument eval used want==nil, which defaults untyped int/float, so a
// literal's untyped-ness is recovered here from its AST shape.
func (c *checker) inferArgType(a ast.Expression, tv types.TypeAndValue) types.Type {
	switch a.(type) {
	case *ast.IntegerLiteral:
		if types.IsInteger(tv.Type) {
			return types.Typ[types.UntypedInt]
		}
	case *ast.FloatLiteral:
		if types.IsFloat(tv.Type) {
			return types.Typ[types.UntypedFloat]
		}
	case *ast.NullLiteral:
		return types.Typ[types.UntypedNull]
	}
	return tv.Type
}

// callMember types a call whose callee is a member expression: a user/Context
// method call (CallMethod) or a builtin member/namespace call (CallBuiltin or
// a special-cased builtin).
func (c *checker) callMember(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, facts factSet) types.TypeAndValue {
	sel := c.resolveSelection(sc, m, facts, true)
	if sel == nil {
		c.checkArgsLoose(sc, e, facts)
		return c.invalid(e)
	}
	switch sel.Kind {
	case types.SelMethod:
		if sel.Recv == nil {
			// A module-qualified function call ("m.foo(args)"): lower to a
			// direct call on the target function, so it is indistinguishable
			// from a bare call to the same imported symbol.
			return c.callFunc(sc, e, sel.Func, facts)
		}
		return c.callUserMethod(sc, e, m, sel, facts)
	case types.SelBuiltin:
		return c.callBuiltin(sc, e, m, sel, facts)
	case types.SelModuleValue:
		c.errorf(m.Property.NamePos, "E-TYPE", "%s is not callable", m.Property.Name)
	case types.SelNamespace:
		c.errorf(m.Property.NamePos, "E-TYPE", "%s is a namespace, not a callable value", m.Property.Name)
	case types.SelField:
		c.errorf(m.Property.NamePos, "E-TYPE", "field %s is not callable", m.Property.Name)
	}
	c.checkArgsLoose(sc, e, facts)
	return c.invalid(e)
}

// callUserMethod types a call to a user receiver method (interface or
// Context): arity + assignable args; result the method's result. Generic
// receiver methods are not allowed in v1 (resolveSignatures rejected them),
// so the signature is used directly.
func (c *checker) callUserMethod(sc *scope, e *ast.CallExpression, m *ast.MemberExpression, sel *types.Selection, facts factSet) types.TypeAndValue {
	fn := sel.Func
	if fn.Sig == nil {
		c.checkArgsLoose(sc, e, facts)
		return c.invalid(e)
	}
	sig := fn.Sig
	if len(e.Arguments) != len(sig.Params) {
		c.errorf(e.Lparen, "E-TYPE", "%s expects %d argument(s), got %d", fn.Name, len(sig.Params), len(e.Arguments))
	}
	for i, a := range e.Arguments {
		if i < len(sig.Params) {
			want := sig.Params[i].Type
			tv := c.expr(sc, a, facts, want)
			c.assign(sc, a, tv, want)
		} else {
			c.expr(sc, a, facts, nil)
		}
	}
	c.info.Calls[e] = &types.Call{Kind: types.CallMethod, Func: fn, Recv: m.Object}
	return c.record(e, sig.Result, nil)
}

// checkArgsLoose evaluates every call argument with no want, for error
// recovery when the callee could not be resolved.
func (c *checker) checkArgsLoose(sc *scope, e *ast.CallExpression, facts factSet) {
	for _, a := range e.Arguments {
		c.expr(sc, a, facts, nil)
	}
}

// constStringArg returns the constant string value of a call argument, or
// ("", false) when it is not a string literal / constant.
func constStringArg(tv types.TypeAndValue) (string, bool) {
	if tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value), true
	}
	return "", false
}
