package types

import (
	"go/constant"

	"tlang/ast"
)

// TypeAndValue is the checker's result for one expression.
type TypeAndValue struct {
	// Type is the final type of the expression (see Info.Types).
	Type Type
	// Value is the constant value for constant expressions, already
	// representable in Type (see Representable); nil otherwise.
	Value constant.Value
}

// IsConstant reports whether the expression is a compile-time constant.
func (tv TypeAndValue) IsConstant() bool { return tv.Value != nil }

// SelectionKind classifies a MemberExpression.
type SelectionKind int

const (
	// SelField selects a field of a user interface value: x.f.
	SelField SelectionKind = iota + 1
	// SelMethod selects a user receiver method; the MemberExpression is the
	// callee of a CallExpression.
	SelMethod
	// SelBuiltin selects a builtin member of a value (s.len, xs.push,
	// ctx.header, err.message, n.toString) or of a namespace (console.log,
	// db.query).
	SelBuiltin
	// SelModuleValue selects a value export of a module namespace
	// ("m.counter" where m is an "import * as m" binding and counter is an
	// exported global). It lowers to a direct read of the target global
	// Var (qualified-access lowering, DESIGN-modules.md §9): Field holds the
	// target *Var and Type its value type, so codegen emits the target's
	// tagged global name with no trace of the namespace.
	SelModuleValue
)

// Selection is the resolution of one MemberExpression.
type Selection struct {
	// Kind classifies the selection.
	Kind SelectionKind
	// Recv is the type of the Object operand (the narrowed type if Object
	// is a narrowed identifier); nil when Object is a namespace (console,
	// db).
	Recv Type
	// Field is the selected field for SelField: a field of Recv (for an
	// instance, the instance's own field Var). Field.Index is the C struct
	// position and FieldCName(Field.Name) the C member.
	Field *Var
	// Func is the method for SelMethod.
	Func *Func
	// Builtin is the member for SelBuiltin.
	Builtin BuiltinID
	// Type is the type of the selected value for SelField and field-like
	// builtins (s.len: int64, ctx.path: string); nil for methods, whose
	// call result is in Info.Types of the CallExpression.
	Type Type
}

// CallKind classifies a CallExpression.
type CallKind int

const (
	// CallFunc calls a user function by name: f(args).
	CallFunc CallKind = iota + 1
	// CallMethod calls a user receiver method: recv.m(args), lowered to a
	// direct call FuncCName(Func)(__fib, recv, args...) (spec §3.2).
	CallMethod
	// CallBuiltin calls a builtin member or namespace function.
	CallBuiltin
	// CallConversion is int32(x), int64(x) or float64(x).
	CallConversion
)

// Call is the resolution of one CallExpression.
type Call struct {
	// Kind classifies the call.
	Kind CallKind
	// Func is the callee for CallFunc and CallMethod. For a generic callee
	// it is the canonical instance for the explicit or inferred type
	// arguments; inside a generic body that instance may be non-concrete
	// (first<T>), and codegen maps it with Info.ConcreteFunc.
	Func *Func
	// Builtin is the builtin for CallBuiltin and CallConversion.
	Builtin BuiltinID
	// TypeArgs are the type arguments: Func.TypeArgs for generic user
	// calls; [T] for db.query<T>, db.queryOne<T>, tx.query<T> and
	// tx.queryOne<T>; nil otherwise.
	TypeArgs []Type
	// Recv is the receiver expression of a CallMethod or of a builtin
	// member call on a value (it is Function.(*ast.MemberExpression).Object);
	// nil for CallFunc, CallConversion and namespace calls (console.*,
	// db.*).
	Recv ast.Expression
	// MayFail reports that the call may return with the fiber error set
	// (Func.MayFail or BuiltinID.MayFail; false for conversions). Equal to
	// Info.MayFail[call].
	MayFail bool
	// Route is the compiled route for a ctx.match call; nil otherwise.
	Route *Route
}

// ConversionKind classifies an implicit conversion.
type ConversionKind int

