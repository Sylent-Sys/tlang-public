package checker

import "testing"

// GAP 1b: cross-declaration C-name collisions are rejected in the checker
// with an E-NAME diagnostic (checkCNameCollisions in resolve.go), mirroring
// codegen's nameTable so the SAME programs are rejected, just earlier.

// C1: interface f_logger beside fn logger both mangle to tl_f_logger.
func TestCollisionFunctionVsFieldPrefix(t *testing.T) {
	src := `
interface f_logger { a: int64; }
fn logger(): void {}
fn main(): void {}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-NAME")
	if !hasMessage(diags, "C name collision: tl_f_logger") {
		t.Fatalf("want a tl_f_logger collision, got %s", diags.Error())
	}
}

// C2: a generic interface instance m_User<a> (interface a mangles to "a")
// collides with method User.a; both reach tl_m_User__a.
func TestCollisionGenericInstanceVsMethod(t *testing.T) {
	src := `
interface a { v: int64; }
interface User { id: int64; }
interface m_User<T> { w: T; }

fn (u: User) a(): void {}

fn main(): void {
	let x = new m_User<a>();
}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-NAME")
	if !hasMessage(diags, "C name collision: tl_m_User__a") {
		t.Fatalf("want a tl_m_User__a collision, got %s", diags.Error())
	}
}

// R1: a reserved single-name shape (interface globals) is rejected exactly
// once by CheckDeclName (1a); the collision pass's per-declaration skip
// prevents a duplicate reserved-name diagnostic.
func TestCollisionReservedGlobalsSingleDiagnostic(t *testing.T) {
	src := `
interface globals { x: int64; }
fn main(): void {}
`
	_, diags := check(t, src)
	wantCodes(t, diags, "E-NAME")
	if !hasMessage(diags, "is reserved for generated code") {
		t.Fatalf("want a reserved-name message, got %s", diags.Error())
	}
}

// Negative: two distinct, legal names must not collide (no over-rejection).
func TestCollisionDistinctNamesOK(t *testing.T) {
	src := `
interface Logger { a: int64; }
fn logger(): void {}
fn main(): void {}
`
	_, diags := check(t, src)
	wantCodes(t, diags)
}
