package codegen

import (
	"strings"
	"testing"

	"tlang/ast"
	"tlang/checker"
	"tlang/parser"
	"tlang/token"
	"tlang/types"
)

// testFile is the file name test programs are parsed under.
const testFile = "test.tl"

// checkSrc parses and checks src, failing the test on any parser or checker
// error (warnings are allowed, as for Emit), and returns the tree and its
// Info.
func checkSrc(t *testing.T, src string) (*ast.Program, *types.Info) {
	t.Helper()
	prog, pdiags := parser.ParseSource(testFile, []byte(src))
	if pdiags.HasErrors() {
		t.Fatalf("parser errors:\n%s\nsource:\n%s", pdiags.Error(), src)
	}
	info, cdiags := checker.Check(prog)
	if cdiags.HasErrors() {
		t.Fatalf("checker errors:\n%s\nsource:\n%s", cdiags.Error(), src)
	}
	return prog, info
}

// emitErr runs Emit on a checked program and returns its error text,
// failing the test when Emit succeeds or returns bytes with the error.
func emitErr(t *testing.T, prog *ast.Program, info *types.Info) string {
	t.Helper()
	out, err := Emit(prog, info)
	if err == nil {
		t.Fatalf("Emit succeeded, want an error")
	}
	if len(out) != 0 {
		t.Fatalf("Emit returned %d bytes with error %v, want none", len(out), err)
	}
	return err.Error()
}

// emitPastPass1 runs Emit on a program that passes pass 1 but may still hit a
// construct a later feature lowers. It fails the test only on a §12
// rejection (an "unsupported input:" error), which would mean pass 1 wrongly
// rejected the input; a clean emit or a "not implemented yet" / "internal
// error" from a later pass both count as pass 1 having accepted the program.
func emitPastPass1(t *testing.T, prog *ast.Program, info *types.Info) {
	t.Helper()
	_, err := Emit(prog, info)
	if err != nil && strings.Contains(err.Error(), prefixUnsupported) {
		t.Fatalf("pass 1 rejected an accepted program: %s", err)
	}
}

// mustEmit parses, checks and emits src, failing the test on any front-end
// or Emit error, and returns the generated C as a string.
func mustEmit(t *testing.T, src string) string {
	t.Helper()
	prog, info := checkSrc(t, src)
	out, err := Emit(prog, info)
	if err != nil {
		t.Fatalf("Emit error: %v\nsource:\n%s", err, src)
	}
	return string(out)
}

// testGenerator returns a generator for unit tests of its helpers.
func testGenerator() *generator {
	return &generator{prog: &ast.Program{File: testFile}, info: types.NewInfo(), file: testFile}
}

// catch runs f and returns the error it aborted with, through
// generator.fail or any other panic, or nil when it returns normally.
func catch(g *generator, f func()) (err *cgError) {
	defer func() {
		if r := recover(); r != nil {
			err = g.recovered(r)
		}
	}()
	f()
	return nil
}

// mustPanic runs f and fails the test unless it panics with a message
// containing want.
func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatalf("no panic, want one containing %q", want)
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Fatalf("panic %v, want one containing %q", r, want)
		}
	}()
	f()
}

// pos returns the position line:col (Offset is not used by codegen).
func pos(line, col int) token.Position { return token.Position{Line: line, Column: col} }

// testVar returns a local variable named name.
func testVar(name string) *types.Var { return &types.Var{Name: name, Kind: types.LocalVar} }

// testNamed returns a plain interface type named name with the given
// fields, declared at p.
func testNamed(name string, p token.Position, fields ...*types.Var) *types.Named {
	n := types.NewNamed(&types.TypeName{Name: name, Pos: p}, nil, nil)
	n.SetFields(fields)
	return n
}
