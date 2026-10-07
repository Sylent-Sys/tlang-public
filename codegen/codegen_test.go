package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"path/filepath"
	"strings"
	"testing"

	"tlang/ast"
	"tlang/types"
)

func TestEmitNilInputs(t *testing.T) {
	want := "internal error: Emit needs a program and its type information"
	inputs := []struct {
		prog *ast.Program
		info *types.Info
	}{
		{nil, nil},
		{&ast.Program{File: testFile}, nil},
		{nil, types.NewInfo()},
	}
	for _, in := range inputs {
		out, err := Emit(in.prog, in.info)
		if err == nil || err.Error() != want || out != nil {
			t.Errorf("Emit(%v, %v) = %q, %v; want no bytes and %q", in.prog, in.info, out, err, want)
		}
	}
}

// TestEmitFullyLowered checks that F6 closes the last lowering gap: a program
// whose last-implemented construct was ctx.json now emits a complete unit
// (the JSON writer for the value type), with no "not implemented yet" error.
// Emit never writes placeholder C, and it is deterministic (a second call on
// the same tree gives the same bytes).
func TestEmitFullyLowered(t *testing.T) {
	const src = `interface Doc {
    id: int64;
}

fn route_dispatcher(ctx: Context): void {
    let d = new Doc();
    ctx.json(200, d);
}
`
	prog, info := checkSrc(t, src)
	out, err := Emit(prog, info)
	if err != nil {
		t.Fatalf("Emit error: %v", err)
	}
	if !strings.Contains(string(out), "tlj_write_Doc(") {
		t.Fatalf("output does not emit the JSON writer for Doc:\n%s", out)
	}
	// Emit is deterministic and does not consume its input.
	out2, err := Emit(prog, info)
	if err != nil {
		t.Fatalf("second Emit error: %v", err)
	}
	if string(out) != string(out2) {
		t.Fatalf("Emit is not deterministic: outputs differ")
	}
}

// TestEmitRecoversPanics checks the panic-to-error net of codegen design
// §1.2: a malformed tree (here a nil statement, which the parser never
// produces) crashes inside the lowering, and Emit turns that into an
// internal error instead of panicking.
func TestEmitRecoversPanics(t *testing.T) {
	prog := &ast.Program{File: "x.tl", Statements: []ast.Statement{(*ast.FunctionStatement)(nil)}}
	out, err := Emit(prog, types.NewInfo())
	if err == nil || out != nil {
		t.Fatalf("Emit = %q, %v; want no bytes and an error", out, err)
	}
	if !strings.HasPrefix(err.Error(), "x.tl: internal error: runtime error: ") {
		t.Fatalf("got %q, want an internal error from the recovered runtime panic", err)
	}
}

// TestOnlyEmitExported enforces that Emit is the package's only exported
// identifier (codegen design §2): the internal API may change between
// implementation stages without affecting the driver.
func TestOnlyEmitExported(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := gotoken.NewFileSet()
	var exported []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := goparser.ParseFile(fset, name, nil, goparser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *goast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					exported = append(exported, d.Name.Name)
				}
			case *goast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *goast.TypeSpec:
						if s.Name.IsExported() {
							exported = append(exported, s.Name.Name)
						}
					case *goast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								exported = append(exported, n.Name)
							}
						}
					}
				}
			}
		}
	}
	if len(exported) != 1 || exported[0] != "Emit" {
		t.Fatalf("exported identifiers %v, want only Emit", exported)
	}
}
