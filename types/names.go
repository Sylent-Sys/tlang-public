package types

import (
	"fmt"
	"strings"
)

// DeclKind says what a declared name will name, for CheckDeclName.
type DeclKind int

const (
	// DeclValue is a variable, parameter, receiver, function or method.
	DeclValue DeclKind = iota
	// DeclType is an interface, object type, type alias or type parameter.
	DeclType
	// DeclField is an interface field.
	DeclField
)

// reservedTypeNames would collide with Mangle output or universe types.
var reservedTypeNames = map[string]bool{
	"i32": true, "i64": true, "f64": true, "str": true, "bool": true, "void": true,
	"int32": true, "int64": true, "float64": true, "string": true,
	"Context": true, "Error": true, "Transaction": true,
}

// CheckDeclName reports why name cannot be declared as kind, or nil. It
// keeps C names collision-free (see Mangle and LocalCName):
//
//   - no declared name may start with "__" (also rejected by the lexer,
//     DESIGN.md §2.1);
//   - values and types may not contain "__" anywhere (it separates mangled
//     parts: tl_f_first__User, tl_m_User__greet, l_x__2);
//   - type names may not be a universe type name or a primitive mangle (i32
//     i64 f64 str bool void int32 int64 float64 string Context Error
//     Transaction), may not start with "arr_" or "opt_", may not be the
//     reserved generated names "globals" or "_init_globals", and may not
//     start with "tl_" (all reserved for generated C, DESIGN.md §3.2).
//
// Field names only need to avoid the "__" prefix (they become f_<name>
// inside their own struct). Redeclaration and shadowing rules are the
// checker's.
func CheckDeclName(name string, kind DeclKind) error {
	if strings.HasPrefix(name, "__") {
		return fmt.Errorf("names starting with \"__\" are reserved for the compiler: %s", name)
	}
	if kind == DeclField {
		return nil
	}
	if strings.Contains(name, "__") {
		return fmt.Errorf("names containing \"__\" are reserved for the compiler: %s", name)
	}
	if kind == DeclType {
		if reservedTypeNames[name] {
			return fmt.Errorf("%s is a reserved type name", name)
		}
		if strings.HasPrefix(name, "arr_") || strings.HasPrefix(name, "opt_") {
			return fmt.Errorf("type names starting with \"arr_\" or \"opt_\" are reserved: %s", name)
		}
		// Reserved generated C shapes (DESIGN.md §3.2): a user interface or
		// type named "globals"/"_init_globals" would mangle to the tl_globals
		// struct and the tl__init_globals initializer that generated code
		// defines, and a type name starting "tl_" (also tl_f_/tl_m_) collides
		// with the generated prefix family. Only DeclType reaches here: a value
		// named "globals"/"tl_x" mangles to g_.../tl_f_..., never to these
		// reserved names, so values stay legal.
		if name == "globals" || name == "_init_globals" {
			return fmt.Errorf("%q is reserved for generated code", name)
		}
		if strings.HasPrefix(name, "tl_") {
			return fmt.Errorf("type names starting with \"tl_\" are reserved for generated code: %s", name)
		}
	}
	return nil
}
