package checker

import "testing"

// escape_test.go covers the region/escape analysis of pass 4: a request-region
// value stored into a global is E-ESCAPE, whether by assignment, by a global
// initializer, or by a push onto a global array; and a global-region value
// stored globally is fine.

func TestEscapeNewInGlobalInit(t *testing.T) {
	// A plain new in a global initializer produces a request-region value.
	src := `
interface Box { n: int64; }
let b: Box = new Box();
`
	_, diags := check(t, src)
	if !hasCode(diags, "E-ESCAPE") {
		t.Fatalf("want E-ESCAPE for a plain new in a global initializer, got %s", diags.Error())
	}
}

func TestEscapeGlobalNewAllowed(t *testing.T) {
	// new global allocates a global-region value, which may be stored
	// globally.
	src := `
interface Box { n: int64; }
let b: Box = new global Box();
`
	_, diags := check(t, src)
	if hasCode(diags, "E-ESCAPE") {
		t.Fatalf("new global in a global initializer should not escape: %s", diags.Error())
	}
}

func TestEscapeRequestIntoGlobalAssign(t *testing.T) {
	// Assigning a request-region value (a plain new) into a global variable
	// inside a body is E-ESCAPE.
	src := `
interface Box { n: int64; }
let g: Box = new global Box();
fn f(): void { g = new Box(); }
`
	_, diags := check(t, src)
	if !hasCode(diags, "E-ESCAPE") {
		t.Fatalf("want E-ESCAPE for storing a request value into a global, got %s", diags.Error())
	}
}

func TestEscapeGlobalArrayPush(t *testing.T) {
	// Pushing a request-region value onto a global array escapes.
	src := `
interface Box { n: int64; }
let xs: Box[] = new global Box[]();
fn f(): void { xs.push(new Box()); }
`
	_, diags := check(t, src)
	if !hasCode(diags, "E-ESCAPE") {
		t.Fatalf("want E-ESCAPE for pushing a request value onto a global array, got %s", diags.Error())
	}
}

func TestEscapeLocalStoreAllowed(t *testing.T) {
	// Storing a request value into a local is fine (the local is request
	// region too).
	src := `
interface Box { n: int64; }
fn f(): void { let b: Box = new Box(); b = new Box(); }
`
	_, diags := check(t, src)
	if hasCode(diags, "E-ESCAPE") {
		t.Fatalf("storing a request value into a local should not escape: %s", diags.Error())
	}
}

func TestEscapeStaticLiteralGlobal(t *testing.T) {
	// A string literal is static region and may initialize a global.
	src := `let s: string = "hello";`
	_, diags := check(t, src)
	if hasCode(diags, "E-ESCAPE") {
		t.Fatalf("a static literal global should not escape: %s", diags.Error())
	}
}
