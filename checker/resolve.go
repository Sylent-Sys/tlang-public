package checker

import (
	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// collectNames is pass 1 (DESIGN.md pass structure): it walks the top-level
// declarations, creates the object for each (interface *Named + *TypeName,
// alias *TypeName or nominal *Named, function *Func with a nil Sig
// placeholder, global *Var), records Info.Defs for the declaring identifier,
// and declares it in the global scope. It resolves no field, signature or
// body: those are pass 2 and later. Redeclaration and CheckDeclName are
// enforced here through declare.
func (c *checker) collectNames(prog *ast.Program) {
	c.collectNamesInto(prog, c.global)
}

// collectNamesInto is collectNames targeting an explicit scope, so
// CheckProgram can collect each module's declarations into that module's own
// scope while recording the scope on every worklist entry (DESIGN-modules.md
// §4). The single-file Check passes c.global.
func (c *checker) collectNamesInto(prog *ast.Program, sc *scope) {
	for _, stmt := range prog.Statements {
		switch s := stmt.(type) {
		case *ast.InterfaceStatement:
			c.collectInterface(s, sc)
		case *ast.TypeAliasStatement:
			c.collectAlias(s, sc)
		case *ast.FunctionStatement:
			c.collectFunction(s, sc)
		case *ast.LetStatement:
			c.collectGlobal(s, sc)
		}
	}
}

// collectInterface creates the *TypeName and generic-origin/plain *Named for
// an interface declaration, with type parameters built from TypeParams, and
// declares the name in the global scope.
func (c *checker) collectInterface(s *ast.InterfaceStatement, sc *scope) {
	obj := &types.TypeName{Name: s.Name.Name, Pos: s.Name.NamePos, Decl: s, Tag: c.scopeTag(sc)}
	tparams := c.typeParams(s.TypeParams)
	named := types.NewNamed(obj, s, tparams)
	obj.Type = named
	c.info.Defs[s.Name] = obj
	c.declFiles[obj] = c.file
	c.declare(sc, s.Name.Name, types.DeclType, obj, s.Name.NamePos)
	c.interfaces = append(c.interfaces, &ifaceDecl{stmt: s, named: named, obj: obj, sc: sc})
}

// collectAlias creates the object for a type-alias declaration: a nominal
// *Named when the value is an object type (DESIGN.md §2.3), or a transparent
// alias *TypeName otherwise. The aliased type is resolved in pass 2.
func (c *checker) collectAlias(s *ast.TypeAliasStatement, sc *scope) {
	obj := &types.TypeName{Name: s.Name.Name, Pos: s.Name.NamePos, Decl: s, Tag: c.scopeTag(sc)}
	tparams := c.typeParams(s.TypeParams)
	d := &aliasDecl{stmt: s, obj: obj, sc: sc}
	if _, isObject := s.Value.(*ast.ObjectType); isObject {
		named := types.NewNamed(obj, s, tparams)
		obj.Type = named
		d.named = named
	} else {
		obj.IsAlias = true
		obj.TypeParams = tparams
		d.alias = obj
	}
	c.info.Defs[s.Name] = obj
	c.declFiles[obj] = c.file
	c.declare(sc, s.Name.Name, types.DeclType, obj, s.Name.NamePos)
	c.aliases = append(c.aliases, d)
}

// collectFunction creates the *Func for a function declaration with a nil
// Sig placeholder (resolved in pass 2) and declares its name. Receivers are
// resolved in pass 2 (methods are recorded there); only the plain function
// name is reserved here.
func (c *checker) collectFunction(s *ast.FunctionStatement, sc *scope) {
	fn := &types.Func{Name: s.Name.Name, Pos: s.Name.NamePos, Decl: s, Tag: c.scopeTag(sc)}
	c.info.Defs[s.Name] = fn
	c.declFiles[fn] = c.file
	// A method name does not occupy the global function namespace; it is
	// attached to its receiver type in pass 2.
	if s.Receiver == nil {
		c.declare(sc, s.Name.Name, types.DeclValue, fn, s.Name.NamePos)
	}
	c.funcs = append(c.funcs, &funcDecl{stmt: s, fn: fn, sc: sc})
}

// collectGlobal creates the GlobalVar *Var for a top-level let/const and
// declares its name. The type and initializer are resolved later.
func (c *checker) collectGlobal(s *ast.LetStatement, sc *scope) {
	v := &types.Var{
		Name:    s.Name.Name,
		Kind:    types.GlobalVar,
		IsConst: s.IsConst,
		Pos:     s.Name.NamePos,
		Decl:    s,
		Index:   len(c.globals),
		Tag:     c.scopeTag(sc),
	}
	c.info.Defs[s.Name] = v
	c.declFiles[v] = c.file
	c.declare(sc, s.Name.Name, types.DeclValue, v, s.Name.NamePos)
	c.globals = append(c.globals, v)
	if c.globalScope != nil {
		c.globalScope[v] = sc
	}
}

// typeParams builds the *TypeParam list for a generic declaration: one
// *TypeName per name whose Type is a fresh NewTypeParam, recorded in
// Info.Defs. The names are declared in the scope that wraps the declaration
// by the pass-2 resolver, not here.
func (c *checker) typeParams(names []*ast.Identifier) []*types.TypeParam {
	if len(names) == 0 {
		return nil
	}
	tps := make([]*types.TypeParam, len(names))
	for i, name := range names {
		obj := &types.TypeName{Name: name.Name, Pos: name.NamePos, Decl: name}
		tp := types.NewTypeParam(obj, i)
		tps[i] = tp
		c.info.Defs[name] = obj
	}
	return tps
}

// inScope runs fn with c.global temporarily pointed at sc, so a per-module
// declaration resolves names and types in its own module scope
// (DESIGN-modules.md §4), and with c.file pointed at that module's file, so
// its diagnostics name it. A nil sc (the single-file Check, which records no
// per-entry scope) leaves c.global unchanged, and a scope with no recorded
// file (every scope of the single-file Check) leaves c.file unchanged. The
// passes are strictly sequential, so the swap is safe.
func (c *checker) inScope(sc *scope, fn func()) {
	if sc == nil {
		fn()
		return
	}
	prev := c.global
	c.global = sc
	if file, ok := c.scopeFiles[sc]; ok {
		c.inFile(file, fn)
	} else {
		fn()
	}
	c.global = prev
}

// resolveType resolves an ast.TypeExpr to its types.Type, recording the
// result in Info.TypeExprs, and emits the OQ1 redundant-optional warning
// (codeOptional) uniformly at every optional-written position. It never
// panics on a parser-produced tree; a bad or undefined type yields
// Typ[Invalid] so one error does not cascade.
func (c *checker) resolveType(e ast.TypeExpr) types.Type {
	return c.resolveTypeIn(c.global, e)
}

// resolveTypeIn resolves e in the given scope (type-parameter scopes wrap
// the global scope during pass 2).
func (c *checker) resolveTypeIn(s *scope, e ast.TypeExpr) types.Type {
	switch e := e.(type) {
	case *ast.NamedType:
		return c.recordType(e, c.resolveNamedType(s, e))
	case *ast.ArrayType:
		return c.recordType(e, types.NewArray(c.resolveTypeIn(s, e.Elem)))
	case *ast.OptionalType:
		inner := c.resolveTypeIn(s, e.Elem)
		if types.IsOptional(inner) {
			// OQ1: the written element is already optional. Warn, keep the
			// collapsed single T | null type NewOptional produces.
			c.warnf(e.Pos(), codeOptional, "redundant optional: %s is already optional", inner)
		}
		if !types.CanBeOptional(types.NonOptional(inner)) {
			c.errorf(e.Pos(), "E-TYPE", "%s cannot be optional", inner)
			return c.recordType(e, types.Typ[types.Invalid])
		}
		return c.recordType(e, types.NewOptional(inner))
	case *ast.ParenType:
		return c.recordType(e, c.resolveTypeIn(s, e.Inner))
	case *ast.BadType:
		return c.setInvalid(e)
	}
	// *ast.ObjectType reaches here only as a misplaced value; the alias code
	// handles the legitimate case. Recover with Invalid.
	return c.setInvalid(e)
}

// resolveNamedType resolves a NamedType (a type name with optional type
// arguments). It handles universe types, plain and generic interfaces, type
// parameters and transparent/generic aliases, reporting E-NAME for an
// undefined name, E-TYPE for a value/namespace used as a type, and E-GENERIC
// for arity or instantiation failures.
func (c *checker) resolveNamedType(s *scope, e *ast.NamedType) types.Type {
	if e.Qualifier != nil {
		return c.resolveQualifiedNamedType(s, e)
	}
	obj := s.lookup(e.Name.Name)
	if obj == nil {
		c.errorf(e.Name.NamePos, "E-NAME", "undefined type: %s", e.Name.Name)
		return types.Typ[types.Invalid]
	}
	c.info.Uses[e.Name] = obj
	tn, ok := obj.(*types.TypeName)
	if !ok {
		c.errorf(e.Name.NamePos, "E-TYPE", "%s is not a type", e.Name.Name)
		return types.Typ[types.Invalid]
	}
	return c.resolveTypeNameRef(s, e, tn)
}

// resolveQualifiedNamedType resolves a dotted type "m.User" (DESIGN-modules.md
// §2.3, §9): the qualifier must resolve to a module namespace, and the member
// must be an exported type of that module. It then runs the same arity/alias/
// instance resolution as an unqualified type reference, so "m.Page<User>"
// instantiates exactly like "Page<User>" would.
func (c *checker) resolveQualifiedNamedType(s *scope, e *ast.NamedType) types.Type {
	qobj := s.lookup(e.Qualifier.Name)
	if qobj == nil {
		c.errorf(e.Qualifier.NamePos, "E-NAME", "undefined: %s", e.Qualifier.Name)
		return types.Typ[types.Invalid]
	}
	ns, ok := qobj.(*types.ModuleNS)
	if !ok {
		c.errorf(e.Qualifier.NamePos, "E-TYPE", "%s is not a module namespace", e.Qualifier.Name)
		return types.Typ[types.Invalid]
	}
	c.info.Uses[e.Qualifier] = ns
	target, ok := ns.Exports[e.Name.Name]
	if !ok {
		c.errorf(e.Name.NamePos, "E-IMPORT",
			"no exported member %s in module %s", e.Name.Name, ns.Name)
		return types.Typ[types.Invalid]
	}
	tn, ok := target.(*types.TypeName)
	if !ok {
		c.errorf(e.Name.NamePos, "E-TYPE", "%s is not a type", e.Name.Name)
		return types.Typ[types.Invalid]
	}
	c.info.Uses[e.Name] = tn
	return c.resolveTypeNameRef(s, e, tn)
}

// resolveTypeNameRef resolves a NamedType whose name has already been bound to
// the TypeName tn (whether unqualified or module-qualified). It applies the
// type arguments and dispatches on what tn denotes.
func (c *checker) resolveTypeNameRef(s *scope, e *ast.NamedType, tn *types.TypeName) types.Type {
	args := c.typeArgs(s, e.TypeArgs)

	switch t := tn.Type.(type) {
	case *types.TypeParam:
		if len(e.TypeArgs) > 0 {
			c.errorf(e.Name.NamePos, "E-GENERIC", "type parameter %s cannot take type arguments", tn.Name)
		}
		return t
	case *types.Named:
		return c.resolveNamedInstance(s, e, tn, t, args)
	}

	// Universe basic type or non-generic alias target: no type arguments.
	if tn.IsAlias {
		return c.resolveAlias(s, e, tn, args)
	}
	if len(e.TypeArgs) > 0 {
		c.errorf(e.Name.NamePos, "E-GENERIC", "%s does not take type arguments", tn.Name)
	}
	return tn.Type
}

// resolveNamedInstance resolves a NamedType whose name denotes an interface
// *Named: a plain type with no args, or a generic origin instantiated with
// args (E-GENERIC on arity, invalid type argument, or instantiation failure).
func (c *checker) resolveNamedInstance(s *scope, e *ast.NamedType, tn *types.TypeName, named *types.Named, args []types.Type) types.Type {
	if !named.IsGeneric() {
		if len(e.TypeArgs) > 0 {
			c.errorf(e.Name.NamePos, "E-GENERIC", "%s is not generic", tn.Name)
			return types.Typ[types.Invalid]
		}
		return named
	}
	// Generic origin: it needs type arguments.
	if len(e.TypeArgs) == 0 {
		c.errorf(e.Name.NamePos, "E-GENERIC", "generic type %s needs type arguments", tn.Name)
		return types.Typ[types.Invalid]
	}
	if len(args) != len(named.TypeParams) {
		c.errorf(e.Name.NamePos, "E-GENERIC",
			"%s expects %d type argument(s), got %d", tn.Name, len(named.TypeParams), len(args))
		return types.Typ[types.Invalid]
	}
	for i, a := range args {
		if !types.ValidTypeArg(a) {
			c.errorf(c.typeArgPos(e, i), "E-GENERIC", "%s is not a valid type argument", a)
			return types.Typ[types.Invalid]
		}
	}
	inst, err := c.info.Instances.InstantiateNamed(named, args)
	if err != nil {
		c.errorf(e.Name.NamePos, "E-GENERIC", "%s", err.Error())
		return types.Typ[types.Invalid]
	}
	c.recordInstSite(inst, e.Name.NamePos)
	return inst
}

// resolveAlias resolves a use of a type alias: a transparent alias yields its
// aliased type; a generic alias substitutes its type parameters (OQ3). An
// object-type alias is a nominal *Named handled by the *types.Named case, not
// here.
func (c *checker) resolveAlias(s *scope, e *ast.NamedType, tn *types.TypeName, args []types.Type) types.Type {
	if len(tn.TypeParams) == 0 {
		if len(e.TypeArgs) > 0 {
			c.errorf(e.Name.NamePos, "E-GENERIC", "%s is not generic", tn.Name)
			return types.Typ[types.Invalid]
		}
		return tn.Type
	}
	// Generic transparent alias.
	if len(args) != len(tn.TypeParams) {
		c.errorf(e.Name.NamePos, "E-GENERIC",
			"%s expects %d type argument(s), got %d", tn.Name, len(tn.TypeParams), len(args))
		return types.Typ[types.Invalid]
	}
	for i, a := range args {
		if !types.ValidTypeArg(a) {
			c.errorf(c.typeArgPos(e, i), "E-GENERIC", "%s is not a valid type argument", a)
			return types.Typ[types.Invalid]
		}
	}
	return c.info.Instances.Subst(tn.Type, tn.TypeParams, args)
}

// typeArgs resolves a list of type-argument expressions.
func (c *checker) typeArgs(s *scope, exprs []ast.TypeExpr) []types.Type {
	if len(exprs) == 0 {
		return nil
	}
	args := make([]types.Type, len(exprs))
	for i, a := range exprs {
		args[i] = c.resolveTypeIn(s, a)
	}
	return args
}

// typeArgPos returns the position of the i-th type argument of e, or the
// name position when it is out of range.
func (c *checker) typeArgPos(e *ast.NamedType, i int) token.Position {
	if i >= 0 && i < len(e.TypeArgs) {
		return e.TypeArgs[i].Pos()
	}
	return e.Name.NamePos
}

// recordInstSite remembers the first source position that requested the
// canonical instance key, for OQ7 error attribution.
func (c *checker) recordInstSite(key any, pos token.Position) {
	if _, seen := c.instSite[key]; !seen {
		c.instSite[key] = filePos{c.file, pos}
	}
}

// resolveSignatures is pass 2 (DESIGN.md pass structure): it resolves every
// interface's type parameters and fields (SetFields), every alias's aliased
// type, and every function's receiver/type-params/parameters/result into a
// Signature set on *Func.Sig; attaches receiver methods to their *Named or
// the Context-method table; then runs the cyclic-required-field DFS and the
// §2.9 method/field/builtin-member collision check. After this pass every
// signature, *Named and global type is set and collision-free.
func (c *checker) resolveSignatures() {
	// Transparent-alias targets first: an alias name may stand for an array
	// or optional that interface fields refer to, and resolving the target
	// needs only the referenced type's identity, not its fields.
	for _, d := range c.aliases {
		if d.alias != nil {
			c.inScope(d.sc, func() { c.resolveAliasValue(d) })
		}
	}
	for _, d := range c.interfaces {
		c.inScope(d.sc, func() { c.resolveInterfaceFields(d) })
	}
	for _, d := range c.aliases {
		if d.named != nil {
			c.inScope(d.sc, func() { c.resolveAliasValue(d) })
		}
	}
	for _, d := range c.funcs {
		c.inScope(d.sc, func() { c.resolveSignature(d) })
	}
	for _, d := range c.funcs {
		c.inScope(d.sc, func() { c.attachMethod(d) })
	}
	for _, g := range c.globals {
		c.inScope(c.globalScope[g], func() { c.resolveGlobalType(g) })
	}
	c.checkRequiredFieldCycles()
	c.checkMethodCollisions()
}

// typeParamScope returns a child of the global scope holding the given type
// parameters' name objects, so their names are visible while resolving a
// generic declaration's fields, signature or body.
func (c *checker) typeParamScope(tparams []*types.TypeParam) *scope {
	s := newScope(c.global)
	for _, tp := range tparams {
		if tp.Obj != nil {
			s.objs[tp.Obj.Name] = tp.Obj
		}
	}
	return s
}

// resolveInterfaceFields resolves an interface's field types in a scope
// holding its type parameters, declares each field name (DeclField, unique
// within the interface -> E-NAME on a duplicate), records Info.Defs for the
// field identifier, and calls Named.SetFields.
func (c *checker) resolveInterfaceFields(d *ifaceDecl) {
	s := c.typeParamScope(d.named.TypeParams)
	fields := c.resolveFields(s, d.stmt.Fields)
	d.named.SetFields(fields)
}

// resolveFields resolves a list of field definitions into *Var values with
// FieldVar kind, enforcing field-name uniqueness. The OQ1 redundant-optional
// warning is tested on the written element type (resolveTypeIn), before the
// implicit "| null" of a "name?:" field is applied.
func (c *checker) resolveFields(s *scope, defs []*ast.FieldDefinition) []*types.Var {
	fieldScope := newScope(s)
	fields := make([]*types.Var, 0, len(defs))
	for _, fd := range defs {
		written := c.resolveTypeIn(s, fd.Type)
		t := written
		if fd.Optional {
			// OQ1: "name?: T | null" writes an already-optional element.
			if types.IsOptional(written) {
				c.warnf(fd.Type.Pos(), codeOptional, "redundant optional: %s is already optional", written)
			}
			if !types.CanBeOptional(types.NonOptional(written)) {
				c.errorf(fd.Type.Pos(), "E-TYPE", "%s cannot be optional", written)
				t = types.Typ[types.Invalid]
			} else {
				t = types.NewOptional(written)
			}
		}
		v := &types.Var{Name: fd.Name.Name, Kind: types.FieldVar, Type: t, Pos: fd.Name.NamePos, Decl: fd}
		c.info.Defs[fd.Name] = v
		if err := types.CheckDeclName(fd.Name.Name, types.DeclField); err != nil {
			c.errorf(fd.Name.NamePos, "E-NAME", "%s", err.Error())
			c.errDeclPos[filePos{c.file, fd.Name.NamePos}] = true
		} else if prev := fieldScope.objs[fd.Name.Name]; prev != nil {
			c.errorf(fd.Name.NamePos, "E-NAME", "field `%s` redeclared", fd.Name.Name)
			c.notef(prev.ObjectPos(), "E-NAME", "previous declaration of field `%s`", fd.Name.Name)
			continue
		}
		fieldScope.objs[fd.Name.Name] = v
		fields = append(fields, v)
	}
	return fields
}

// resolveAliasValue resolves the aliased type of a transparent alias, or the
// fields of a nominal object-type alias. A generic object-type alias is not
// supported in v1 (OQ3 -> E-UNSUPPORTED).
func (c *checker) resolveAliasValue(d *aliasDecl) {
	if d.named != nil {
		if len(d.named.TypeParams) > 0 {
			c.errorf(d.stmt.Name.NamePos, "E-UNSUPPORTED",
				"generic object-type alias is not supported")
		}
		s := c.typeParamScope(d.named.TypeParams)
		ot := d.stmt.Value.(*ast.ObjectType)
		fields := c.resolveFields(s, ot.Fields)
		d.named.SetFields(fields)
		return
	}
	s := c.typeParamScope(d.alias.TypeParams)
	d.alias.Type = c.resolveTypeIn(s, d.stmt.Value)
}

// resolveSignature resolves a function's receiver, type parameters,
// parameters and result into a types.Signature and sets *Func.Sig. The
// receiver and parameter *Vars are recorded in Info.Defs.
func (c *checker) resolveSignature(d *funcDecl) {
	tparams := c.typeParams(d.stmt.TypeParams)
	s := c.typeParamScope(tparams)
	sig := &types.Signature{TypeParams: tparams, Result: types.Typ[types.Void]}

	if d.stmt.Receiver != nil {
		sig.Recv = c.resolveParam(s, d.stmt.Receiver, types.RecvVar, 0)
	}
	sig.Params = make([]*types.Var, len(d.stmt.Parameters))
	for i, p := range d.stmt.Parameters {
		sig.Params[i] = c.resolveParam(s, p, types.ParamVar, i)
	}
	if d.stmt.ReturnType != nil {
		sig.Result = c.resolveTypeIn(s, d.stmt.ReturnType)
	}
	d.fn.Sig = sig
}

// resolveParam resolves one parameter or receiver into a *Var and records
// Info.Defs for its name identifier.
func (c *checker) resolveParam(s *scope, p *ast.Parameter, kind types.VarKind, index int) *types.Var {
	v := &types.Var{
		Name:  p.Name.Name,
		Kind:  kind,
		Type:  c.resolveTypeIn(s, p.Type),
		Pos:   p.Name.NamePos,
		Decl:  p,
		Index: index,
	}
	c.info.Defs[p.Name] = v
	return v
}

// attachMethod attaches a resolved receiver method to its receiver type: an
// interface *Named's Methods, or the Context-method table. A receiver that
// is not an interface or Context is E-TYPE (DESIGN.md §2.9). Non-method
// functions are skipped.
func (c *checker) attachMethod(d *funcDecl) {
	if d.stmt.Receiver == nil || d.fn.Sig == nil || d.fn.Sig.Recv == nil {
		return
	}
	recvType := d.fn.Sig.Recv.Type
	switch rt := recvType.(type) {
	case *types.Named:
		if rt.IsGeneric() || rt.IsInstance() {
			c.errorf(d.stmt.Receiver.Pos(), "E-TYPE",
				"receiver type %s may not be generic", rt)
			return
		}
		if m := rt.Method(d.fn.Name); m != nil {
			// The previous method may be declared in another module that
			// imports the same receiver type.
			c.errorf(d.fn.Pos, "E-NAME", "method `%s` redeclared on %s", d.fn.Name, rt.Name())
			c.notefIn(c.objFile(m), m.Pos, "E-NAME", "previous declaration of method `%s`", d.fn.Name)
			c.errDeclPos[filePos{c.file, d.fn.Pos}] = true
			return
		}
		rt.Methods = append(rt.Methods, d.fn)
	case *types.Basic:
		if rt.Kind != types.Context {
			c.errorf(d.stmt.Receiver.Pos(), "E-TYPE",
				"receiver type must be an interface or Context, got %s", recvType)
			return
		}
		if m := c.methods[d.fn.Name]; m != nil {
			c.errorf(d.fn.Pos, "E-NAME", "method `%s` redeclared on Context", d.fn.Name)
			c.notefIn(c.objFile(m), m.Pos, "E-NAME", "previous declaration of method `%s`", d.fn.Name)
			c.errDeclPos[filePos{c.file, d.fn.Pos}] = true
			return
		}
		c.methods[d.fn.Name] = d.fn
	default:
		c.errorf(d.stmt.Receiver.Pos(), "E-TYPE",
			"receiver type must be an interface or Context, got %s", recvType)
	}
}

// resolveGlobalType resolves a top-level let/const's declared type, when
// written, into Var.Type. The initializer (and type inference for an omitted
// annotation) is checked in pass 3; until then an un-annotated global's Type
// stays nil.
func (c *checker) resolveGlobalType(g *types.Var) {
	let, ok := g.Decl.(*ast.LetStatement)
	if !ok || let.Type == nil {
		return
	}
	g.Type = c.resolveType(let.Type)
}

// checkRequiredFieldCycles reports E-INIT for a cycle of non-optional
// interface fields (DESIGN.md §2.4): new must allocate the whole required
// tree, so a required cycle can never be constructed. Arrays and optionals
// break the edge (an array field starts empty, an optional starts null). A
// cycle can run through interfaces of several modules (an allowed import
// cycle), so each diagnostic names its interface's own file.
func (c *checker) checkRequiredFieldCycles() {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[*types.Named]int{}
	var stack []*types.Named
	var visit func(n *types.Named)
	visit = func(n *types.Named) {
		if color[n] == black {
			return
		}
		if color[n] == gray {
			c.errorfIn(c.objFile(n.Obj), n.Obj.Pos, "E-INIT",
				"cycle of required interface fields through %s; make one field optional", n.Name())
			for _, m := range stack {
				if m != n {
					c.notefIn(c.objFile(m.Obj), m.Obj.Pos, "E-INIT", "%s is on the required-field cycle", m.Name())
				}
			}
			return
		}
		color[n] = gray
		stack = append(stack, n)
		for _, f := range n.Fields() {
			if f.Optional {
				continue
			}
			if dep := requiredNamed(f.Type); dep != nil {
				visit(dep)
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
	}
	for _, d := range c.interfaces {
		visit(d.named)
	}
	for _, d := range c.aliases {
		if d.named != nil {
			visit(d.named)
		}
	}
}

// requiredNamed returns the interface *Named a non-optional field requires at
// construction, or nil. An optional or array type breaks the requirement, so
// it yields nil (an array of interfaces starts empty).
func requiredNamed(t types.Type) *types.Named {
	if n, ok := t.(*types.Named); ok {
		return n
	}
	return nil
}

// checkMethodCollisions runs the §2.9 method/field/builtin-member collision
// check for every interface: a method whose name is also a field (E-NAME
// "collides with a field") or a builtin member of the receiver type (E-NAME
// "collides with a builtin member"). Recovery keeps the field and drops the
// method, matching the scope "first wins" rule, so pass-3 selection never
// sees an ambiguous member.
func (c *checker) checkMethodCollisions() {
	for _, d := range c.interfaces {
		c.checkNamedMethodCollisions(d.named)
	}
	for _, d := range c.aliases {
		if d.named != nil {
			c.checkNamedMethodCollisions(d.named)
		}
	}
}

// cnameDecl is one entry of the checker's C-name collision table: what
// describes the user declaration ("interface f_logger", "function logger",
// "method User.a"), empty for a name generated code reserves; pos is the
// declaring identifier's position and file the file of its module.
type cnameDecl struct {
	what string
	file string
	pos  token.Position
}

// checkCNameCollisions is the authoritative cross-declaration C-name
// collision check (DESIGN.md §3.2; mirrors codegen's registerNames/nameTable,
// codegen/validate.go + codegen/cname.go, so the SAME programs are rejected
// just earlier). It keys the StructCName of every concrete interface/instance
// and the FuncCName of every function, method and function instance against
// the reserved generated names tl_globals and tl__init_globals, and reports
// E-NAME on the first collision.
//
// It is panic-safe because it iterates ONLY the pass-6 output lists
// Info.Interfaces/Info.Funcs/Info.FuncInstances, which assembleOutputLists
// has already filtered to concrete entries (so types.StructCName/FuncCName,
// which panic on a non-concrete type, are safe); an IsConcrete belt guards
// each key as defense in depth. A declaration that already produced a
// declaration-name E-NAME (1a) is skipped so it is not reported twice.
func (c *checker) checkCNameCollisions() {
	byName := map[string]cnameDecl{}
	// Reserved generated names (codegen reservedCNames, minus __tl_program:
	// no user C name can equal it, since __-prefixed names are already banned).
	byName["tl_globals"] = cnameDecl{}
	byName["tl__init_globals"] = cnameDecl{}

	add := func(cname string, cur cnameDecl) {
		prev, dup := byName[cname]
		if !dup {
			byName[cname] = cur
			return
		}
		if prev.what == "" {
			// Collision with a reserved generated name.
			c.errorfIn(cur.file, cur.pos, "E-NAME",
				"C name %s is reserved for generated code (%s at %s)", cname, cur.what, cur.pos)
			return
		}
		// Collision between two user declarations: report at the later
		// declaration, naming both in source order (mirrors codegen
		// errCollision). Two declarations of different modules order by module
		// and name each position with its file.
		first, second := prev, cur
		if c.cnameBefore(cur, prev) {
			first, second = cur, prev
		}
		at := func(d cnameDecl) string {
			if first.file == second.file {
				return d.pos.String()
			}
			return d.file + ":" + d.pos.String()
		}
		c.errorfIn(second.file, second.pos, "E-NAME",
			"C name collision: %s (%s at %s and %s at %s)", cname, first.what, at(first), second.what, at(second))
	}

	for _, n := range c.info.Interfaces {
		if !types.IsConcrete(n) {
			continue
		}
		d := cnameDecl{what: declWhat(n), file: c.objFile(n.Obj), pos: namedPos(n)}
		if c.errDeclPos[filePos{d.file, d.pos}] {
			continue
		}
		add(types.StructCName(n), d)
	}
	for _, f := range c.info.Funcs {
		if f.Sig == nil || !types.IsConcrete(f.Sig) {
			continue
		}
		d := cnameDecl{what: funcWhat(f), file: c.objFile(f), pos: f.Pos}
		if c.errDeclPos[filePos{d.file, d.pos}] {
			continue
		}
		add(types.FuncCName(f), d)
	}
	for _, f := range c.info.FuncInstances {
		if f.Sig == nil || !types.IsConcrete(f.Sig) {
			continue
		}
		d := cnameDecl{what: funcWhat(f), file: c.objFile(f), pos: f.Pos}
		if c.errDeclPos[filePos{d.file, d.pos}] {
			continue
		}
		add(types.FuncCName(f), d)
	}
	// Globals contribute their GlobalCName too (DESIGN-modules.md §5.4): a
	// cross-module g_ clash (two modules whose tag+name collide, or a tagged
	// global colliding with the reserved tl_globals shape) is rejected
	// E-NAME here. For a single-file program every tag is "", so g_<name> is
	// unique by CheckDeclName and this adds nothing new.
	for _, v := range c.info.Globals {
		d := cnameDecl{what: "global " + v.Name, file: c.objFile(v), pos: v.Pos}
		if c.errDeclPos[filePos{d.file, d.pos}] {
			continue
		}
		add(types.GlobalCName(v.Tag, v.Name), d)
	}
}

// cnameBefore reports whether declaration a precedes b in whole-program
// source order: by position within one file, and by module order (the
// dependency-first order CheckProgram received) across files. The
// single-file Check only ever compares positions.
func (c *checker) cnameBefore(a, b cnameDecl) bool {
	if a.file == b.file {
		return a.pos.Before(b.pos)
	}
	return c.moduleIndex(a.file) < c.moduleIndex(b.file)
}

// moduleIndex returns the position of the module with the given file in the
// whole-program module order, or -1 when no module has that file.
func (c *checker) moduleIndex(file string) int {
	for i, mc := range c.modules {
		if mc.mod.Prog.File == file {
			return i
		}
	}
	return -1
}

// declWhat describes an interface/type for a collision message, mirroring
// codegen namedWhat: "type X" for an object-type alias declaration, else
// "interface X".
func declWhat(n *types.Named) string {
	if _, ok := n.Decl.(*ast.TypeAliasStatement); ok {
		return "type " + n.String()
	}
	return "interface " + n.String()
}

// funcWhat describes a function/method for a collision message, mirroring
// codegen funcWhat.
func funcWhat(f *types.Func) string {
	if f.IsMethod() {
		return "method " + f.FullName()
	}
	return "function " + f.FullName()
}

// namedPos returns an interface's declaring identifier position (an instance
// inherits its origin's), guarding a nil Obj (mirrors codegen namedPos).
func namedPos(n *types.Named) token.Position {
	if n.Obj == nil {
		return token.Position{}
	}
	return n.Obj.Pos
}

// checkNamedMethodCollisions applies the collision check to one interface,
// dropping any colliding method from Named.Methods. A method may be declared
// in a module that imports the interface, so the error names the method's
// file and the field note the interface's.
func (c *checker) checkNamedMethodCollisions(n *types.Named) {
	kept := n.Methods[:0]
	for _, m := range n.Methods {
		if f := n.Field(m.Name); f != nil {
			c.errorfIn(c.objFile(m), m.Pos, "E-NAME", "method `%s` collides with a field", m.Name)
			c.notefIn(c.objFile(n.Obj), f.Pos, "E-NAME", "field `%s` declared here", m.Name)
			continue
		}
		if types.MemberOf(n, m.Name) != types.BuiltinInvalid {
			c.errorfIn(c.objFile(m), m.Pos, "E-NAME", "method `%s` collides with a builtin member", m.Name)
			continue
		}
		kept = append(kept, m)
	}
	n.Methods = kept
}
