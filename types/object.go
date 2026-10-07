package types

import (
	"tlang/ast"
	"tlang/token"
)

// Object is a named entity: *Var, *Func, *TypeName or *Builtin. Info.Defs
// and Info.Uses map identifiers to objects.
type Object interface {
	// ObjectName returns the declared name.
	ObjectName() string
	// ObjectType returns the object's type: a variable's type, a function's
	// *Signature, the type a TypeName denotes; nil for a *Builtin.
	ObjectType() Type
	// ObjectPos returns the position of the declaring identifier; the zero
	// Position for universe objects and instance copies.
	ObjectPos() token.Position
	// String describes the object for diagnostics ("var x int64",
	// "func first<T>", "type User", "builtin db").
	String() string
	isObject()
}

// VarKind classifies variables.
type VarKind int

const (
	// LocalVar is a block-scoped variable: a let/const in a body, a for-of
	// loop variable, a catch binding, or the transaction handle.
	LocalVar VarKind = iota + 1
	// ParamVar is a function or method parameter.
	ParamVar
	// RecvVar is a method receiver.
	RecvVar
	// GlobalVar is a top-level let/const, stored per scheduler in
	// struct tl_globals (DESIGN.md §2.10).
	GlobalVar
	// FieldVar is an interface field.
	FieldVar
)

// String returns "local", "param", "receiver", "global" or "field".
func (k VarKind) String() string {
	switch k {
	case LocalVar:
		return "local"
	case ParamVar:
		return "param"
	case RecvVar:
		return "receiver"
	case GlobalVar:
		return "global"
	case FieldVar:
		return "field"
	}
	return "var?"
}

// Var is a variable, parameter, receiver or interface field.
type Var struct {
	// Name is the declared name.
	Name string
	// Kind classifies the variable.
	Kind VarKind
	// IsConst reports that the binding cannot be reassigned: const
	// declarations and const for-of variables (fields of a const object stay
	// mutable, DESIGN.md §2.4). The transaction handle is also const.
	IsConst bool
	// Type is the declared or inferred type (after defaulting untyped
	// constants: "let n = 1" is int64). Inside a generic body it may
	// mention the function's type parameters (see Info.Concrete).
	Type Type
	// Region is the escape-analysis region of the values the variable
	// holds (DESIGN.md §2.10): RegionGlobal for globals, RegionRequest for
	// parameters, receivers and catch bindings, the join of all assigned
	// values for locals, RegionNone when Type has no region (HasRegion).
	// Not used for fields.
	Region Region
	// Pos is the position of the declaring identifier.
	Pos token.Position
	// Decl is the declaring node:
	//   LocalVar   *ast.LetStatement, *ast.ForOfStatement (loop variable),
	//              *ast.TryCatchStatement (catch binding),
	//              *ast.TransactionStatement (transaction handle)
	//   ParamVar   *ast.Parameter
	//   RecvVar    *ast.Parameter (FunctionStatement.Receiver)
	//   GlobalVar  *ast.LetStatement
	//   FieldVar   *ast.FieldDefinition
	// Nil for synthesized variables in tests.
	Decl ast.Node
	// Index is the parameter index (ParamVar), the index in Info.Globals
	// (GlobalVar), or the field index in declaration order (FieldVar); 0
	// otherwise.
	Index int
	// Optional reports, for a FieldVar, that the field may be absent (in
	// object literals and JSON) and starts as null: its Type is *Optional,
	// whether written "name?: T" or "name: T | null". Always equal to
	// IsOptional(Type) for fields; false for other kinds.
	Optional bool
	// Origin is, for a field of a generic instance or a parameter of a
	// function instance, the corresponding *Var of the origin; nil
	// otherwise.
	Origin *Var
	// Tag is the mangling tag of the owning module for a GlobalVar
	// (DESIGN-modules.md §5); "" for globals of the root/single-file module
	// and for every non-global kind. The empty tag reproduces today's C
	// names byte-for-byte.
	Tag string
}

// Func is a top-level function or receiver method, or an instance of a
// generic function.
type Func struct {
	// Name is the declared name (no type arguments, no receiver).
	Name string
	// Pos is the position of the declaring identifier.
	Pos token.Position
	// Decl is the declaration. Instances share the origin's Decl, so the
	// body is checked once, in terms of the type parameters.
	Decl *ast.FunctionStatement
	// Sig is the signature. For an instance it is the origin's signature
	// with the type arguments substituted, TypeParams nil, and fresh
	// parameter *Vars (Var.Origin = the origin's parameter).
	Sig *Signature
	// Guards are the resolved @Use guard functions in source order (spec
	// §10); nil when the function has no @Use.
	Guards []*Func
	// AfterHooks are the resolved @After hook functions in source order
	// (mirroring Guards); nil when the function has no @After. Each is a
	// (ctx: Context): void function. Lowered LIFO on every exit path; its
	// failures are swallowed, so they never make the decorated function
	// MayFail.
	AfterHooks []*Func
	// Origin is the generic origin of an instance; nil otherwise.
	Origin *Func
	// TypeArgs are the type arguments of an instance; nil otherwise.
	TypeArgs []Type
	// Instances are the instances of a generic origin in creation order
	// (concrete and non-concrete).
	Instances []*Func
	// MayFail reports that a call may return with the fiber error set
	// (DESIGN.md §2.8): a guard may fail, or the body has a throw, an
	// expression marked in Info.MayFail, or a transaction statement at a
	// point not inside the Body of a try (every catch catches everything;
	// code in a catch block is outside its own try). Computed by the checker
	// as a fixed point over the call graph (starting from false for
	// recursion) and set on every Func, instances included (an instance has
	// its origin's value: type arguments cannot change what may fail).
	MayFail bool
	// Tag is the mangling tag of the owning module (DESIGN-modules.md §5);
	// "" for the root/single-file module. Methods carry their tag through
	// the receiver's type (Mangle), so only the plain-function branch of
	// FuncCName consults it. The empty tag reproduces today's C names.
	Tag string
}

