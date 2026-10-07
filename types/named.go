package types

import (
	"fmt"

	"tlang/ast"
)

// Named is a user interface type: an "interface" declaration or a
// "type P = { ... }" declaration (DESIGN.md §2.3, §2.4). It maps to the C
// struct StructCName(n) and is used through a pointer (CType(n) == "tl_X*").
//
// A Named is one of:
//   - plain: not generic (TypeParams and TypeArgs nil, Origin nil);
//   - generic origin: TypeParams non-nil; never used as a value type
//     directly, only through instances;
//   - instance: Origin non-nil, TypeArgs non-nil (len == len(Origin.TypeParams)),
//     created only by InstanceCache.InstantiateNamed, which guarantees one
//     *Named per distinct (origin, type arguments). An instance whose
//     TypeArgs contain type parameters ("Page<T>" inside a generic body) is
//     non-concrete (IsConcrete reports false) and never reaches codegen.
//
// Fields of plain and origin types are set once by the checker with
// SetFields. Fields of an instance are computed lazily on first use of
// Fields (substituting the origin's field types), which lets the checker
// instantiate a generic interface before the origin's fields are resolved.
type Named struct {
	// Obj is the declaring type name. For an instance it is the origin's.
	Obj *TypeName
	// Decl is the declaring node: *ast.InterfaceStatement, or
	// *ast.TypeAliasStatement whose Value is an *ast.ObjectType. Instances
	// share the origin's Decl. Nil only for types built in tests.
	Decl ast.Node
	// TypeParams are the type parameters of a generic origin; nil otherwise.
	TypeParams []*TypeParam
	// TypeArgs are the type arguments of an instance; nil otherwise.
	TypeArgs []Type
	// Origin is the generic origin of an instance; nil otherwise.
	Origin *Named
	// Methods are the receiver methods declared on this type in declaration
	// order (plain types only: receivers cannot be generic in v1).
	Methods []*Func
	// Instances are the instances of a generic origin in creation order
	// (concrete and non-concrete); nil for other types.
	Instances []*Named

	fields   []*Var
	complete bool           // fields set (plain/origin)
	expanded bool           // fields computed (instance)
	cache    *InstanceCache // instance: cache used for lazy expansion
}

// NewNamed creates a plain (tparams == nil) or generic origin interface type
// and sets obj.Type to it when obj.Type is nil.
func NewNamed(obj *TypeName, decl ast.Node, tparams []*TypeParam) *Named {
	n := &Named{Obj: obj, Decl: decl, TypeParams: tparams}
	if obj != nil && obj.Type == nil {
		obj.Type = n
	}
	return n
}

// Name returns the declared name (without type arguments).
func (n *Named) Name() string {
	if n.Obj == nil {
		return "?"
	}
	return n.Obj.Name
}

// IsGeneric reports whether n is a generic origin.
func (n *Named) IsGeneric() bool { return len(n.TypeParams) > 0 }

// IsInstance reports whether n is an instance of a generic origin.
func (n *Named) IsInstance() bool { return n.Origin != nil }

// SetFields sets the fields of a plain or origin type, in declaration order,
// and marks it complete. It sets each field's Kind to FieldVar, Index to its
// position, and Optional to IsOptional(Type). It panics on an instance.
func (n *Named) SetFields(fields []*Var) {
	if n.Origin != nil {
		panic("types: SetFields called on instance " + n.String())
	}
	for i, f := range fields {
		f.Kind = FieldVar
		f.Index = i
		f.Optional = IsOptional(f.Type)
	}
	n.fields = fields
	n.complete = true
}

// Complete reports whether the fields are available: SetFields was called
// on a plain or origin type, or, for an instance, on its origin.
func (n *Named) Complete() bool {
	if n.Origin != nil {
		return n.Origin.complete
	}
	return n.complete
}

// Fields returns the fields in declaration order (C struct order). For an
// instance, the first call substitutes the origin's field types; the
// resulting fields are new *Var values (Origin = the origin's field) with
// the same names and indexes. It panics if called on an instance whose
// origin is not Complete (a checker ordering bug).
func (n *Named) Fields() []*Var {
	if n.Origin != nil && !n.expanded {
		n.expand()
	}
	return n.fields
}

func (n *Named) expand() {
	o := n.Origin
	if !o.complete {
		panic("types: fields of " + n.String() + " requested before its origin " + o.Name() + " was completed")
	}
	n.expanded = true
	fields := make([]*Var, len(o.fields))
	for i, f := range o.fields {
		t := n.cache.Subst(f.Type, o.TypeParams, n.TypeArgs)
		fields[i] = &Var{
			Name:     f.Name,
			Kind:     FieldVar,
			Type:     t,
			Pos:      f.Pos,
			Decl:     f.Decl,
			Index:    i,
			Optional: IsOptional(t),
			Origin:   f,
		}
	}
	n.fields = fields
}

// Field returns the field with the given name, or nil.
func (n *Named) Field(name string) *Var {
	for _, f := range n.Fields() {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// Method returns the receiver method with the given name, or nil.
func (n *Named) Method(name string) *Func {
	for _, m := range n.Methods {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// String returns "User", "Page<User>", or "Page<T>" for a generic origin.
func (n *Named) String() string {
	switch {
	case n.Origin != nil:
		return n.Name() + "<" + typeListString(n.TypeArgs) + ">"
	case len(n.TypeParams) > 0:
		args := make([]Type, len(n.TypeParams))
		for i, tp := range n.TypeParams {
			args[i] = tp
		}
		return n.Name() + "<" + typeListString(args) + ">"
	}
	return n.Name()
}

// GoString helps debugging (fmt %#v) without recursing into fields.
func (n *Named) GoString() string { return fmt.Sprintf("types.Named(%s)", n.String()) }

// tagPrefix returns the module-tag prefix of n's name fragment: the owning
// type name's Tag followed by "__" when the Tag is non-empty, else "" so an
// untagged (root/single-file) type mangles byte-for-byte as before
// (DESIGN-modules.md §5). The tag slots in after the fixed "tl_" prefix and
// before the user name; the base36 disambiguator in a tag never contains
// "__", so the scheme stays injective.
func tagPrefix(n *Named) string {
	if n.Obj == nil || n.Obj.Tag == "" {
		return ""
	}
	return n.Obj.Tag + "__"
}
