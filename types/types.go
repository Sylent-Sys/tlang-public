// Package types is the semantic model of TLang, shared by the checker (which
// builds it) and the C code generator (which only reads it): types, objects
// (declared entities), the generic instantiation cache, C name mangling
// (DESIGN.md §3.2) and C types (§2.3), the builtin tables (§2.6-§2.11), the
// region lattice (§2.10), and Info, the checker's complete output.
//
// # Identity
//
// Typing is nominal (DESIGN.md §2.3). Basic types are the singletons in Typ.
// Every interface declaration is one *Named, and every distinct generic
// instance (Page<User>) is one canonical *Named created by an
// InstanceCache, so *Named and *Func values can be compared with == and
// used as map keys. Array and Optional types are structural and not
// canonical: compare them with Identical, and use Mangle(t) as a map key
// when deduplicating concrete types.
//
// # Builtin types
//
// Context, Error and Transaction are Basic kinds (shared singletons), not
// *Named: they have no user-visible fields, only builtin members
// (BuiltinID). User receiver methods on Context are *Func objects whose
// Sig.Recv has type Typ[Context]; they are not attached to the shared type
// (the checker keeps its own lookup table for them).
package types

import "strings"

// Type is a TLang type. The concrete types are *Basic, *Named, *Array,
// *Optional, *TypeParam and *Signature.
type Type interface {
	// String returns the type in TLang syntax, for diagnostics:
	// "int64", "Page<User>", "(string | null)[]", "untyped int".
	String() string
	isType()
}

// BasicKind identifies a predeclared type.
type BasicKind int

const (
	// Invalid is the type of erroneous expressions. Predicates treat it
	// leniently (AssignableTo returns true) so one error does not cascade.
	Invalid BasicKind = iota
	// Int32 is int32 (C int32_t), wrapping arithmetic.
	Int32
	// Int64 is int64 (C int64_t), wrapping arithmetic.
	Int64
	// Float64 is float64 (C double).
	Float64
	// Bool is bool.
	Bool
	// String is string (C tlang_string, a non-owning slice).
	String
	// Void is the result type of functions that return nothing.
	Void
	// Context is the builtin request context (C tlang_ctx*), a reference.
	Context
	// Error is the builtin error value (C tlang_error, by value) with
	// members message: string and status: int32.
	Error
	// Transaction is the builtin transaction handle (C tlang_tx*), only
	// available inside db.transaction.
	Transaction
	// UntypedInt is the type of integer constants before they take the
	// expected type (default int64).
	UntypedInt
	// UntypedFloat is the type of float constants (default float64).
	UntypedFloat
	// UntypedNull is the type of the null literal before it takes the
	// expected optional type. It has no default type.
	UntypedNull
)

// Basic is a predeclared type. Use the singletons in Typ; never allocate a
// Basic.
type Basic struct {
	// Kind is the predeclared type.
	Kind BasicKind
	// Name is the TLang spelling ("int64", "Context", "untyped int",
	// "null", "invalid type").
	Name string
}

// Typ holds the Basic singletons, indexed by kind: Typ[Int64] is int64.
var Typ = [...]*Basic{
	Invalid:      {Invalid, "invalid type"},
	Int32:        {Int32, "int32"},
	Int64:        {Int64, "int64"},
	Float64:      {Float64, "float64"},
	Bool:         {Bool, "bool"},
	String:       {String, "string"},
	Void:         {Void, "void"},
	Context:      {Context, "Context"},
	Error:        {Error, "Error"},
	Transaction:  {Transaction, "Transaction"},
	UntypedInt:   {UntypedInt, "untyped int"},
	UntypedFloat: {UntypedFloat, "untyped float"},
	UntypedNull:  {UntypedNull, "null"},
}

// String returns Name.
func (b *Basic) String() string { return b.Name }

// Array is T[], a reference to a slice header {items, len, cap}
// (DESIGN.md §2.6). Interface elements are stored as pointers, primitive and
// string elements by value.
type Array struct {
	// Elem is the element type.
	Elem Type
}

// NewArray returns the array type with element type elem.
func NewArray(elem Type) *Array { return &Array{Elem: elem} }

// String returns "T[]", or "(T | null)[]" for optional elements.
func (a *Array) String() string {
	if _, ok := a.Elem.(*Optional); ok {
		return "(" + typeString(a.Elem) + ")[]"
	}
	return typeString(a.Elem) + "[]"
}

// Optional is T | null (DESIGN.md §2.5). Elem is never itself an *Optional
// (NewOptional collapses "(T | null) | null" to "T | null"). Valid element
// types are reported by CanBeOptional.
type Optional struct {
	// Elem is the non-null type T.
	Elem Type
}

// NewOptional returns elem | null. If elem is already optional it is
// returned unchanged, so substituting T := U | null into T | null yields
// U | null.
func NewOptional(elem Type) *Optional {
	if o, ok := elem.(*Optional); ok {
		return o
	}
	return &Optional{Elem: elem}
}

// String returns "T | null".
func (o *Optional) String() string { return typeString(o.Elem) + " | null" }

// TypeParam is a type parameter of a generic function or interface.
// Type parameters are unconstrained (DESIGN.md §2.9). Each declaration
// creates distinct *TypeParam values; identity is by pointer.
type TypeParam struct {
	// Obj is the type parameter's name object (Obj.Type == this).
	Obj *TypeName
	// Index is the position in the declaring type-parameter list.
	Index int
}

// NewTypeParam creates a type parameter and sets obj.Type to it when
// obj.Type is nil.
func NewTypeParam(obj *TypeName, index int) *TypeParam {
	tp := &TypeParam{Obj: obj, Index: index}
	if obj != nil && obj.Type == nil {
		obj.Type = tp
	}
	return tp
}

// String returns the parameter name.
func (t *TypeParam) String() string {
	if t.Obj == nil {
		return "?"
	}
	return t.Obj.Name
}

// Signature is the type of a function or method. Functions are not values
// in TLang, so a Signature appears only in Func.Sig (and as the recorded
// type of nothing in Info.Types).
type Signature struct {
	// Recv is the receiver of a method (Kind RecvVar), nil for functions.
	Recv *Var
	// TypeParams are the type parameters of a generic function origin; nil
	// for non-generic functions and for instances.
	TypeParams []*TypeParam
	// Params are the parameters (Kind ParamVar) in order.
	Params []*Var
	// Result is the result type: Typ[Void] when the function returns
	// nothing. Never nil.
	Result Type
}

// String returns "fn<T>(xs: T[]): T | null" or "fn (u: User) (x: int32): void".
func (s *Signature) String() string {
	var b strings.Builder
	b.WriteString("fn")
	if s.Recv != nil {
		b.WriteString(" (" + s.Recv.Name + ": " + typeString(s.Recv.Type) + ") ")
	}
	if len(s.TypeParams) > 0 {
		b.WriteString("<")
		for i, tp := range s.TypeParams {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(tp.String())
		}
		b.WriteString(">")
	}
	b.WriteString("(")
	for i, p := range s.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.Name + ": " + typeString(p.Type))
	}
	b.WriteString("): ")
	b.WriteString(typeString(s.Result))
	return b.String()
}

func (*Basic) isType()     {}
func (*Named) isType()     {}
func (*Array) isType()     {}
func (*Optional) isType()  {}
func (*TypeParam) isType() {}
func (*Signature) isType() {}

func typeString(t Type) string {
	if t == nil {
		return "<nil>"
	}
	return t.String()
}

func typeListString(list []Type) string {
	parts := make([]string, len(list))
	for i, t := range list {
		parts[i] = typeString(t)
	}
	return strings.Join(parts, ", ")
}
