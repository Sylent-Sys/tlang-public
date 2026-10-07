package types

// Identical reports whether x and y are the same type: the same Basic kind,
// the same *Named (instances of one origin with identical type arguments
// also count, as a safety net for instances from different caches), the
// same *TypeParam, or Array/Optional/Signature types with identical
// components. Typing is nominal: two interfaces with the same fields are
// not identical.
func Identical(x, y Type) bool {
	if x == y {
		return true
	}
	if x == nil || y == nil {
		return false
	}
	switch x := x.(type) {
	case *Basic:
		y, ok := y.(*Basic)
		return ok && x.Kind == y.Kind
	case *Array:
		y, ok := y.(*Array)
		return ok && Identical(x.Elem, y.Elem)
	case *Optional:
		y, ok := y.(*Optional)
		return ok && Identical(x.Elem, y.Elem)
	case *Named:
		y, ok := y.(*Named)
		if !ok || x.Origin == nil || x.Origin != y.Origin || len(x.TypeArgs) != len(y.TypeArgs) {
			return false
		}
		for i := range x.TypeArgs {
			if !Identical(x.TypeArgs[i], y.TypeArgs[i]) {
				return false
			}
		}
		return true
	case *Signature:
		y, ok := y.(*Signature)
		if !ok || len(x.Params) != len(y.Params) || len(x.TypeParams) != len(y.TypeParams) ||
			(x.Recv == nil) != (y.Recv == nil) || !Identical(x.Result, y.Result) {
			return false
		}
		if x.Recv != nil && !Identical(x.Recv.Type, y.Recv.Type) {
			return false
		}
		for i := range x.Params {
			if !Identical(x.Params[i].Type, y.Params[i].Type) {
				return false
			}
		}
		return true
	}
	return false
}

// AssignableTo reports whether a value of type from can be used where type
// to is expected (assignment, argument, return, field value, array element):
//
//   - identical types;
//   - T to T | null (widening; codegen applies Info.Conversions);
//   - null (UntypedNull) to any T | null;
//   - an untyped integer constant to int32, int64, float64, or an optional
//     of these; an untyped float constant to float64 or float64 | null.
//     The value must also fit: check Representable for constants;
//   - Invalid to or from anything (suppresses follow-up errors).
//
// Nothing else converts implicitly: no numeric widening (int32 to int64),
// no T | null to T (narrow first), no structural interface matching, no
// array covariance.
func AssignableTo(from, to Type) bool {
	if from == nil || to == nil {
		return false
	}
	if IsInvalid(from) || IsInvalid(to) {
		return true
	}
	if Identical(from, to) {
		return true
	}
	if b, ok := from.(*Basic); ok {
		target := NonOptional(to)
		switch b.Kind {
		case UntypedInt:
			return IsBasic(target, Int32) || IsBasic(target, Int64) || IsBasic(target, Float64)
		case UntypedFloat:
			return IsBasic(target, Float64)
		case UntypedNull:
			return IsOptional(to)
		}
	}
	if o, ok := to.(*Optional); ok {
		return Identical(from, o.Elem)
	}
	return false
}

// IsBasic reports whether t is the Basic type of kind k.
func IsBasic(t Type, k BasicKind) bool {
	b, ok := t.(*Basic)
	return ok && b.Kind == k
}

// IsInvalid reports whether t is Typ[Invalid].
func IsInvalid(t Type) bool { return IsBasic(t, Invalid) }

// IsInteger reports whether t is int32, int64 or untyped int.
func IsInteger(t Type) bool {
	return IsBasic(t, Int32) || IsBasic(t, Int64) || IsBasic(t, UntypedInt)
}

// IsFloat reports whether t is float64 or untyped float.
func IsFloat(t Type) bool { return IsBasic(t, Float64) || IsBasic(t, UntypedFloat) }

// IsNumeric reports whether t is an integer or float type (typed or untyped).
func IsNumeric(t Type) bool { return IsInteger(t) || IsFloat(t) }

// IsString reports whether t is string.
func IsString(t Type) bool { return IsBasic(t, String) }

// IsBool reports whether t is bool.
func IsBool(t Type) bool { return IsBasic(t, Bool) }

