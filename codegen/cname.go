package codegen

import (
	"fmt"
	"strconv"

	"tlang/ast"
	"tlang/token"
	"tlang/types"
)

// C names (codegen design §4, DESIGN.md §3.2). Every C name of a user entity
// comes from a types name helper applied to a concrete type; codegen never
// builds a mangle by hand. The wrappers below check concreteness first, so a
// type that should never reach them (a type parameter, a generic origin,
// Invalid, nil) becomes an internal error instead of a panic inside the
// types package.

// requireConcrete fails with an internal error unless t has a C
// representation; what names the helper for the message.
func (g *generator) requireConcrete(t types.Type, what string) {
	if t == nil || !types.IsConcrete(t) {
		g.fail(internalErr(g.cur, "%s of non-concrete type %v", what, t))
	}
}

// mangle returns types.Mangle(t): the C name fragment of a concrete type,
// used for TLANG_SLICE_DEFINE tokens and as the deduplication key of
// concrete types.
func (g *generator) mangle(t types.Type) string {
	g.requireConcrete(t, "mangle")
	return types.Mangle(t)
}

// ctype returns types.CType(t), the C type of values of the concrete type t.
func (g *generator) ctype(t types.Type) string {
	g.requireConcrete(t, "C type")
	return types.CType(t)
}

// structName returns types.StructCName(n), the struct tag and typedef name
// of a concrete interface or interface instance (tl_User, tl_Page__User).
func (g *generator) structName(n *types.Named) string {
	if n == nil {
		g.fail(internalErr(g.cur, "struct name of a nil interface"))
	}
	g.requireConcrete(n, "struct name")
	return types.StructCName(n)
}

// sliceName returns types.SliceCName(elem), the slice header type of arrays
// of the concrete element type elem (tlang_slice_i64, tlang_slice_User).
func (g *generator) sliceName(elem types.Type) string {
	g.requireConcrete(elem, "slice name")
	return types.SliceCName(elem)
}

// funcName returns types.FuncCName(f): tl_f_<name>, tl_f_<name>__<args> for
// a generic instance, tl_m_<Recv>__<name> for a method. f must be concrete:
// a plain function or method, or an instance whose type arguments are
// concrete, never a generic origin.
func (g *generator) funcName(f *types.Func) string {
	if f == nil || f.Sig == nil || !types.IsConcrete(f.Sig) {
		g.fail(internalErr(g.cur, "function name of non-concrete function %v", f))
	}
	return types.FuncCName(f)
}

// jsonParseName returns types.JSONParseCName(t) (tlj_parse_User,
// tlj_parse_arr_User) for a concrete interface or array type.
func (g *generator) jsonParseName(t types.Type) string {
	g.requireConcrete(t, "JSON parser name")
	return types.JSONParseCName(t)
}

// jsonWriteName returns types.JSONWriteCName(t) (tlj_write_User,
// tlj_write_arr_User) for a concrete interface or array type.
func (g *generator) jsonWriteName(t types.Type) string {
	g.requireConcrete(t, "JSON writer name")
	return types.JSONWriteCName(t)
}

// fieldName returns types.FieldCName(name), the C struct member of a field
// (f_name).
func fieldName(name string) string { return types.FieldCName(name) }

// globalName returns types.GlobalCName(v.Tag, v.Name), the member of struct
// tl_globals of a global (g_name for an untagged global, read as
// __fib->globals->g_name).
func globalName(v *types.Var) string { return types.GlobalCName(v.Tag, v.Name) }

// localBase returns types.LocalCName(name), the base C name of a local,
// parameter or receiver (l_name); localNames adds the shadowing suffixes.
func localBase(name string) string { return types.LocalCName(name) }

// File-scope entities that codegen invents and that have no types helper are
// named with the "__tl_" prefix (codegen design §4.2): no C name derived
// from a user declaration starts with "__", so these cannot collide with
// user entities. Each is a static const object, emitted only together with
// the construct that references it (codegen design §5.1).

// routeName returns the name of the static route table of the ctx.match
// call with Route.ID id: __tl_r<ID>.
func routeName(id int) string { return "__tl_r" + strconv.Itoa(id) }

// routeSegsName returns the name of the segment array of route id:
// __tl_r<ID>_segs.
func routeSegsName(id int) string { return routeName(id) + "_segs" }

// typeDescName returns the name of the row descriptor of the DB row type
// with mangle m: __tl_td_<m>.
func typeDescName(m string) string { return "__tl_td_" + m }

// fieldDescName returns the name of the field-descriptor array of the DB row
// type with mangle m: __tl_fd_<m>.
func fieldDescName(m string) string { return "__tl_fd_" + m }

// longStrName returns the name of the n-th overlength string constant's
// char array: __tl_s<N>.
func longStrName(n int) string { return "__tl_s" + strconv.Itoa(n) }

// localNames allocates the C names of the parameters, the receiver and the
// locals of one generated C function (codegen design §4.3, plan D5). A
// variable is named by its *types.Var, so shadowing declarations and an
// initializer that reads the outer binding of the same name need no special
// care. A declaration takes types.LocalCName of its name (l_x) unless that
// name is in use in an open scope; then the first free of l_x__2, l_x__3, ...
// Names are released when their scope closes, so sibling scopes reuse them
// (two sibling catch (e) blocks both get l_e; nested ones get l_e and
// l_e__2).
//
// The outermost scope is the function's: the receiver and the parameters are
// declared there first, so no local can take their names. Generated
// temporaries (__t<N>, __jp<N>, ...) and labels start with "__", which no
// "l_" name does, so they need no reservation; reserve exists for the hidden
// parameter __fib and any other fixed generated name.
type localNames struct {
	// scopes lists the names declared in each open scope, outermost first.
	scopes [][]string
	// inUse counts, per C name, the open scopes that declare it (lookup only).
	inUse map[string]int
	// byVar maps each declared or aliased variable to its C name (lookup only).
	byVar map[*types.Var]string
}