const (
	// ConvNone means no conversion (the zero Conversion).
	ConvNone ConversionKind = iota
	// ConvToOptional widens a T value to T | null (DESIGN.md §2.5): string
	// becomes tlang_str_some(x) (never turns "" into null); int32, int64,
	// float64 and bool become (tlang_opt_X){true, x}; interfaces and arrays
	// need no code.
	ConvToOptional
)

// Conversion is an implicit conversion of an expression's value.
type Conversion struct {
	// Kind classifies the conversion.
	Kind ConversionKind
	// From is the expression's type (as in Info.Types).
	From Type
	// To is the target type (an *Optional for ConvToOptional). Inside a
	// generic body From and To may mention type parameters; after
	// Info.Concrete, if From and To are Identical (T := U | null) the
	// conversion is a no-op.
	To Type
}

// JSONType is a type that needs generated JSON code (DESIGN.md §2.12).
type JSONType struct {
	// Type is a concrete *Named or *Array; the functions are
	// JSONParseCName(Type) and JSONWriteCName(Type).
	Type Type
	// Parse reports that a parser is needed (ctx.bindJson, transitively).
	Parse bool
	// Write reports that a writer is needed (ctx.json, transitively).
	Write bool
}

// ProgramKind says how the program runs (DESIGN.md §2.10).
type ProgramKind int

const (
	// ProgramUnknown: neither or both entry points (an error was reported).
	ProgramUnknown ProgramKind = iota
	// ProgramServer: fn route_dispatcher(ctx: Context): void is the HTTP
	// handler.
	ProgramServer
	// ProgramScript: fn main(): void runs once.
	ProgramScript
)

