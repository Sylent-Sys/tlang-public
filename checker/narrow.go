package checker

import (
	"tlang/ast"
	"tlang/types"
)

// narrow.go is the null-narrowing engine of DESIGN.md §2.5. It tracks, at
// each program point, the set of narrowable bindings (a local, parameter or
// receiver *Var of optional type) known to be non-null, and threads that set
// through the body walker as an immutable value so branches get independent
// copies.

// factSet is an immutable set of bindings known to be non-null at a program
// point. It is treated as copy-on-write: with returns a new set, never
// mutating the receiver, so a set captured by one branch is unaffected by
// another.
type factSet map[*types.Var]bool

// has reports whether v is known non-null in f.
func (f factSet) has(v *types.Var) bool { return f[v] }

// with returns a copy of f with each v in add marked non-null. It returns f
// unchanged when add is empty.
func (f factSet) with(add ...*types.Var) factSet {
	if len(add) == 0 {
		return f
	}
	out := make(factSet, len(f)+len(add))
	for k := range f {
		out[k] = true
	}
	for _, v := range add {
		if v != nil {
			out[v] = true
		}
	}
	return out
}

// without returns a copy of f with each v in drop removed. It returns f
// unchanged when drop is empty or none of the bindings are present.
func (f factSet) without(drop map[*types.Var]bool) factSet {
	if len(drop) == 0 || len(f) == 0 {
		return f
	}
	changed := false
	for v := range drop {
		if f[v] {
			changed = true
			break
		}
	}
	if !changed {
		return f
	}
	out := make(factSet, len(f))
	for k := range f {
		if !drop[k] {
			out[k] = true
		}
	}
	return out
}

// posRole classifies the position a value expression occupies, so that the
// four roles that keep the optional type (and are not marked Info.Narrowed)
// can suppress narrowing of an otherwise-narrowed identifier (DESIGN.md
// §2.5 / the Info.Narrowed doc).
type posRole int

const (
	// roleValue is an ordinary value position: narrowing applies.
	roleValue posRole = iota
	// roleAssignTarget is the target of an assignment: not narrowed.
	roleAssignTarget
	// roleNullCompare is an operand of == or != against null: not narrowed.
	roleNullCompare
	// roleNullishLeft is the left operand of ??: not narrowed.
	roleNullishLeft
	// rolePostfixBang is the operand of a postfix !: not narrowed.
	rolePostfixBang
)

// narrowable reports whether obj is a binding that may be narrowed: a local,
// parameter or receiver *Var whose type is optional. Globals and fields are
// never narrowed (DESIGN.md §2.5: copy to a local first).
func narrowable(obj types.Object) (*types.Var, bool) {
	v, ok := obj.(*types.Var)
	if !ok || !types.IsOptional(v.Type) {
		return nil, false
	}
	switch v.Kind {
	case types.LocalVar, types.ParamVar, types.RecvVar:
		return v, true
	}
	return nil, false
}

// facts is the pair of narrowing deltas a condition produces: whenTrue holds
// when the condition is true, whenFalse when it is false. Only the bindings
// learned non-null are listed; each is relative to the ambient fact set.
type condFacts struct {
	whenTrue  []*types.Var
	whenFalse []*types.Var
}

