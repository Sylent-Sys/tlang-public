package types

import (
	"slices"
	"strconv"
	"strings"
)

// MaxInstanceDepth bounds the nesting depth of a type argument (TypeDepth).
// An instantiation beyond it fails, which stops expanding recursion such as
// "interface Tree<T> { kids: Tree<T[]>[] }" (each expansion adds a level).
const MaxInstanceDepth = 32

// InstantiationError describes a failed instantiation.
type InstantiationError struct {
	// Origin is the name of the generic interface or function.
	Origin string
	// TypeArgs are the requested type arguments.
	TypeArgs []Type
	// Reason explains the failure.
	Reason string
}

// Error returns "cannot instantiate Origin<args>: reason".
func (e *InstantiationError) Error() string {
	return "cannot instantiate " + e.Origin + "<" + typeListString(e.TypeArgs) + ">: " + e.Reason
}

// InstanceCache creates and deduplicates generic instances. One cache is
// used for a whole compilation (Info.Instances). For each origin and list of
// type arguments (compared with Identical) it returns the same *Named or
// *Func, so codegen can rely on pointer identity, and it remembers creation
// order so that output is deterministic. Not safe for concurrent use.
type InstanceCache struct {
	ids       map[any]int
	named     map[instKey]*Named
	funcs     map[instKey]*Func
	namedList []*Named
	funcList  []*Func
	errs      []error
}

type instKey struct {
	origin any
	args   string
}

// NewInstanceCache returns an empty cache.
func NewInstanceCache() *InstanceCache {
	return &InstanceCache{
		ids:   map[any]int{},
		named: map[instKey]*Named{},
		funcs: map[instKey]*Func{},
	}
}

// InstantiateNamed returns the canonical instance origin<targs>, creating
// it on first request. origin must be a generic origin (not an instance)
// and len(targs) must equal len(origin.TypeParams). The new instance's
// fields are computed lazily (Named.Fields). Type arguments may mention
// type parameters (a non-concrete instance such as Page<T> inside a generic
// body). It fails when a type argument is nil or nested deeper than
// MaxInstanceDepth. It does not check ValidTypeArg; the checker does.
func (c *InstanceCache) InstantiateNamed(origin *Named, targs []Type) (*Named, error) {
	if origin == nil || origin.Origin != nil || len(origin.TypeParams) == 0 {
		name := "?"
		if origin != nil {
			name = origin.String()
		}
		return nil, &InstantiationError{Origin: name, TypeArgs: targs, Reason: "not a generic interface"}
	}
	if err := checkTypeArgs(origin.Name(), len(origin.TypeParams), targs); err != nil {
		return nil, err
	}
	k := instKey{origin, c.argsKey(targs)}
	if n := c.named[k]; n != nil {
		return n, nil
	}
	inst := &Named{
		Obj:      origin.Obj,
		Decl:     origin.Decl,
		TypeArgs: slices.Clone(targs),
		Origin:   origin,
		cache:    c,
	}
	c.named[k] = inst
	c.namedList = append(c.namedList, inst)
	origin.Instances = append(origin.Instances, inst)
	return inst, nil
}

// InstantiateFunc returns the canonical instance origin<targs>, creating it
// on first request. origin must be a generic function (Sig set, with type
// parameters) and not an instance. The instance copies Name, Pos, Decl,
// Guards, AfterHooks, MayFail and Tag from origin (Tag keeps same-named
// generics of two modules apart in FuncCName) and gets the substituted
// signature with fresh parameter Vars. Same failure rules as
// InstantiateNamed.
func (c *InstanceCache) InstantiateFunc(origin *Func, targs []Type) (*Func, error) {
	if origin == nil || origin.Origin != nil || !origin.IsGeneric() {
		name := "?"
		if origin != nil {
			name = origin.Name
		}
		return nil, &InstantiationError{Origin: name, TypeArgs: targs, Reason: "not a generic function"}
	}
	if err := checkTypeArgs(origin.Name, len(origin.Sig.TypeParams), targs); err != nil {
		return nil, err
	}
	k := instKey{origin, c.argsKey(targs)}
	if f := c.funcs[k]; f != nil {
		return f, nil
	}
	inst := &Func{
		Name:       origin.Name,
		Pos:        origin.Pos,
		Decl:       origin.Decl,
		Guards:     origin.Guards,
		AfterHooks: origin.AfterHooks,
		Origin:     origin,
		TypeArgs:   slices.Clone(targs),
		MayFail:    origin.MayFail,
		Tag:        origin.Tag,
	}
	c.funcs[k] = inst
	c.funcList = append(c.funcList, inst)
	origin.Instances = append(origin.Instances, inst)
	s := substituter{c: c, tparams: origin.Sig.TypeParams, targs: inst.TypeArgs}
	inst.Sig = s.signature(origin.Sig, true)
	return inst, nil
}

func checkTypeArgs(name string, want int, targs []Type) error {
	if len(targs) != want {
		return &InstantiationError{Origin: name, TypeArgs: targs,
			Reason: "wrong number of type arguments: want " + strconv.Itoa(want) + ", got " + strconv.Itoa(len(targs))}
	}
	for _, a := range targs {
		if a == nil {
			return &InstantiationError{Origin: name, TypeArgs: targs, Reason: "missing type argument"}
		}
		if TypeDepth(a) > MaxInstanceDepth {
			return &InstantiationError{Origin: name, TypeArgs: targs,
				Reason: "type arguments nested deeper than " + strconv.Itoa(MaxInstanceDepth) + " levels (recursive generic type?)"}
		}
	}
	return nil
}