// Info is the checker's complete output. Codegen emits C from the AST and
// Info alone, without re-deriving semantics. Maps are keyed by node
// pointers of the checked *ast.Program; lists are in deterministic order.
// Info may be incomplete when the checker reported errors; codegen runs
// only on error-free programs.
//
// Generic code: a generic function body is checked once, in terms of its
// type parameters, so entries recorded inside it (Types, the Vars of
// Defs/Uses, Calls, Selections, Conversions) may mention type parameters.
// When emitting a concrete instance, codegen maps types with Concrete and
// callees with ConcreteFunc. The lists below hold concrete entities only
// (IsConcrete).
type Info struct {
	// Types records every value expression with its final type: untyped
	// constants already took the expected type, or Default when none was
	// expected (a literal 5 passed to an int32 parameter is int32). No
	// untyped type remains in an error-free program. Further rules:
	//   - a null literal is recorded with the optional type it converts to
	//     (in x == null, the type of x);
	//   - a constant is never recorded with an optional type: in
	//     "let x: int64 | null = 5" the 5 is int64 and Conversions[5]
	//     holds the widening;
	//   - a narrowed identifier (Narrowed) is recorded with the narrowed
	//     type T;
	//   - void calls and AssignmentExpressions are recorded with Typ[Void];
	//   - TypeAndValue.Value is set for constants: number, bool and string
	//     literals, and operations on constants folded with FoldUnary and
	//     FoldBinary (-1, 2 * 3, !true, 1 < 2); codegen may emit the value
	//     instead of the expression. Values are exact (go/constant); an int64
	//     value can be math.MinInt64, which C cannot spell as a literal.
	// There is no entry for identifiers that denote types, functions or
	// namespaces (see Uses), for a MemberExpression that is the callee of a
	// CallExpression (see Selections and Calls), for declaring identifiers
	// (see Defs), or for MemberExpression.Property and ObjectEntry.Key.
	Types map[ast.Expression]TypeAndValue

	// TypeExprs records the resolved type of every type expression the
	// checker resolved: annotations, parameter, result and field types,
	// "new" types, explicit type arguments.
	TypeExprs map[ast.TypeExpr]Type

	// Defs maps each declaring identifier to its object:
	//   FunctionStatement.Name                 *Func
	//   Parameter.Name                         *Var (ParamVar; RecvVar for the receiver)
	//   LetStatement.Name                      *Var (LocalVar; GlobalVar at top level)
	//   ForOfStatement.Var                     *Var (LocalVar)
	//   TryCatchStatement.CatchParam           *Var (LocalVar, type Error)
	//   TransactionStatement.Param             *Var (LocalVar, type Transaction)
	//   InterfaceStatement.Name,
	//   TypeAliasStatement.Name                *TypeName
	//   type parameter identifiers             *TypeName whose Type is a *TypeParam
	//   FieldDefinition.Name                   *Var (FieldVar) of the declared type
	Defs map[*ast.Identifier]Object

	// Uses maps each referring identifier to its object: variables in
	// expressions (*Var), callee names (*Func), type names in NamedType and
	// conversion callees (*TypeName), the namespaces db and console
	// (*Builtin, also as TransactionStatement.Receiver), @Use arguments
	// (*Func), and ObjectEntry.Key (the field *Var of the literal's type,
	// so Var.Index gives the struct position). MemberExpression.Property
	// and Decorator.Name are not recorded (see Selections).
	Uses map[*ast.Identifier]Object

	// Selections records every MemberExpression, including callees of
	// method and builtin calls.
	Selections map[*ast.MemberExpression]*Selection

	// Calls records every CallExpression.
	Calls map[*ast.CallExpression]*Call

	// Conversions records the implicit conversion applied to an
	// expression's value where it is used: initializer, assigned value
	// (including compound assignment), argument, return value, object or
	// array literal element, a branch of ?: whose result type is the
	// optional of the branch's type. Absent means no conversion. Operands of
	// == and != never get one (CanCompare only allows combinations codegen
	// compares directly), nor does a ?? fallback (it must already be T).
	// Codegen applies the conversion after evaluating the expression (and
	// after reading a narrowed payload).
	Conversions map[ast.Expression]Conversion

	// Narrowed marks each use of a local or parameter of type T | null at a
	// point where it is known not to be null (DESIGN.md §2.5); Types holds T
	// for it. Codegen reads the payload: ".v" when the variable's type
	// (after Concrete, inside a generic instance) is a primitive optional
	// (IsPrimitiveOptional), the value itself for string, interface and
	// array optionals. Every read of the variable inside the narrowed
	// region is marked, except: an assignment target, an operand of a
	// comparison with null, the left operand of ??, and the operand of a
	// postfix ! (those keep the optional type and are not marked).
	Narrowed map[*ast.Identifier]bool

	// MayFail marks expressions whose own operation (operands have their own
	// entries) may leave the fiber error set; codegen emits
	// "if (__fib->err) goto <nearest catch label or failure exit>" right
	// after evaluating it and before evaluating anything else (DESIGN.md
	// §2.8). The checker marks exactly:
	//   *ast.CallExpression        Call.MayFail
	//   *ast.NonNullExpression     always (500 "null value")
	//   *ast.InfixExpression       integer / and % unless the divisor is a
	//                              nonzero constant (500 "division by zero")
	//   *ast.IndexExpression       always (500 "index out of range"), whether
	//                              read or assignment target
	//   *ast.AssignmentExpression  integer /= and %= (same rule as / and %)
	// Absent means false. Allocation never fails in this sense: running out
	// of memory aborts the request through the runtime (tlang.h, "Out of
	// memory"), so new, literals, concatenation and toString need no check.
	// Float / and % never fail. ThrowStatement always fails and
	// TransactionStatement may fail (BEGIN, COMMIT); they are not recorded.
	MayFail map[ast.Expression]bool

	// TxIDs numbers TransactionStatements from 1 in source order across the
	// program; codegen uses N in its labels (__tx_rollback_N, __tx_fail_N,
	// __tx_end_N, spec §11.4).
	TxIDs map[*ast.TransactionStatement]int

	// Interfaces lists the concrete interface types that need a C struct:
	// plain interfaces and object-type declarations in source order, then
	// every concrete instance in Instances.NamedInstances() in creation
	// order, the whole list passed through SortByFieldDeps. Generic origins
	// and non-concrete instances are excluded. Structs only point to each
	// other, so codegen still emits forward typedefs first.
	Interfaces []*Named

	// Funcs lists every non-generic function and method in source order.
	Funcs []*Func

	// FuncInstances lists every concrete instance in
	// Instances.FuncInstances(), in creation order. Before filling it, the
	// checker closes the cache under monomorphization: for each concrete
	// instance it instantiates, with that instance's type arguments, every
	// generic callee and interface instance its body uses (the same closure
	// feeds Interfaces, JSONTypes and DBTypes), until no new instance
	// appears.
	FuncInstances []*Func

	// Globals lists the top-level variables in declaration order (the order
	// of struct tl_globals members and of initialization); Var.Index is the
	// position in this list.
	Globals []*Var

	// JSONTypes lists every concrete interface or array type that needs a
	// generated JSON parser and/or writer, closed over field and element
	// types (optional fields and elements contribute their non-null type),
	// one entry per type (deduplicated by Mangle), dependencies first;
	// cycles through optional fields are broken by first demand.
	JSONTypes []*JSONType

	// DBTypes lists every concrete interface used as a row type by
	// db.query<T>, db.queryOne<T>, tx.query<T> or tx.queryOne<T>,
	// deduplicated, in first-use order.
	DBTypes []*Named

	// Routes lists every ctx.match call in source order; Route.ID is its
	// index plus 1.
	Routes []*Route

	// Kind is ProgramServer or ProgramScript (ProgramUnknown only with
	// errors).
	Kind ProgramKind

	// Entry is route_dispatcher (server) or main (script).
	Entry *Func

	// UsesDB reports any use of db (a db or tx call, or a transaction
	// statement): codegen sets .uses_db and the driver links libpq
	// (otherwise it compiles the runtime with -DTLANG_NO_PG).
	UsesDB bool

	// Instances is the instantiation cache used by the checker. Codegen
	// reaches it only through Concrete and ConcreteFunc, which never create
	// instances.
	Instances *InstanceCache
}