// condFactsOf derives the narrowing deltas of a boolean condition, resolving
// the narrowable binding of a plain identifier compared with null and
// composing && / || / ! structurally. It does not type-check cond (the
// caller does that); it only reads the already-resolved shape.
func (c *checker) condFactsOf(cond ast.Expression, sc *scope) condFacts {
	switch e := cond.(type) {
	case *ast.InfixExpression:
		switch e.Operator {
		case "==":
			if v := c.nullCompareVar(e, sc); v != nil {
				return condFacts{whenFalse: []*types.Var{v}}
			}
		case "!=":
			if v := c.nullCompareVar(e, sc); v != nil {
				return condFacts{whenTrue: []*types.Var{v}}
			}
		case "&&":
			l := c.condFactsOf(e.Left, sc)
			r := c.condFactsOf(e.Right, sc)
			// whenTrue: both held; whenFalse learns nothing combinable.
			return condFacts{whenTrue: append(append([]*types.Var{}, l.whenTrue...), r.whenTrue...)}
		case "||":
			l := c.condFactsOf(e.Left, sc)
			r := c.condFactsOf(e.Right, sc)
			// whenFalse: both were false; whenTrue learns nothing combinable.
			return condFacts{whenFalse: append(append([]*types.Var{}, l.whenFalse...), r.whenFalse...)}
		}
	case *ast.PrefixExpression:
		if e.Operator == "!" {
			inner := c.condFactsOf(e.Right, sc)
			return condFacts{whenTrue: inner.whenFalse, whenFalse: inner.whenTrue}
		}
	}
	return condFacts{}
}

// nullCompareVar returns the narrowable binding of an "x == null" or
// "x != null" comparison (either operand order), or nil when neither operand
// is a plain narrowable identifier compared with null.
func (c *checker) nullCompareVar(e *ast.InfixExpression, sc *scope) *types.Var {
	if v := c.narrowIdentAgainstNull(e.Left, e.Right, sc); v != nil {
		return v
	}
	return c.narrowIdentAgainstNull(e.Right, e.Left, sc)
}

// narrowIdentAgainstNull returns the narrowable binding id denotes when id is
// a plain identifier and other is the null literal, else nil.
func (c *checker) narrowIdentAgainstNull(id, other ast.Expression, sc *scope) *types.Var {
	ident, ok := id.(*ast.Identifier)
	if !ok {
		return nil
	}
	if _, ok := other.(*ast.NullLiteral); !ok {
		return nil
	}
	if v, ok := narrowable(sc.lookup(ident.Name)); ok {
		return v
	}
	return nil
}

// assignedVars returns the set of narrowable bindings assigned anywhere
// inside root. Narrowing is cancelled flow-insensitively for any binding
// assigned within a narrowed region (DESIGN.md §2.5), so the walker scans the
// region once with this before entering it.
func (c *checker) assignedVars(root ast.Node, sc *scope) map[*types.Var]bool {
	out := map[*types.Var]bool{}
	if root == nil {
		return out
	}
	ast.Inspect(root, func(n ast.Node) bool {
		asg, ok := n.(*ast.AssignmentExpression)
		if !ok {
			return true
		}
		if id, ok := asg.Target.(*ast.Identifier); ok {
			if v, ok := narrowable(sc.lookup(id.Name)); ok {
				out[v] = true
			}
		}
		return true
	})
	return out
}

// dropAssigned returns the subset of vs not present in assigned, so a
// binding the narrowed region reassigns is not re-added as a narrowing fact
// (flow-insensitive invalidation, DESIGN.md §2.5).
func dropAssigned(vs []*types.Var, assigned map[*types.Var]bool) []*types.Var {
	if len(assigned) == 0 {
		return vs
	}
	out := vs[:0:0]
	for _, v := range vs {
		if !assigned[v] {
			out = append(out, v)
		}
	}
	return out
}

// alwaysExits reports whether every path through s ends in a terminator
// (return/throw/break/continue), so that code after an "if (x == null) { s }"
// runs with x known non-null (DESIGN.md §2.5). It is a conservative
// structural check: a block exits when its last reachable statement exits;
// an if exits when it has an else and both branches exit.
func alwaysExits(s ast.Statement) bool {
	switch s := s.(type) {
	case *ast.ReturnStatement, *ast.ThrowStatement, *ast.BreakStatement, *ast.ContinueStatement:
		return true
	case *ast.BlockStatement:
		if len(s.Statements) == 0 {
			return false
		}
		return alwaysExits(s.Statements[len(s.Statements)-1])
	case *ast.IfStatement:
		return s.Alternative != nil && alwaysExits(s.Consequence) && alwaysExits(s.Alternative)
	}
	return false
}
