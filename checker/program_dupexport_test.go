package checker

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tlang/diag"
	"tlang/module"
)

// program_dupexport_test.go pins the duplicate-export rule: a module exports
// each name at most once, whichever forms export it (a direct `export`ed
// declaration, `export default`, or a re-export specifier, aliased or not).
// The second export is E-IMPORT with a note at the first, as for a second
// `export default`.

// fullDiags lists the sorted diagnostics as "file:line:col: severity code:
// message".
func fullDiags(d *diag.List) []string {
	var out []string
	for _, item := range d.Sorted() {
		out = append(out, fmt.Sprintf("%s:%s: %s %s: %s", item.File, item.Pos, item.Severity, item.Code, item.Message))
	}
	return out
}

// dupExportLibs are the modules the cases re-export from: b.ts and c.ts each
// export X and Y.
var dupExportLibs = map[string]string{
	"b.ts": "export fn X(): int64 { return 1; }\nexport fn Y(): int64 { return 2; }\n",
	"c.ts": "export fn X(): int64 { return 3; }\nexport fn Y(): int64 { return 4; }\n",
}

// dupExportMain imports X from a.ts so every case reaches a.ts.
const dupExportMain = "import { X } from \"./a\";\nfn main(): void { let v = X(); }\n"

func dupExportFiles(a string) map[string]string {
	files := map[string]string{"a.ts": a, "main.ts": dupExportMain}
	for k, v := range dupExportLibs {
		files[k] = v
	}
	return files
}

func TestCheckProgramDuplicateExport(t *testing.T) {
	cases := []struct {
		name string
		a    string
		want []string
	}{
		{
			name: "two re-exports of the same name",
			a:    "export { X } from \"./b\";\nexport { X } from \"./c\";\n",
			want: []string{
				"a.ts:1:10: note E-IMPORT: first export of `X` here",
				"a.ts:2:10: error E-IMPORT: duplicate export `X`",
			},
		},
		{
			name: "two re-exports of the same name from one module",
			a:    "export { X } from \"./b\";\nexport { X } from \"./b\";\n",
			want: []string{
				"a.ts:1:10: note E-IMPORT: first export of `X` here",
				"a.ts:2:10: error E-IMPORT: duplicate export `X`",
			},
		},
		{
			name: "aliased re-export then plain re-export",
			a:    "export { Y as X } from \"./b\";\nexport { X } from \"./c\";\n",
			want: []string{
				"a.ts:1:15: note E-IMPORT: first export of `X` here",
				"a.ts:2:10: error E-IMPORT: duplicate export `X`",
			},
		},
		{
			name: "re-export then direct export",
			a:    "export { X } from \"./b\";\nexport fn X(): int64 { return 5; }\n",
			want: []string{
				"a.ts:1:10: note E-IMPORT: first export of `X` here",
				"a.ts:2:1: error E-IMPORT: duplicate export `X`",
			},
		},
		{
			name: "aliased re-export then direct export",
			a:    "export { Y as X } from \"./b\";\nexport fn X(): int64 { return 5; }\n",
			want: []string{
				"a.ts:1:15: note E-IMPORT: first export of `X` here",
				"a.ts:2:1: error E-IMPORT: duplicate export `X`",
			},
		},
		{
			name: "re-export then default export of the same name",
			a:    "export { X } from \"./b\";\nexport default fn X(): int64 { return 5; }\n",
			want: []string{
				"a.ts:1:10: note E-IMPORT: first export of `X` here",
				"a.ts:2:1: error E-IMPORT: duplicate export `X`",
			},
		},
		{
			name: "re-export then exported global",
			a:    "export { Y as X } from \"./b\";\nexport let X: int64 = 5;\n",
			want: []string{
				"a.ts:1:15: note E-IMPORT: first export of `X` here",
				"a.ts:2:1: error E-IMPORT: duplicate export `X`",
			},
		},
		{
			// Each later duplicate is reported against the first export.
			name: "three exports of one name",
			a:    "export { X } from \"./b\";\nexport { Y as X } from \"./c\";\nexport fn X(): int64 { return 5; }\n",
			want: []string{
				"a.ts:1:10: note E-IMPORT: first export of `X` here",
				"a.ts:1:10: note E-IMPORT: first export of `X` here",
				"a.ts:2:15: error E-IMPORT: duplicate export `X`",
				"a.ts:3:1: error E-IMPORT: duplicate export `X`",
			},
		},
		{
			// Two declarations of one name stay a redeclaration (E-NAME from
			// collect), not also a duplicate export.
			name: "two direct exports are a redeclaration only",
			a:    "export fn X(): int64 { return 5; }\nexport fn X(): int64 { return 6; }\n",
			want: []string{
				"a.ts:1:11: note E-NAME: previous declaration of `X`",
				"a.ts:2:11: error E-NAME: `X` redeclared",
			},
		},
		{
			name: "distinct exported names are accepted",
			a:    "export { X as Z } from \"./b\";\nexport { Y } from \"./c\";\nexport fn X(): int64 { return 5; }\n",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fullDiags(checkProgramList(t, dupExportFiles(tc.a), "main.ts"))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("diagnostics:\n  got  %q\n  want %q", got, tc.want)
			}
		})
	}
}

// TestCheckProgramDuplicateExportSameStatement: two specifiers exporting one
// name in a single re-export statement are a parse error; the checker (which
// the language server runs on a module with parse errors) does not report
// them again.
func TestCheckProgramDuplicateExportSameStatement(t *testing.T) {
	dir := t.TempDir()
	for name, src := range dupExportFiles("export { X, Y as X } from \"./b\";\n") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	graph, bdiags := module.Build(filepath.Join(dir, "main.ts"))
	if !strings.Contains(bdiags.Error(), "duplicate import name \"X\"") {
		t.Fatalf("module.Build diagnostics = %q, want the parser's duplicate", bdiags.Error())
	}
	_, diags := CheckProgram(graph.Modules)
	for _, d := range fullDiags(diags) {
		if strings.Contains(d, "duplicate export") {
			t.Errorf("checker re-reported a same-statement duplicate: %s", d)
		}
	}
}