// NewInfo returns an Info with all maps allocated and an empty instance
// cache.
func NewInfo() *Info {
	return &Info{
		Types:       map[ast.Expression]TypeAndValue{},
		TypeExprs:   map[ast.TypeExpr]Type{},
		Defs:        map[*ast.Identifier]Object{},
		Uses:        map[*ast.Identifier]Object{},
		Selections:  map[*ast.MemberExpression]*Selection{},
		Calls:       map[*ast.CallExpression]*Call{},
		Conversions: map[ast.Expression]Conversion{},
		Narrowed:    map[*ast.Identifier]bool{},
		MayFail:     map[ast.Expression]bool{},
		TxIDs:       map[*ast.TransactionStatement]int{},
		Instances:   NewInstanceCache(),
	}
}

// TypeOf returns Types[e].Type, or nil when e has no entry.
func (info *Info) TypeOf(e ast.Expression) Type {
	if tv, ok := info.Types[e]; ok {
		return tv.Type
	}
	return nil
}

// ObjectOf returns Defs[id] or else Uses[id], or nil.
func (info *Info) ObjectOf(id *ast.Identifier) Object {
	if obj, ok := info.Defs[id]; ok {
		return obj
	}
	return info.Uses[id]
}

// Concrete returns t as seen inside the function instance inst: the
// origin's type parameters replaced by inst.TypeArgs. When inst is nil or
// not an instance, t is returned unchanged.
func (info *Info) Concrete(t Type, inst *Func) Type {
	if t == nil || inst == nil || inst.Origin == nil || inst.Origin.Sig == nil {
		return t
	}
	return info.Instances.Subst(t, inst.Origin.Sig.TypeParams, inst.TypeArgs)
}

// ConcreteFunc returns the canonical concrete instance for a callee
// recorded inside the body of inst (Call.Func or Selection.Func): callee
// itself when it is not an instance or its type arguments do not mention
// inst's type parameters, otherwise the instance with substituted type
// arguments. It never creates instances: nil means the checker did not
// instantiate a needed function, an internal error.
func (info *Info) ConcreteFunc(callee, inst *Func) *Func {
	if callee == nil || callee.Origin == nil || inst == nil || inst.Origin == nil {
		return callee
	}
	args := make([]Type, len(callee.TypeArgs))
	changed := false
	for i, a := range callee.TypeArgs {
		args[i] = info.Concrete(a, inst)
		changed = changed || args[i] != a
	}
	if !changed {
		return callee
	}
	return info.Instances.LookupFunc(callee.Origin, args)
}