// IsVoid reports whether t is void.
func IsVoid(t Type) bool { return IsBasic(t, Void) }

// IsUntyped reports whether t is untyped int, untyped float or null.
func IsUntyped(t Type) bool {
	return IsBasic(t, UntypedInt) || IsBasic(t, UntypedFloat) || IsBasic(t, UntypedNull)
}

// IsOptional reports whether t is T | null.
func IsOptional(t Type) bool {
	_, ok := t.(*Optional)
	return ok
}

// NonOptional returns T for T | null, and t itself otherwise.
func NonOptional(t Type) Type {
	if o, ok := t.(*Optional); ok {
		return o.Elem
	}
	return t
}

// IsPrimitiveOptional reports whether t is int32 | null, int64 | null,
// float64 | null or bool | null: the optionals represented as
// tlang_opt_* structs {bool has; T v;}, whose payload codegen reads with .v
// (DESIGN.md §2.5).
func IsPrimitiveOptional(t Type) bool {
	o, ok := t.(*Optional)
	if !ok {
		return false
	}
	return IsBasic(o.Elem, Int32) || IsBasic(o.Elem, Int64) || IsBasic(o.Elem, Float64) || IsBasic(o.Elem, Bool)
}

// IsReference reports whether values of t are C pointers with reference
// semantics, where == compares identity and NULL can represent null:
// interfaces, arrays, Context, Transaction, and optionals of interfaces and
// arrays. Strings (fat pointers by value), Error (by value), numbers and
// bools are not references. A type parameter is not (unknown until
// instantiated).
func IsReference(t Type) bool {
	switch t := t.(type) {
	case *Named, *Array:
		return true
	case *Basic:
		return t.Kind == Context || t.Kind == Transaction
	case *Optional:
		return IsReference(t.Elem)
	}
	return false
}

// HasRegion reports whether values of t can point into memory with a
// lifetime, so that the escape analysis tracks them (DESIGN.md §2.10):
// strings, interfaces, arrays, Error (its message), Context, Transaction,
// optionals of these, and type parameters (conservatively). Numbers, bools
// and void have no region.
func HasRegion(t Type) bool {
	switch t := t.(type) {
	case *Named, *Array, *TypeParam:
		return true
	case *Basic:
		switch t.Kind {
		case String, Error, Context, Transaction:
			return true
		}
	case *Optional:
		return HasRegion(t.Elem)
	}
	return false
}

// CanBeOptional reports whether T | null is a valid type: T is a number,
// bool, string, interface, array or type parameter; not void, Error,
// Context, Transaction, an optional, or an untyped type (DESIGN.md §2.5).
// Invalid is accepted to avoid follow-up errors.
func CanBeOptional(t Type) bool {
	switch t := t.(type) {
	case *Basic:
		switch t.Kind {
		case Int32, Int64, Float64, Bool, String, Invalid:
			return true
		}
		return false
	case *Named:
		return !t.IsGeneric()
	case *Array, *TypeParam:
		return true
	}
	return false
}

// ValidTypeArg reports whether t may be a type argument: a number, bool,
// string, interface (not an uninstantiated generic), array, optional or type
// parameter. void, Context, Error, Transaction and untyped types may not.
// Invalid is accepted to avoid follow-up errors.
func ValidTypeArg(t Type) bool {
	switch t := t.(type) {
	case *Basic:
		switch t.Kind {
		case Int32, Int64, Float64, Bool, String, Invalid:
			return true
		}
		return false
	case *Named:
		return !t.IsGeneric()
	case *Array, *Optional, *TypeParam:
		return true
	}
	return false
}

// HasZeroValue reports whether a variable of type t may be declared without
// an initializer (DESIGN.md §2.4): numbers (0), bool (false), string ("")
// and optionals (null). Interfaces, arrays, Error, Context, Transaction and
// type parameters need an initializer. Invalid is accepted.
func HasZeroValue(t Type) bool {
	switch t := t.(type) {
	case *Basic:
		switch t.Kind {
		case Int32, Int64, Float64, Bool, String, Invalid:
			return true
		}
	case *Optional:
		return true
	}
	return false
}