// LookupNamed returns the existing instance origin<targs>, or nil. It never
// creates an instance.
func (c *InstanceCache) LookupNamed(origin *Named, targs []Type) *Named {
	return c.named[instKey{origin, c.argsKey(targs)}]
}

// LookupFunc returns the existing instance origin<targs>, or nil. It never
// creates an instance.
func (c *InstanceCache) LookupFunc(origin *Func, targs []Type) *Func {
	return c.funcs[instKey{origin, c.argsKey(targs)}]
}

// NamedInstances returns every interface instance in creation order,
// including non-concrete ones.
func (c *InstanceCache) NamedInstances() []*Named { return slices.Clone(c.namedList) }

// FuncInstances returns every function instance in creation order,
// including non-concrete ones.
func (c *InstanceCache) FuncInstances() []*Func { return slices.Clone(c.funcList) }

// Errors returns the instantiation failures that happened inside Subst
// (including lazy field expansion), in order. Subst replaces a failed
// instance by Typ[Invalid]; the checker reports these errors (E-GENERIC).
func (c *InstanceCache) Errors() []error { return slices.Clone(c.errs) }

// Subst returns t with each tparams[i] replaced by targs[i]. Instances
// whose type arguments change are re-instantiated through the cache (so the
// result is canonical); Array and Optional are rebuilt only when their
// element changes (NewOptional collapses double optionals); a Signature
// gets fresh parameter Vars. Types that do not mention tparams are
// returned unchanged. len(tparams) must equal len(targs).
func (c *InstanceCache) Subst(t Type, tparams []*TypeParam, targs []Type) Type {
	if len(tparams) == 0 || t == nil {
		return t
	}
	s := substituter{c: c, tparams: tparams, targs: targs}
	return s.typ(t)
}

type substituter struct {
	c       *InstanceCache
	tparams []*TypeParam
	targs   []Type
}

func (s *substituter) typ(t Type) Type {
	switch t := t.(type) {
	case *TypeParam:
		for i, tp := range s.tparams {
			if tp == t {
				return s.targs[i]
			}
		}
		return t
	case *Array:
		if e := s.typ(t.Elem); e != t.Elem {
			return NewArray(e)
		}
		return t
	case *Optional:
		if e := s.typ(t.Elem); e != t.Elem {
			return NewOptional(e)
		}
		return t
	case *Named:
		if t.Origin == nil {
			return t
		}
		args := make([]Type, len(t.TypeArgs))
		changed := false
		for i, a := range t.TypeArgs {
			args[i] = s.typ(a)
			changed = changed || args[i] != a
		}
		if !changed {
			return t
		}
		inst, err := s.c.InstantiateNamed(t.Origin, args)
		if err != nil {
			s.c.errs = append(s.c.errs, err)
			return Typ[Invalid]
		}
		return inst
	case *Signature:
		return s.signature(t, false)
	}
	return t
}

// signature substitutes a signature. dropTParams removes the type
// parameters (instantiation); otherwise they are kept unless substituted.
func (s *substituter) signature(sig *Signature, dropTParams bool) *Signature {
	out := &Signature{Result: s.typ(sig.Result)}
	if sig.Recv != nil {
		out.Recv = s.variable(sig.Recv)
	}
	out.Params = make([]*Var, len(sig.Params))
	for i, p := range sig.Params {
		out.Params[i] = s.variable(p)
	}
	if !dropTParams {
		for _, tp := range sig.TypeParams {
			if !slices.Contains(s.tparams, tp) {
				out.TypeParams = append(out.TypeParams, tp)
			}
		}
	}
	return out
}

func (s *substituter) variable(v *Var) *Var {
	nv := *v
	nv.Type = s.typ(v.Type)
	nv.Origin = v
	return &nv
}

// TypeDepth returns the nesting depth of t: 1 for basic types and type
// parameters, 1 + the element depth for arrays and optionals, 1 + the
// deepest type argument for instances (1 for plain interfaces).
func TypeDepth(t Type) int {
	switch t := t.(type) {
	case *Array:
		return 1 + TypeDepth(t.Elem)
	case *Optional:
		return 1 + TypeDepth(t.Elem)
	case *Named:
		d := 0
		for _, a := range t.TypeArgs {
			d = max(d, TypeDepth(a))
		}
		return 1 + d
	}
	return 1
}

// id returns a small stable number for a pointer, assigned on first sight.
func (c *InstanceCache) id(p any) int {
	if n, ok := c.ids[p]; ok {
		return n
	}
	n := len(c.ids) + 1
	c.ids[p] = n
	return n
}

func (c *InstanceCache) argsKey(targs []Type) string {
	var b strings.Builder
	for i, a := range targs {
		if i > 0 {
			b.WriteByte(',')
		}
		c.writeKey(&b, a)
	}
	return b.String()
}

// writeKey writes a string that identifies t up to Identical.
func (c *InstanceCache) writeKey(b *strings.Builder, t Type) {
	switch t := t.(type) {
	case *Basic:
		b.WriteString("B" + strconv.Itoa(int(t.Kind)))
	case *Named:
		if t.Origin != nil {
			b.WriteString("N" + strconv.Itoa(c.id(t.Origin)) + "<" + c.argsKey(t.TypeArgs) + ">")
		} else {
			b.WriteString("N" + strconv.Itoa(c.id(t)))
		}
	case *Array:
		b.WriteString("[")
		c.writeKey(b, t.Elem)
		b.WriteString("]")
	case *Optional:
		b.WriteString("?")
		c.writeKey(b, t.Elem)
	case *TypeParam:
		b.WriteString("P" + strconv.Itoa(c.id(t)))
	case nil:
		b.WriteString("nil")
	default:
		b.WriteString("X" + strconv.Itoa(c.id(t)))
	}
}
