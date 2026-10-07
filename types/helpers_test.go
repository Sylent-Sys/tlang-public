package types

// Test helpers shared by the types tests.

func iface(name string, tparams ...string) *Named {
	var tps []*TypeParam
	for i, p := range tparams {
		tps = append(tps, NewTypeParam(&TypeName{Name: p}, i))
	}
	return NewNamed(&TypeName{Name: name}, nil, tps)
}

func fieldVar(name string, t Type) *Var { return &Var{Name: name, Type: t} }

func withFields(n *Named, fields ...*Var) *Named {
	n.SetFields(fields)
	return n
}

func mustNamed(c *InstanceCache, origin *Named, args ...Type) *Named {
	n, err := c.InstantiateNamed(origin, args)
	if err != nil {
		panic(err)
	}
	return n
}

var (
	i32  = Typ[Int32]
	i64  = Typ[Int64]
	f64  = Typ[Float64]
	bl   = Typ[Bool]
	str  = Typ[String]
	void = Typ[Void]
	ctx  = Typ[Context]
	errT = Typ[Error]
	tx   = Typ[Transaction]
	uint = Typ[UntypedInt]
	ufl  = Typ[UntypedFloat]
	null = Typ[UntypedNull]
	bad  = Typ[Invalid]
)

func opt(t Type) Type { return NewOptional(t) }
func arr(t Type) Type { return NewArray(t) }
