package checker

import (
	"testing"

	"tlang/types"
)

// generics_test.go covers pass 6's monomorphization closure (the concrete
// FuncInstances/Interfaces sets and their order) and the OQ7
// InstanceCache.Errors() attribution (a recursive generic type exceeding
// MaxInstanceDepth reports E-GENERIC at the use site, deduped).

func TestMonomorphizationFuncInstance(t *testing.T) {
	// A generic function called with one concrete type argument yields one
	// concrete instance in FuncInstances.
	src := `
fn id<T>(x: T): T { return x; }
fn main(): void { let n: int64 = id<int64>(1); }
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	if len(info.FuncInstances) != 1 {
		t.Fatalf("want 1 func instance, got %d", len(info.FuncInstances))
	}
	inst := info.FuncInstances[0]
	if inst.Name != "id" || len(inst.TypeArgs) != 1 || !types.IsBasic(inst.TypeArgs[0], types.Int64) {
		t.Fatalf("instance = %s, want id<int64>", inst.FullName())
	}
}

func TestMonomorphizationTransitive(t *testing.T) {
	// A concrete call into a generic function that itself calls another
	// generic function closes transitively.
	src := `
fn inner<T>(x: T): T { return x; }
fn outer<T>(x: T): T { return inner<T>(x); }
fn main(): void { let n: int64 = outer<int64>(1); }
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	names := map[string]bool{}
	for _, inst := range info.FuncInstances {
		names[inst.FullName()] = true
	}
	if !names["outer<int64>"] || !names["inner<int64>"] {
		t.Fatalf("closure should include outer<int64> and inner<int64>, got %v", names)
	}
}

func TestMonomorphizationInterfaceInstance(t *testing.T) {
	// A concrete generic interface used as a type appears in Interfaces.
	src := `
interface Box<T> { value: T; }
fn main(): void { let b: Box<int64> = new Box<int64>(); }
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)
	var found bool
	for _, n := range info.Interfaces {
		if n.Origin != nil && n.Name() == "Box" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Box<int64> instance should be in Interfaces, got %d entries", len(info.Interfaces))
	}
}

func TestGenericRecursionDepthAttributed(t *testing.T) {
	// A recursive generic interface expands past MaxInstanceDepth; the
	// E-GENERIC is attributed to the use site, and dedup keeps it a single
	// diagnostic even though lazy expansion fails repeatedly.
	src := `
interface Tree<T> { kids: Tree<T[]>[]; }
fn main(): void { let t: Tree<int64> = new Tree<int64>(); }
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-GENERIC") {
		t.Fatalf("want E-GENERIC for a recursive generic beyond MaxInstanceDepth, got %s", diags.Error())
	}
	// Dedup: the same (code, position, message) is reported at most once.
	seen := map[string]bool{}
	for _, d := range diags.Items {
		if d.Code != "E-GENERIC" {
			continue
		}
		key := d.Pos.String() + "|" + d.Message
		if seen[key] {
			t.Fatalf("E-GENERIC not deduplicated: %q repeated", key)
		}
		seen[key] = true
	}
}

// instancesNamed returns the concrete instances in info.Interfaces whose
// origin name is want (generic origins and non-matching names excluded).
func instancesNamed(info *types.Info, want string) []*types.Named {
	var out []*types.Named
	for _, n := range info.Interfaces {
		if n.Origin != nil && n.Name() == want {
			out = append(out, n)
		}
	}
	return out
}

func TestSelfReferentialGenericTerminates(t *testing.T) {
	// A self-referential generic with an OPTIONAL self-reference is a legal
	// program (DESIGN §2.4: an optional field breaks the required-field
	// cycle). The monomorphization closure must terminate — before the fix it
	// re-entered forceFields<->seedTypeInstances on the same Node<int64>
	// forever and stack-overflowed — accept it, report no error, and record a
	// single concrete Node<int64> whose next field points at itself.
	src := `
interface Node<T> { next: Node<T> | null; }
fn main(): void { let n: Node<int64> = new Node<int64>(); }
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)

	nodes := instancesNamed(info, "Node")
	if len(nodes) != 1 {
		t.Fatalf("want exactly one concrete Node instance, got %d", len(nodes))
	}
	node := nodes[0]
	next := node.Field("next")
	if next == nil {
		t.Fatal("Node<int64>.next field missing")
	}
	opt, ok := next.Type.(*types.Optional)
	if !ok {
		t.Fatalf("Node<int64>.next = %s, want an optional", next.Type)
	}
	if opt.Elem != node {
		t.Fatalf("Node<int64>.next element = %v, want the same canonical Node<int64> self-pointer %v", opt.Elem, node)
	}
}

func TestMutuallyRecursiveGenericsTerminate(t *testing.T) {
	// Two generics referencing each other through optional fields terminate
	// via the shared visited set; both concrete instances appear exactly once.
	src := `
interface A<T> { b: B<T> | null; }
interface B<T> { a: A<T> | null; }
fn main(): void { let x: A<int64> = new A<int64>(); }
`
	_, info, diags := checkProg(t, src)
	wantCodes(t, diags)

	if as := instancesNamed(info, "A"); len(as) != 1 {
		t.Fatalf("want exactly one concrete A instance, got %d", len(as))
	}
	if bs := instancesNamed(info, "B"); len(bs) != 1 {
		t.Fatalf("want exactly one concrete B instance, got %d", len(bs))
	}
}

func TestNonOptionalSelfRefStillRejected(t *testing.T) {
	// A NON-optional self-referential generic field is a required-field cycle,
	// rejected by checkRequiredFieldCycles in resolve pass 2 (E-INIT), before
	// monomorphization. The termination fix must not accidentally accept it.
	src := `
interface Node<T> { next: Node<T>; }
fn main(): void { let n: Node<int64> = new Node<int64>(); }
`
	_, _, diags := checkProg(t, src)
	if !hasCode(diags, "E-INIT") {
		t.Fatalf("want E-INIT for a non-optional self-referential generic, got %s", diags.Error())
	}
}
