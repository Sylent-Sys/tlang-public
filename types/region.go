package types

// Region is the lifetime class of a value for the escape check of
// DESIGN.md §2.10 (spec §6.3 rule 3). Regions form a chain
// RegionNone < RegionStatic < RegionGlobal < RegionRequest, and a value
// whose region is RegionRequest must not be stored into a global variable
// or into a field or element of a global-region object (diag.CodeEscape).
type Region int

const (
	// RegionNone: the value has no lifetime (numbers, bools; HasRegion
	// false).
	RegionNone Region = iota
	// RegionStatic: literals and slices of literals; valid forever.
	RegionStatic
	// RegionGlobal: new global ..., s.clone_global(), global variables and
	// anything read through them; valid for the life of the scheduler.
	RegionGlobal
	// RegionRequest: plain new, clone, concatenation, toString, values from
	// ctx, JSON binding, DB results, parameters, call results and catch
	// bindings; valid until the request's arena is reset.
	RegionRequest
)

// Join returns the longer-lived-is-safer join of r and o: the later of the
// two in the chain None < Static < Global < Request. A local's region is the
// join of the regions of everything assigned to it.
func (r Region) Join(o Region) Region { return max(r, o) }

// String returns "none", "static", "global" or "request".
func (r Region) String() string {
	switch r {
	case RegionNone:
		return "none"
	case RegionStatic:
		return "static"
	case RegionGlobal:
		return "global"
	case RegionRequest:
		return "request"
	}
	return "region?"
}
