package checker

import (
	"tlang/token"
	"tlang/types"
)

// scope is a lexical scope: a set of named objects plus a link to the
// enclosing scope. The global scope's parent is nil; lookup falls back to
// the universe (types.LookupUniverse) past the root.
type scope struct {
	// objs holds the bindings declared directly in this scope.
	objs map[string]types.Object
	// parent is the enclosing scope, nil for the global scope.
	parent *scope
}

// newScope returns an empty scope nested in parent.
func newScope(parent *scope) *scope {
	return &scope{objs: map[string]types.Object{}, parent: parent}
}

// lookup returns the object bound to name in this scope or an enclosing one,
// falling back to the universe past the root, or nil when name is undefined.
func (s *scope) lookup(name string) types.Object {
	for sc := s; sc != nil; sc = sc.parent {
		if obj := sc.objs[name]; obj != nil {
			return obj
		}
	}
	return types.LookupUniverse(name)
}

// declare binds obj to name in s, enforcing the name policy of DESIGN.md
// §2.3 (name resolution) and OQ2 (universe shadowing):
//
//   - types.CheckDeclName is applied first; on error the name is reported
//     E-NAME but still bound, so later uses resolve and do not cascade;
//   - a DeclValue or DeclType binding may not shadow a universe type name or
//     namespace (OQ2): CheckDeclName does not reject "let string = ...", so
//     declare adds the ban here;
//   - redeclaration in this same scope is E-NAME "redeclared" with a note at
//     the previous declaration, and the second binding is dropped (first
//     wins), so later uses stay stable;
//   - shadowing an outer user binding in a nested scope is allowed silently.
func (c *checker) declare(s *scope, name string, kind types.DeclKind, obj types.Object, pos token.Position) {
	if err := types.CheckDeclName(name, kind); err != nil {
		c.errorf(pos, "E-NAME", "%s", err.Error())
		c.errDeclPos[filePos{c.file, pos}] = true
	} else if (kind == types.DeclValue || kind == types.DeclType) && types.LookupUniverse(name) != nil {
		c.errorf(pos, "E-NAME", "cannot redeclare the builtin type/name `%s`", name)
		return
	}
	if prev := s.objs[name]; prev != nil {
		c.errorf(pos, "E-NAME", "`%s` redeclared", name)
		c.notefIn(c.objFile(prev), prev.ObjectPos(), "E-NAME", "previous declaration of `%s`", name)
		c.errDeclPos[filePos{c.file, pos}] = true
		return
	}
	s.objs[name] = obj
}