// IsMethod reports whether f has a receiver.
func (f *Func) IsMethod() bool { return f.Sig != nil && f.Sig.Recv != nil }

// IsGeneric reports whether f is a generic origin (has type parameters).
func (f *Func) IsGeneric() bool { return f.Sig != nil && len(f.Sig.TypeParams) > 0 }

// IsInstance reports whether f is an instance of a generic function.
func (f *Func) IsInstance() bool { return f.Origin != nil }

// Recv returns the receiver variable, or nil.
func (f *Func) Recv() *Var {
	if f.Sig == nil {
		return nil
	}
	return f.Sig.Recv
}

// FullName returns the name for diagnostics: "logger", "Context.handle",
// "first<User>".
func (f *Func) FullName() string {
	s := f.Name
	if r := f.Recv(); r != nil {
		s = typeString(r.Type) + "." + s
	}
	if len(f.TypeArgs) > 0 {
		s += "<" + typeListString(f.TypeArgs) + ">"
	}
	return s
}

// TypeName is a name that denotes a type: a universe type (int64, Context),
// an interface, a type alias, or a type parameter.
type TypeName struct {
	// Name is the declared name.
	Name string
	// Pos is the position of the declaring identifier (zero for universe
	// types).
	Pos token.Position
	// Decl is the declaring node: *ast.InterfaceStatement,
	// *ast.TypeAliasStatement, or the *ast.Identifier of a type parameter;
	// nil for universe types.
	Decl ast.Node
	// Type is the denoted type: *Basic (universe), *Named (interface or
	// object-type declaration), *TypeParam, or, when IsAlias, the aliased
	// type (which may mention TypeParams for a generic alias).
	Type Type
	// IsAlias reports a transparent alias ("type Ids = int64[];").
	IsAlias bool
	// TypeParams are the type parameters of a generic alias
	// ("type List<T> = T[];"); using List<User> means
	// InstanceCache.Subst(Type, TypeParams, [User]). Nil otherwise. (For a
	// generic interface they are on the *Named.)
	TypeParams []*TypeParam
	// Tag is the mangling tag of the owning module (DESIGN-modules.md §5);
	// "" for the root/single-file module. It threads into Mangle via
	// tagPrefix(*Named). The empty tag reproduces today's C names.
	Tag string
}

// Builtin is a universe object that is not a type: the namespaces console
// and db.
type Builtin struct {
	// Name is "console" or "db".
	Name string
	// ID is BuiltinConsole or BuiltinDB.
	ID BuiltinID
}

// ModuleNS is a compile-time-only object bound by a namespace import
// ("import * as m from ...", DESIGN-modules.md §9): it names a target
// module's export set rather than a value. It is never mangled and never
// used as a value; the checker resolves "m.X" against Exports (and Default
// for "m.default") and reports an E-TYPE when a ModuleNS appears in value
// position.
type ModuleNS struct {
	// Name is the local binding ("m" in "import * as m from ...").
	Name string
	// Pos is the position of the declaring identifier.
	Pos token.Position
	// Exports is the target module's named export set, keyed by exported
	// name.
	Exports map[string]Object
	// Default is the target module's default export, or nil when it has
	// none.
	Default Object
}

func (*Var) isObject()      {}
func (*Func) isObject()     {}
func (*TypeName) isObject() {}
func (*Builtin) isObject()  {}
func (*ModuleNS) isObject() {}

// ObjectName implements Object.
func (v *Var) ObjectName() string { return v.Name }

// ObjectType implements Object.
func (v *Var) ObjectType() Type { return v.Type }

// ObjectPos implements Object.
func (v *Var) ObjectPos() token.Position { return v.Pos }

// String returns "<kind> name type", e.g. "local x int64".
func (v *Var) String() string { return v.Kind.String() + " " + v.Name + " " + typeString(v.Type) }

// ObjectName implements Object.
func (f *Func) ObjectName() string { return f.Name }

// ObjectType returns f.Sig (nil if not yet set).
func (f *Func) ObjectType() Type {
	if f.Sig == nil {
		return nil
	}
	return f.Sig
}

// ObjectPos implements Object.
func (f *Func) ObjectPos() token.Position { return f.Pos }

// String returns "func " + FullName().
func (f *Func) String() string { return "func " + f.FullName() }

// ObjectName implements Object.
func (t *TypeName) ObjectName() string { return t.Name }

// ObjectType implements Object.
func (t *TypeName) ObjectType() Type { return t.Type }

// ObjectPos implements Object.
func (t *TypeName) ObjectPos() token.Position { return t.Pos }

// String returns "type Name".
func (t *TypeName) String() string { return "type " + t.Name }

// ObjectName implements Object.
func (b *Builtin) ObjectName() string { return b.Name }

// ObjectType returns nil: namespaces are not values.
func (b *Builtin) ObjectType() Type { return nil }

// ObjectPos returns the zero Position.
func (b *Builtin) ObjectPos() token.Position { return token.Position{} }

// String returns "builtin name".
func (b *Builtin) String() string { return "builtin " + b.Name }

// ObjectName implements Object.
func (m *ModuleNS) ObjectName() string { return m.Name }

// ObjectType returns nil: a module namespace is not a value.
func (m *ModuleNS) ObjectType() Type { return nil }

// ObjectPos implements Object.
func (m *ModuleNS) ObjectPos() token.Position { return m.Pos }

// String returns "module name".
func (m *ModuleNS) String() string { return "module " + m.Name }
