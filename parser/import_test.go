package parser

import (
	"strings"
	"testing"

	"tlang/ast"
	"tlang/diag"
)

// hasCode reports whether the list carries a diagnostic with the given code.
func hasCode(dg *diag.List, code string) bool {
	for _, d := range dg.Items {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestImportForms(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`import def from "mod";`, `import def from "mod";`},
		{`import { A } from "mod";`, `import { A } from "mod";`},
		{`import { A, B } from "mod";`, `import { A, B } from "mod";`},
		{`import { A as X, B } from "mod";`, `import { A as X, B } from "mod";`},
		{`import def, { A, B as C } from "mod";`, `import def, { A, B as C } from "mod";`},
		{`import * as m from "mod";`, `import * as m from "mod";`},
	}
	for _, c := range cases {
		prog, dg := parseSrc(t, c.src)
		if dg.Len() != 0 {
			t.Errorf("%q: diagnostics: %s", c.src, dg.Error())
			continue
		}
		if len(prog.Statements) != 1 {
			t.Errorf("%q: want 1 statement, got %d", c.src, len(prog.Statements))
			continue
		}
		if _, ok := prog.Statements[0].(*ast.ImportDecl); !ok {
			t.Errorf("%q: want *ast.ImportDecl, got %T", c.src, prog.Statements[0])
			continue
		}
		got := strings.TrimRight(prog.String(), "\n")
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestImportFields(t *testing.T) {
	prog, dg := parseSrc(t, `import def, { A, B as C } from "mod";`)
	if dg.Len() != 0 {
		t.Fatalf("diagnostics: %s", dg.Error())
	}
	imp := prog.Statements[0].(*ast.ImportDecl)
	if imp.Default == nil || imp.Default.Name != "def" {
		t.Errorf("default = %v, want def", imp.Default)
	}
	if imp.From != "mod" {
		t.Errorf("from = %q, want mod", imp.From)
	}
	if len(imp.Named) != 2 {
		t.Fatalf("named count = %d, want 2", len(imp.Named))
	}
	if imp.Named[0].Name.Name != "A" || imp.Named[0].Alias != nil {
		t.Errorf("spec 0 = %v", imp.Named[0])
	}
	if imp.Named[1].Name.Name != "B" || imp.Named[1].Alias == nil || imp.Named[1].Alias.Name != "C" {
		t.Errorf("spec 1 = %v", imp.Named[1])
	}
}

func TestReExport(t *testing.T) {
	prog, dg := parseSrc(t, `export { A, B as C } from "mod";`)
	if dg.Len() != 0 {
		t.Fatalf("diagnostics: %s", dg.Error())
	}
	if len(prog.Statements) != 1 {
		t.Fatalf("want 1 statement, got %d", len(prog.Statements))
	}
	re, ok := prog.Statements[0].(*ast.ReExportDecl)
	if !ok {
		t.Fatalf("want *ast.ReExportDecl, got %T", prog.Statements[0])
	}
	if re.From != "mod" || len(re.Specs) != 2 {
		t.Errorf("re-export = %+v", re)
	}
	if got := strings.TrimRight(prog.String(), "\n"); got != `export { A, B as C } from "mod";` {
		t.Errorf("render = %q", got)
	}
}

func TestImportsFirst(t *testing.T) {
	// An import after a top-level declaration is E-PARSE "imports must come first".
	src := `fn main(): void { return; }
import { A } from "mod";`
	_, dg := parseSrc(t, src)
	if !hasCode(dg, diag.CodeParse) {
		t.Errorf("import after decl: expected E-PARSE")
	}
	found := false
	for _, d := range dg.Items {
		if strings.Contains(d.Message, "imports must come first") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'imports must come first', got %s", dg.Error())
	}
}

func TestReExportAfterDecl(t *testing.T) {
	src := `const k = 1;
export { A } from "mod";`
	_, dg := parseSrc(t, src)
	found := false
	for _, d := range dg.Items {
		if strings.Contains(d.Message, "imports must come first") {
			found = true
		}
	}
	if !found {
		t.Errorf("re-export after decl: expected 'imports must come first', got %s", dg.Error())
	}
}

func TestImportsFirstMultiple(t *testing.T) {
	// Several imports followed by declarations parse cleanly.
	src := `import { A } from "a";
import def from "b";
export { C } from "c";
fn main(): void { return; }`
	prog, dg := parseSrc(t, src)
	if dg.Len() != 0 {
		t.Fatalf("diagnostics: %s", dg.Error())
	}
	if len(prog.Statements) != 4 {
		t.Fatalf("want 4 statements, got %d", len(prog.Statements))
	}
	if _, ok := prog.Statements[0].(*ast.ImportDecl); !ok {
		t.Errorf("stmt 0 = %T, want *ast.ImportDecl", prog.Statements[0])
	}
	if _, ok := prog.Statements[2].(*ast.ReExportDecl); !ok {
		t.Errorf("stmt 2 = %T, want *ast.ReExportDecl", prog.Statements[2])
	}
	if _, ok := prog.Statements[3].(*ast.FunctionStatement); !ok {
		t.Errorf("stmt 3 = %T, want *ast.FunctionStatement", prog.Statements[3])
	}
}

func TestImportEmptyList(t *testing.T) {
	_, dg := parseSrc(t, `import {} from "mod";`)
	if !hasCode(dg, codeImport) {
		t.Errorf("empty list: expected E-IMPORT, got %s", dg.Error())
	}
}

func TestImportDuplicateName(t *testing.T) {
	// A duplicate local name within one statement is E-IMPORT.
	_, dg := parseSrc(t, `import { A, A } from "mod";`)
	if !hasCode(dg, codeImport) {
		t.Errorf("duplicate name: expected E-IMPORT, got %s", dg.Error())
	}
	// A duplicate created by an alias also counts.
	_, dg2 := parseSrc(t, `import { A, B as A } from "mod";`)
	if !hasCode(dg2, codeImport) {
		t.Errorf("duplicate via alias: expected E-IMPORT, got %s", dg2.Error())
	}
	// Distinct names are fine.
	_, dg3 := parseSrc(t, `import { A, B as C } from "mod";`)
	if dg3.Len() != 0 {
		t.Errorf("distinct names: unexpected diagnostics: %s", dg3.Error())
	}
}

func TestImportNamespaceNoMix(t *testing.T) {
	// A namespace import may not combine with a named or default import.
	_, dg := parseSrc(t, `import * as m, { X } from "mod";`)
	if !hasCode(dg, diag.CodeParse) {
		t.Errorf("namespace + named: expected E-PARSE, got %s", dg.Error())
	}
	_, dg2 := parseSrc(t, `import def, * as m from "mod";`)
	if !hasCode(dg2, diag.CodeParse) {
		t.Errorf("default + namespace: expected E-PARSE, got %s", dg2.Error())
	}
}

func TestBareReExport(t *testing.T) {
	// A bare "export { ... }" with no "from" is E-PARSE "re-export requires `from`".
	_, dg := parseSrc(t, `export { A, B };`)
	found := false
	for _, d := range dg.Items {
		if d.Code == diag.CodeParse && strings.Contains(d.Message, "re-export requires") {
			found = true
		}
	}
	if !found {
		t.Errorf("bare re-export: expected 're-export requires `from`', got %s", dg.Error())
	}
}

func TestExportDefaultForms(t *testing.T) {
	cases := []struct {
		src       string
		isDefault bool
	}{
		{`export fn f(): void { return; }`, false},
		{`export default fn f(): void { return; }`, true},
		{`export interface I { a: int64 }`, false},
		{`export default interface I { a: int64 }`, true},
		{`export type T = int64;`, false},
		{`export default type T = int64;`, true},
		{`export const k = 1;`, false},
		{`export default const k = 1;`, true},
	}
	for _, c := range cases {
		prog, dg := parseSrc(t, c.src)
		if dg.Len() != 0 {
			t.Errorf("%q: diagnostics: %s", c.src, dg.Error())
			continue
		}
		if len(prog.Statements) != 1 {
			t.Errorf("%q: want 1 statement, got %d", c.src, len(prog.Statements))
			continue
		}
		exported, isDefault := declExportFlags(prog.Statements[0])
		if !exported {
			t.Errorf("%q: Exported = false, want true", c.src)
		}
		if isDefault != c.isDefault {
			t.Errorf("%q: IsDefault = %v, want %v", c.src, isDefault, c.isDefault)
		}
	}
}

func TestExportDefaultExpressionRejected(t *testing.T) {
	// "export default <expression>" is not a named declaration.
	_, dg := parseSrc(t, `export default 42;`)
	found := false
	for _, d := range dg.Items {
		if d.Code == diag.CodeParse && strings.Contains(d.Message, "default export must be a named declaration") {
			found = true
		}
	}
	if !found {
		t.Errorf("export default 42: expected named-declaration error, got %s", dg.Error())
	}
}

// declExportFlags returns (Exported, IsDefault) for a top-level named
// declaration statement.
func declExportFlags(s ast.Statement) (bool, bool) {
	switch d := s.(type) {
	case *ast.FunctionStatement:
		return d.Exported, d.IsDefault
	case *ast.InterfaceStatement:
		return d.Exported, d.IsDefault
	case *ast.TypeAliasStatement:
		return d.Exported, d.IsDefault
	case *ast.LetStatement:
		return d.Exported, d.IsDefault
	}
	return false, false
}

func TestDottedType(t *testing.T) {
	cases := []struct {
		typ  string
		want string
	}{
		{"m.User", "m.User"},
		{"m.Page<User>", "m.Page<User>"},
		{"m.Page<m.User>", "m.Page<m.User>"},
	}
	for _, c := range cases {
		src := "let v: " + c.typ + " = x;"
		prog, dg := parseSrc(t, src)
		if dg.Len() != 0 {
			t.Errorf("%q: diagnostics: %s", c.typ, dg.Error())
			continue
		}
		typ := prog.Statements[0].(*ast.LetStatement).Type
		named, ok := typ.(*ast.NamedType)
		if !ok {
			t.Errorf("%q: want *ast.NamedType, got %T", c.typ, typ)
			continue
		}
		if named.Qualifier == nil {
			t.Errorf("%q: Qualifier is nil", c.typ)
		}
		if got := named.String(); got != c.want {
			t.Errorf("%q: got %q, want %q", c.typ, got, c.want)
		}
	}
}

func TestDottedTypeMalformedRecovery(t *testing.T) {
	// "m." with no member name is malformed and recovers with E-PARSE.
	_, dg := parseSrc(t, "let v: m. = x;")
	if !hasCode(dg, diag.CodeParse) {
		t.Errorf("malformed dotted type: expected E-PARSE, got %s", dg.Error())
	}
}

func TestUnqualifiedTypeRendersSame(t *testing.T) {
	// A single-identifier type still has a nil Qualifier and renders without
	// a dot (byte-identical to before the Qualifier field existed).
	prog, dg := parseSrc(t, "let v: User = x;")
	if dg.Len() != 0 {
		t.Fatalf("diagnostics: %s", dg.Error())
	}
	named := prog.Statements[0].(*ast.LetStatement).Type.(*ast.NamedType)
	if named.Qualifier != nil {
		t.Errorf("Qualifier = %v, want nil", named.Qualifier)
	}
	if named.String() != "User" {
		t.Errorf("render = %q, want User", named.String())
	}
}
