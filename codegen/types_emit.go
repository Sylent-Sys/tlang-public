package codegen

import (
	"tlang/token"
	"tlang/types"
)

// Type collection and region A (codegen design §3.1 step 2, §3.3, §5.3,
// §5.4, plan D4). collectTypes runs the struct/slice fixed point of plan D4
// to the stable list the whole translation unit needs; emitTypes writes the
// forward typedefs, the slice defines and the struct bodies of region A;
// zeroValue produces the §5.4 canonical zero value of a C type.

// collectTypes computes g.structs and g.slices, the complete lists of
// concrete interface instances and non-predefined array types the unit
// refers to (plan D4). It calls Info.SliceTypes exactly once (the design
// forbids a second call, which would re-create instances) and then loops
// until no new instance and no new array type appears, because reading an
// instance's Fields can create further instances (plan D4, P3). Every
// discovered instance joins the collision table (§12 item 8). Finally it
// runs the pre-emission checks of plan D4 step 5: a field or array element
// whose concrete type is nil (§12 item 2) or void (§12 item 3).
func (g *generator) collectTypes() {
	// 1. The arrays the checker already knows, collected once; remember the
	//    mangle of each so later discovery never adds a duplicate.
	g.slices = g.info.SliceTypes()
	g.sliceSet = make(map[string]bool, len(g.slices))
	for _, a := range g.slices {
		g.sliceSet[g.mangle(a.Elem)] = true
	}

	// 2. Seed the struct list with the declared interfaces (already in the
	//    collision table from pass 1).
	list := make([]*types.Named, 0, len(g.info.Interfaces))
	inList := make(map[string]bool, len(g.info.Interfaces))
	for _, n := range g.info.Interfaces {
		list = append(list, n)
		inList[g.mangle(n)] = true
	}

	// 3. Fixed point: merge every concrete named instance (registering its C
	//    name) and collect the array types reachable through the fields of
	//    every struct now in the list. Reading Fields can create instances,
	//    so loop while the instance list grows or a new array appears.
	for {
		changed := false
		for _, n := range g.info.Instances.NamedInstances() {
			if n.Origin == nil || !types.IsConcrete(n) {
				continue
			}
			m := g.mangle(n)
			if inList[m] {
				continue
			}
			inList[m] = true
			list = append(list, n)
			g.addName(g.structName(n), namedWhat(n), namedPos(n))
			changed = true
		}
		for _, n := range list {
			for _, f := range n.Fields() {
				if g.collectArrays(f.Type) {
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}

	// 4. Deterministic struct-body order; the slice list stays in discovery
	//    order (SliceTypes order, then the extras appended by collectArrays).
	g.structs = types.SortByFieldDeps(list)

	// 5. Pre-emission checks (plan D4 step 5): reject nil and void field and
	//    array-element types before any C is written.
	for _, n := range g.structs {
		for _, f := range n.Fields() {
			g.checkFieldType(n, f)
		}
	}
	for _, a := range g.slices {
		if types.IsVoid(a.Elem) {
			g.fail(errVoidValue(token.Position{}, "array of void"))
		}
	}

	// 6. Record the instance count for the post-emission guard of plan D4
	//    step 6 (a concrete instance created after collection would miss its
	//    struct body).
	g.instCount = len(g.info.Instances.NamedInstances())
}

// collectArrays walks t with the checker's slice recursion (element first;
// through *Array and *Optional; not through *Named) and appends every
// concrete array type whose mangle is new to g.slices. It reports whether it
// added anything.
func (g *generator) collectArrays(t types.Type) bool {
	switch t := t.(type) {
	case *types.Array:
		added := g.collectArrays(t.Elem)
		if types.IsConcrete(t.Elem) {
			if m := g.mangle(t.Elem); !g.sliceSet[m] {
				g.sliceSet[m] = true
				g.slices = append(g.slices, t)
				added = true
			}
		}
		return added
	case *types.Optional:
		return g.collectArrays(t.Elem)
	}
	return false
}

// checkFieldType rejects a field whose concrete type is nil (§12 item 2) or
// void (§12 item 3), before region A is written.
func (g *generator) checkFieldType(n *types.Named, f *types.Var) {
	if f.Type == nil {
		g.fail(errExprNoType(f.Pos))
	}
	if types.IsVoid(f.Type) {
		g.fail(errVoidValue(f.Pos, "field "+n.Name()+"."+f.Name))
	}
}

// emitTypes writes region A (codegen design §3.3): every interface forward
// typedef first, then the slice defines of the non-predefined element types
// in g.slices order (no trailing semicolon, §13 rule 8), then the struct
// bodies in g.structs order. An empty region writes nothing.
func (g *generator) emitTypes() {
	w := g.section(secTypes)
	for _, n := range g.structs {
		name := g.structName(n)
		w.linef("typedef struct %s %s;", name, name)
	}
	for _, a := range g.slices {
		if types.PredefinedSlice(a.Elem) {
			continue
		}
		w.linef("TLANG_SLICE_DEFINE(%s, %s)", g.mangle(a.Elem), g.ctype(a.Elem))
	}
	for _, n := range g.structs {
		w.gap()
		g.emitStructBody(w, n)
	}
}

// emitStructBody writes one struct body: one member per concrete field, or
// the single placeholder member "char __empty;" for an empty interface (ISO
// C forbids an empty struct, §13 rule 9).
func (g *generator) emitStructBody(w *cwriter, n *types.Named) {
	w.open("struct " + g.structName(n))
	fields := n.Fields()
	if len(fields) == 0 {
		w.line("char __empty;")
	}
	for _, f := range fields {
		w.linef("%s %s;", g.ctype(f.Type), fieldName(f.Name))
	}
	w.close(";")
}

// zeroValue returns the canonical zero value of the C type of the concrete
// type t (codegen design §5.4, plan D4 fallback part): the expression a
// may-fail function returns on failure, a missing-return fallback returns,
// and an uninitialized let is assigned. It never emits a GNU "{}" empty
// initializer. A void type has no value (the empty string): callers reject a
// void value as §12 item 3 before asking for its zero.
func (g *generator) zeroValue(t types.Type) string {
	g.requireConcrete(t, "zero value")
	if b, ok := t.(*types.Basic); ok {
		switch b.Kind {
		case types.Int32, types.Int64:
			return "0"
		case types.Float64:
			return "0.0"
		case types.Bool:
			return "false"
		case types.String:
			return "(tlang_string){0}"
		case types.Error:
			return "(tlang_error){0}"
		case types.Void:
			return ""
		case types.Context, types.Transaction:
			return "NULL"
		}
	}
	if types.IsPrimitiveOptional(t) {
		// tlang_opt_i32/i64/f64/bool by value: has == false.
		return "(" + g.ctype(t) + "){0}"
	}
	// Interface pointer, array (slice pointer), and reference optionals
	// (string|null reuses tlang_string, interface|null and array|null reuse
	// the pointer C type): the null representation is NULL or {0}.
	switch nonOpt := types.NonOptional(t).(type) {
	case *types.Basic:
		if nonOpt.Kind == types.String {
			return "(tlang_string){0}"
		}
	}
	return "NULL"
}
