package types

import "sort"

// universe holds the predeclared objects. They are shared, read-only
// singletons.
var universe = map[string]Object{}

func init() {
	for _, k := range []BasicKind{Int32, Int64, Float64, Bool, String, Void, Context, Error, Transaction} {
		universe[Typ[k].Name] = &TypeName{Name: Typ[k].Name, Type: Typ[k]}
	}
}

// LookupUniverse returns the predeclared object named name, or nil:
//
//	type names  int32 int64 float64 bool string void Context Error Transaction
//	            (*TypeName whose Type is the Typ singleton; int32, int64 and
//	            float64 are also callable as conversions, see ConversionOf)
//
// true, false and null are keywords, not objects.
func LookupUniverse(name string) Object { return universe[name] }

// UniverseNames returns the predeclared names in sorted order.
func UniverseNames() []string {
	names := make([]string, 0, len(universe))
	for n := range universe {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