// SliceTypes returns every concrete array type the program uses, one per
// Mangle name, element arrays before arrays of them, in first-seen order
// over: Interfaces (field types), Globals (types and initializers), Funcs
// and FuncInstances (signatures, declared variables and expression types,
// substituted per instance), and JSONTypes. Codegen emits a slice typedef
// for each whose element is not PredefinedSlice.
func (info *Info) SliceTypes() []*Array {
	c := &sliceCollector{seen: map[string]bool{}}
	for _, n := range info.Interfaces {
		for _, f := range n.Fields() {
			c.add(f.Type)
		}
	}
	for _, g := range info.Globals {
		c.add(g.Type)
		if let, ok := g.Decl.(*ast.LetStatement); ok && let.Value != nil {
			info.collectTypes(c, let.Value, nil)
		}
	}
	for _, f := range info.Funcs {
		info.collectFunc(c, f, nil)
	}
	for _, f := range info.FuncInstances {
		info.collectFunc(c, f, f)
	}
	for _, j := range info.JSONTypes {
		c.add(j.Type)
	}
	return c.out
}

func (info *Info) collectFunc(c *sliceCollector, f, inst *Func) {
	if f.Sig != nil {
		if f.Sig.Recv != nil {
			c.add(f.Sig.Recv.Type)
		}
		for _, p := range f.Sig.Params {
			c.add(p.Type)
		}
		c.add(f.Sig.Result)
	}
	if f.Decl != nil && f.Decl.Body != nil {
		info.collectTypes(c, f.Decl.Body, inst)
	}
}

func (info *Info) collectTypes(c *sliceCollector, root ast.Node, inst *Func) {
	ast.Inspect(root, func(n ast.Node) bool {
		e, ok := n.(ast.Expression)
		if !ok {
			return true
		}
		if tv, ok := info.Types[e]; ok {
			c.add(info.Concrete(tv.Type, inst))
		}
		if id, ok := e.(*ast.Identifier); ok {
			if v, ok := info.Defs[id].(*Var); ok {
				c.add(info.Concrete(v.Type, inst))
			}
		}
		return true
	})
}

type sliceCollector struct {
	seen map[string]bool
	out  []*Array
}

func (c *sliceCollector) add(t Type) {
	switch t := t.(type) {
	case *Array:
		c.add(t.Elem)
		if !IsConcrete(t) {
			return
		}
		if m := Mangle(t); !c.seen[m] {
			c.seen[m] = true
			c.out = append(c.out, t)
		}
	case *Optional:
		c.add(t.Elem)
	}
}

// SortByFieldDeps orders interfaces so that each one comes after the
// interfaces its fields refer to (directly, or through arrays and
// optionals), restricted to the members of list. It is a depth-first
// post-order that starts from each element in list order, so it is
// deterministic and keeps the input order where there is no dependency;
// cycles (possible through optional fields) are broken where the walk
// meets them.
func SortByFieldDeps(list []*Named) []*Named {
	member := make(map[*Named]bool, len(list))
	for _, n := range list {
		member[n] = true
	}
	visited := make(map[*Named]bool, len(list))
	out := make([]*Named, 0, len(list))
	var visit func(n *Named)
	visit = func(n *Named) {
		if visited[n] {
			return
		}
		visited[n] = true
		for _, f := range n.Fields() {
			for _, d := range namedIn(f.Type, nil) {
				if member[d] {
					visit(d)
				}
			}
		}
		out = append(out, n)
	}
	for _, n := range list {
		visit(n)
	}
	return out
}

// namedIn appends the interfaces t refers to directly or through arrays and
// optionals (not through other interfaces' fields).
func namedIn(t Type, acc []*Named) []*Named {
	switch t := t.(type) {
	case *Named:
		return append(acc, t)
	case *Array:
		return namedIn(t.Elem, acc)
	case *Optional:
		return namedIn(t.Elem, acc)
	}
	return acc
}