// newLocalNames returns an allocator with the function scope open.
func newLocalNames() *localNames {
	return &localNames{
		scopes: [][]string{nil},
		inUse:  map[string]int{},
		byVar:  map[*types.Var]string{},
	}
}

// push opens a nested scope (a C block).
func (l *localNames) push() { l.scopes = append(l.scopes, nil) }

// pop closes the innermost scope, releasing its names. The function scope
// cannot be closed.
func (l *localNames) pop() {
	if len(l.scopes) < 2 {
		panic("localNames: pop of the function scope")
	}
	last := len(l.scopes) - 1
	for _, name := range l.scopes[last] {
		l.inUse[name]--
	}
	l.scopes = l.scopes[:last]
}

// reserve marks name as taken in the function scope.
func (l *localNames) reserve(name string) {
	l.scopes[0] = append(l.scopes[0], name)
	l.inUse[name]++
}

// declare names v in the innermost scope and returns its C name. Declaring a
// variable twice is a codegen bug.
func (l *localNames) declare(v *types.Var) string {
	if v == nil {
		panic("localNames: declare of a nil variable")
	}
	if name, dup := l.byVar[v]; dup {
		panic(fmt.Sprintf("localNames: %s declared twice (as %s)", v.Name, name))
	}
	base := localBase(v.Name)
	name := base
	for n := 2; l.inUse[name] > 0; n++ {
		name = base + "__" + strconv.Itoa(n)
	}
	last := len(l.scopes) - 1
	l.scopes[last] = append(l.scopes[last], name)
	l.inUse[name]++
	l.byVar[v] = name
	return name
}

// alias gives v the C name of the already named variable same. It maps the
// origin's parameter *Vars, which the shared body of a generic function
// refers to, to the parameters of the instance being emitted (codegen design
// §5.2 step 1).
func (l *localNames) alias(v, same *types.Var) {
	name, ok := l.byVar[same]
	if !ok {
		panic(fmt.Sprintf("localNames: alias of unnamed variable %v", same))
	}
	l.byVar[v] = name
}

// name returns the C name of v, and whether v has one.
func (l *localNames) name(v *types.Var) (string, bool) {
	name, ok := l.byVar[v]
	return name, ok
}

// reservedCNames are the file-scope C names that generated code itself
// defines and that a user declaration's C name could otherwise produce:
// interface globals maps to tl_globals (the struct tag of the program
// globals, declared by tlang.h), interface _init_globals to tl__init_globals
// (the globals initializer). __tl_program cannot be produced by a user name
// but is listed for completeness (codegen design §4.4).
var reservedCNames = []string{"tl_globals", "tl__init_globals", "__tl_program"}

// nameDecl is one entry of the C-name collision table.
type nameDecl struct {
	// what describes the declaration ("interface User", "function logger",
	// "method User.greet"); "" for a name generated code reserves.
	what string
	// pos is the position of the declaring identifier.
	pos token.Position
}

// nameTable is the program-level C-name collision table (codegen design
// §4.4, §12 item 8): the file-scope C names of every interface and interface
// instance (StructCName) and of every function, method and function instance
// (FuncCName), plus the reserved names of generated code. The mangling
// scheme admits collisions that types.CheckDeclName does not prevent
// (interface f_logger beside fn logger both give tl_f_logger), so the first
// duplicate is an error rather than C that does not compile.
type nameTable struct {
	byName map[string]nameDecl // lookup only, never ranged
}

// newNameTable returns a table holding reservedCNames.
func newNameTable() *nameTable {
	t := &nameTable{byName: map[string]nameDecl{}}
	for _, name := range reservedCNames {
		t.reserve(name)
	}
	return t
}

// reserve registers a C name of generated code. It returns the §12 item 8
// error when a user declaration already has that name.
func (t *nameTable) reserve(cname string) *cgError {
	if prev, dup := t.byName[cname]; dup {
		if prev.what == "" {
			return nil
		}
		return errReservedName(cname, prev)
	}
	t.byName[cname] = nameDecl{}
	return nil
}

// add registers the C name of a user declaration described by what at pos.
// It returns the §12 item 8 error for the first duplicate: against a
// reserved name it is reported at the user declaration; between two user
// declarations it names both in source order and is reported at the later.
func (t *nameTable) add(cname, what string, pos token.Position) *cgError {
	d := nameDecl{what: what, pos: pos}
	prev, dup := t.byName[cname]
	switch {
	case !dup:
		t.byName[cname] = d
		return nil
	case prev.what == "":
		return errReservedName(cname, d)
	case d.pos.Before(prev.pos):
		return errCollision(cname, d, prev)
	}
	return errCollision(cname, prev, d)
}

// namedWhat describes an interface for collision messages: "interface User",
// "interface Page<User>", or "type P" for an object-type declaration.
func namedWhat(n *types.Named) string {
	if _, ok := n.Decl.(*ast.TypeAliasStatement); ok {
		return "type " + n.String()
	}
	return "interface " + n.String()
}

// namedPos returns the position of an interface's declaring identifier (an
// instance shares its origin's).
func namedPos(n *types.Named) token.Position {
	if n.Obj == nil {
		return token.Position{}
	}
	return n.Obj.Pos
}

// funcWhat describes a function for collision messages: "function logger",
// "function first<User>", "method User.greet".
func funcWhat(f *types.Func) string {
	if f.IsMethod() {
		return "method " + f.FullName()
	}
	return "function " + f.FullName()
}