// Comparable reports whether == and != apply to two values of the same
// non-optional type t (DESIGN.md §2.3): numbers and bools (value), strings
// (bytes), interfaces, arrays, Context and Transaction (identity). Error,
// void and type parameters are not comparable. For operand pairs, including
// optionals and null, use CanCompare.
func Comparable(t Type) bool {
	switch t := t.(type) {
	case *Basic:
		switch t.Kind {
		case Int32, Int64, Float64, Bool, String, Context, Transaction, UntypedInt, UntypedFloat, Invalid:
			return true
		}
	case *Named, *Array:
		return true
	}
	return false
}

// Ordered reports whether < > <= >= apply to t: numbers only.
func Ordered(t Type) bool { return IsNumeric(t) || IsInvalid(t) }

// CanCompare reports whether x == y and x != y are allowed between operands
// of types x and y (DESIGN.md §2.3, §2.5), so that codegen can emit each
// allowed combination directly, without conversions:
//
//   - two values of one Comparable type: numbers and bools by value,
//     strings by bytes, interfaces, arrays, Context and Transaction by
//     identity;
//   - null with any optional: a presence test;
//   - two reference types with the same non-null type, optional or not
//     (User | null with User): pointer identity;
//   - an untyped numeric constant with a non-optional numeric type it is
//     assignable to (the checker then gives the constant that type), or
//     two untyped numeric constants.
//
// Everything else is rejected, in particular an optional number, bool or
// string against a non-null value (narrow first or use ??) and two such
// optionals against each other. Invalid is accepted.
func CanCompare(x, y Type) bool {
	switch {
	case IsInvalid(x) || IsInvalid(y):
		return true
	case IsBasic(x, UntypedNull):
		return IsOptional(y)
	case IsBasic(y, UntypedNull):
		return IsOptional(x)
	case IsUntyped(x) || IsUntyped(y):
		return untypedNumericPair(x, y)
	case IsReference(x) && IsReference(y):
		return Identical(NonOptional(x), NonOptional(y))
	}
	return Identical(x, y) && Comparable(x)
}

// CanOrder reports whether x < y (and > <= >=) is allowed: two numbers of
// the same type, or an untyped numeric constant with a numeric type it is
// assignable to, or two untyped numeric constants. Invalid is accepted.
func CanOrder(x, y Type) bool {
	switch {
	case IsInvalid(x) || IsInvalid(y):
		return true
	case IsUntyped(x) || IsUntyped(y):
		return untypedNumericPair(x, y)
	}
	return IsNumeric(x) && Identical(x, y)
}

func untypedNumericPair(x, y Type) bool {
	if !IsNumeric(x) || !IsNumeric(y) {
		return false
	}
	if IsUntyped(x) && IsUntyped(y) {
		return true
	}
	if IsUntyped(x) {
		return AssignableTo(x, y)
	}
	return AssignableTo(y, x)
}

// Default returns the type an untyped constant takes when no type is
// expected: int64 for untyped int, float64 for untyped float. Other types,
// including null, are returned unchanged.
func Default(t Type) Type {
	switch {
	case IsBasic(t, UntypedInt):
		return Typ[Int64]
	case IsBasic(t, UntypedFloat):
		return Typ[Float64]
	}
	return t
}

// IsConcrete reports whether t mentions no type parameter and no
// uninstantiated generic interface, so that it has a C representation.
// Untyped and Invalid types are not concrete either.
func IsConcrete(t Type) bool {
	switch t := t.(type) {
	case *Basic:
		return !IsUntyped(t) && t.Kind != Invalid
	case *Named:
		if t.IsGeneric() {
			return false
		}
		for _, a := range t.TypeArgs {
			if !IsConcrete(a) {
				return false
			}
		}
		return true
	case *Array:
		return IsConcrete(t.Elem)
	case *Optional:
		return IsConcrete(t.Elem)
	case *Signature:
		if len(t.TypeParams) > 0 || !IsConcrete(t.Result) {
			return false
		}
		if t.Recv != nil && !IsConcrete(t.Recv.Type) {
			return false
		}
		for _, p := range t.Params {
			if !IsConcrete(p.Type) {
				return false
			}
		}
		return true
	}
	return false
}
