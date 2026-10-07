package types

import "fmt"

// Infer computes the type arguments of a generic call (DESIGN.md §2.9):
// explicit holds the explicitly written type arguments (a prefix of
// tparams, usually all or none), params the declared parameter types
// (mentioning tparams), args the argument types in Info.Types form before
// defaulting (untyped constants and null allowed). It unifies each
// parameter with its argument structurally:
//
//   - T with a typed argument A binds T := A (a second, different binding is
//     an error);
//   - X[] with A[], and G<X...> with G<A...> of the same origin, unify their
//     components;
//   - X | null with A | null unifies X with A; with a non-null A it unifies
//     X with A (the argument will be widened); with null it learns nothing.
//
// Typed arguments are used first; a parameter still unbound afterwards is
// bound from an untyped constant argument through Default (id(5) gives
// T := int64). Pairs beyond min(len(params), len(args)) are ignored (the
// checker reports arity). When explicit covers every type parameter, it is
// returned as is without looking at the arguments. It returns one type per
// type parameter, or an error for too many explicit arguments, conflicting
// bindings, or a type parameter that no argument determines. The caller
// still checks ValidTypeArg and assignability of every argument after
// substitution.
func Infer(tparams []*TypeParam, explicit []Type, params, args []Type) ([]Type, error) {
	if len(explicit) > len(tparams) {
		return nil, fmt.Errorf("got %d type arguments, want %d", len(explicit), len(tparams))
	}
	if len(explicit) == len(tparams) {
		// Fully explicit: nothing to infer; argument mismatches are
		// assignability errors, reported by the caller.
		return append([]Type(nil), explicit...), nil
	}
	u := unifier{tparams: tparams, bound: make([]Type, len(tparams))}
	copy(u.bound, explicit)
	n := min(len(params), len(args))
	for pass := 0; pass < 2; pass++ {
		u.untypedPass = pass == 1
		for i := 0; i < n; i++ {
			if err := u.unify(params[i], args[i]); err != nil {
				return nil, err
			}
		}
	}
	for i, b := range u.bound {
		if b == nil {
			return nil, fmt.Errorf("cannot infer %s", tparams[i])
		}
	}
	return u.bound, nil
}

type unifier struct {
	tparams     []*TypeParam
	bound       []Type
	untypedPass bool
}

func (u *unifier) index(t *TypeParam) int {
	for i, tp := range u.tparams {
		if tp == t {
			return i
		}
	}
	return -1
}

func (u *unifier) unify(p, a Type) error {
	if a == nil || IsInvalid(a) || IsBasic(a, UntypedNull) {
		return nil
	}
	switch p := p.(type) {
	case *TypeParam:
		i := u.index(p)
		if i < 0 {
			return nil
		}
		if IsUntyped(a) {
			if u.untypedPass && u.bound[i] == nil {
				u.bound[i] = Default(a)
			}
			return nil
		}
		if u.untypedPass {
			return nil
		}
		if u.bound[i] == nil {
			u.bound[i] = a
			return nil
		}
		if !Identical(u.bound[i], a) {
			return fmt.Errorf("type parameter %s inferred as both %s and %s", p, u.bound[i], a)
		}
	case *Array:
		if a, ok := a.(*Array); ok {
			return u.unify(p.Elem, a.Elem)
		}
	case *Optional:
		if a, ok := a.(*Optional); ok {
			return u.unify(p.Elem, a.Elem)
		}
		return u.unify(p.Elem, a)
	case *Named:
		if a, ok := a.(*Named); ok && p.Origin != nil && p.Origin == a.Origin {
			for i := range p.TypeArgs {
				if err := u.unify(p.TypeArgs[i], a.TypeArgs[i]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// IsDBScalar reports whether t can be a database argument or the type of a
// row field mapped from a result column (DESIGN.md §2.11): int32, int64,
// float64, bool, string, or an optional of these (null maps to SQL NULL).
func IsDBScalar(t Type) bool {
	switch b := NonOptional(t).(type) {
	case *Basic:
		switch b.Kind {
		case Int32, Int64, Float64, Bool, String:
			return true
		}
	}
	return false
}

// JSONEncodable reports whether values of the concrete type t can be parsed
// from and written as JSON (DESIGN.md §2.12): numbers, bool, string,
// interfaces whose fields are all encodable, arrays of encodable elements,
// and optionals of these. Error, Context, Transaction, void, type
// parameters, uninstantiated generics and untyped types are not. Recursive
// interfaces are fine (a cycle is assumed encodable while it is being
// checked).
func JSONEncodable(t Type) bool { return jsonEncodable(t, map[*Named]bool{}) }

func jsonEncodable(t Type, visiting map[*Named]bool) bool {
	switch t := t.(type) {
	case *Basic:
		switch t.Kind {
		case Int32, Int64, Float64, Bool, String:
			return true
		}
	case *Optional:
		return jsonEncodable(t.Elem, visiting)
	case *Array:
		return jsonEncodable(t.Elem, visiting)
	case *Named:
		if !IsConcrete(t) || !t.Complete() {
			return false
		}
		if visiting[t] {
			return true
		}
		visiting[t] = true
		for _, f := range t.Fields() {
			if !jsonEncodable(f.Type, visiting) {
				return false
			}
		}
		return true
	}
	return false
}
