package types

import "fmt"

// C naming (DESIGN.md §3.2) and C types (DESIGN.md §2.3). These functions
// accept only concrete types (IsConcrete); they panic on untyped types,
// type parameters, uninstantiated generics, signatures and Invalid, which
// never reach codegen in a program that type-checked.
//
// The scheme is injective as long as declared names follow CheckDeclName:
// no "__" inside names, and no interface named like a primitive mangle or
// starting with "arr_"/"opt_".

// Mangle returns the C name fragment of t:
//
//	int32 i32   int64 i64   float64 f64   bool bool   string str   void void
//	Context Context   Error Error   Transaction Transaction
//	interface User                   User
//	instance Page<User>              Page__User   (args joined by "__")
//	instance Map<str, User[]>        Map__str__arr_User
//	T[]                              arr_<T>
//	T | null                         opt_<T>
func Mangle(t Type) string {
	switch t := t.(type) {
	case *Basic:
		switch t.Kind {
		case Int32:
			return "i32"
		case Int64:
			return "i64"
		case Float64:
			return "f64"
		case Bool:
			return "bool"
		case String:
			return "str"
		case Void:
			return "void"
		case Context, Error, Transaction:
			return t.Name
		}
	case *Named:
		if t.IsGeneric() {
			break
		}
		s := tagPrefix(t) + t.Name()
		for _, a := range t.TypeArgs {
			s += "__" + Mangle(a)
		}
		return s
	case *Array:
		return "arr_" + Mangle(t.Elem)
	case *Optional:
		return "opt_" + Mangle(t.Elem)
	}
	panic(fmt.Sprintf("types.Mangle: %v has no C name", t))
}

// CType returns the C type used for values of t (DESIGN.md §2.3, §2.5):
//
//	int32 int32_t   int64 int64_t   float64 double   bool bool   void void
//	string                   tlang_string
//	Context                  tlang_ctx*
//	Error                    tlang_error
//	Transaction              tlang_tx*
//	interface User           tl_User*            (StructCName + "*")
//	instance Page<User>      tl_Page__User*
//	T[]                      tlang_slice_<Mangle(T)>*   (SliceCName + "*")
//	int32 | null             tlang_opt_i32   (likewise i64, f64, bool)
//	string | null            tlang_string    (data == NULL means null)
//	User | null, T[] | null  same pointer type as the non-null type (NULL)
//
// Array element storage is CType of the element (tl_User* for interfaces,
// values otherwise), so tlang_slice_User has items of type tl_User**.
func CType(t Type) string {
	switch t := t.(type) {
	case *Basic:
		switch t.Kind {
		case Int32:
			return "int32_t"
		case Int64:
			return "int64_t"
		case Float64:
			return "double"
		case Bool:
			return "bool"
		case String:
			return "tlang_string"
		case Void:
			return "void"
		case Context:
			return "tlang_ctx*"
		case Error:
			return "tlang_error"
		case Transaction:
			return "tlang_tx*"
		}
	case *Named:
		if !t.IsGeneric() {
			return StructCName(t) + "*"
		}
	case *Array:
		return SliceCName(t.Elem) + "*"
	case *Optional:
		switch e := t.Elem.(type) {
		case *Basic:
			switch e.Kind {
			case Int32, Int64, Float64, Bool:
				return "tlang_opt_" + Mangle(e)
			case String:
				return "tlang_string"
			}
		case *Named, *Array:
			return CType(e)
		}
	}
	panic(fmt.Sprintf("types.CType: %v has no C type", t))
}

// StructCName returns the C struct tag and typedef name of an interface:
// "tl_" + Mangle(n), e.g. tl_User, tl_Page__User.
func StructCName(n *Named) string { return "tl_" + Mangle(n) }

// SliceCName returns the C slice header type for arrays of elem:
// "tlang_slice_" + Mangle(elem), e.g. tlang_slice_i64, tlang_slice_User,
// tlang_slice_opt_str, tlang_slice_arr_i32.
func SliceCName(elem Type) string { return "tlang_slice_" + Mangle(elem) }

// PredefinedSlice reports whether the runtime header (tlang.h) already
// defines SliceCName(elem): elements int32, int64, float64, bool and
// string. Codegen emits the typedef for every other element type.
func PredefinedSlice(elem Type) bool {
	b, ok := elem.(*Basic)
	if !ok {
		return false
	}
	switch b.Kind {
	case Int32, Int64, Float64, Bool, String:
		return true
	}
	return false
}

// FuncCName returns the C function name (DESIGN.md §3.2):
//
//	function logger                   tl_f_logger
//	generic instance first<User>      tl_f_first__User   (args joined by "__")
//	method handle on Context          tl_m_Context__handle
//	method greet on User              tl_m_User__greet
func FuncCName(f *Func) string {
	if r := f.Recv(); r != nil {
		return "tl_m_" + Mangle(r.Type) + "__" + f.Name
	}
	s := "tl_f_" + f.Name
	if f.Tag != "" {
		s = "tl_f_" + f.Tag + "__" + f.Name
	}
	for _, a := range f.TypeArgs {
		s += "__" + Mangle(a)
	}
	return s
}

// FieldCName returns the C struct member of a field: "f_" + name.
func FieldCName(name string) string { return "f_" + name }

// GlobalCName returns the member of struct tl_globals for a global:
// "g_" + name when tag == "" (accessed as __fib->globals->g_name), else
// "g_" + tag + "__" + name. The module tag slots in after the fixed "g_"
// prefix and before the user name (DESIGN-modules.md §5); the empty tag
// reproduces today's name byte-for-byte.
func GlobalCName(tag, name string) string {
	if tag == "" {
		return "g_" + name
	}
	return "g_" + tag + "__" + name
}

// LocalCName returns the base C name of a local, parameter or receiver:
// "l_" + name. Codegen appends "__2", "__3", ... to shadowing declarations
// in nested scopes of the same function; this cannot collide with a user
// name because declared names may not contain "__" (CheckDeclName).
func LocalCName(name string) string { return "l_" + name }

// JSONParseCName returns the generated JSON parser name for an interface or
// array type: "tlj_parse_" + Mangle(t), e.g. tlj_parse_User,
// tlj_parse_arr_User.
func JSONParseCName(t Type) string { return "tlj_parse_" + Mangle(t) }

// JSONWriteCName returns the generated JSON writer name for an interface or
// array type: "tlj_write_" + Mangle(t).
func JSONWriteCName(t Type) string { return "tlj_write_" + Mangle(t) }
