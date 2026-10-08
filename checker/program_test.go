package checker

import (
	"os"
	"path/filepath"
	"testing"

	"tlang/module"
	"tlang/parser"
	"tlang/types"
)

func TestVirtualImportsAndNestedNamespace(t *testing.T) {
	info, codes := checkProgram(t, map[string]string{"main.ts": `import * as system from "tlang/system"; fn main(): void { system.console.info("ok"); }`}, "main.ts")
	if len(codes) != 0 {
		t.Fatalf("unexpected diagnostics: %v", codes)
	}
	if len(info.Calls) != 1 {
		t.Fatalf("calls = %d, want one builtin call", len(info.Calls))
	}
}

// checkProgram writes files into a temp dir, builds the module graph rooted
// at root, and runs CheckProgram. It fails the test on a graph-build error
// (unresolved specifier, lex/parse error) so a semantic test never masks a
// front-end mistake unless it asserts one. root is a repo-relative name
// (e.g. "main.ts").
func checkProgram(t *testing.T, files map[string]string, root string) (*types.Info, []string) {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	graph, bdiags := module.Build(filepath.Join(dir, filepath.FromSlash(root)))
	if bdiags.HasErrors() {
		t.Fatalf("module.Build %s: unexpected errors: %s", root, bdiags.Error())
	}
	info, diags := CheckProgram(graph.Modules)
	return info, codesOf(diags)
}

// checkProgramDiags is checkProgram but returns the full *diag list codes
// including when module.Build itself reports (it does not fail on build
// errors). Used by tests that assert E-IMPORT from resolution.
func checkProgramRaw(t *testing.T, files map[string]string, root string) (*types.Info, []string) {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	graph, _ := module.Build(filepath.Join(dir, filepath.FromSlash(root)))
	_, diags := CheckProgram(graph.Modules)
	return nil, codesOf(diags)
}

func wantEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("diagnostics: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("diagnostics: got %v, want %v", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Single-module byte-equivalence (AC-28)

// A one-module import-free program routed through CheckProgram produces the
// same model (and no diagnostics) a direct Check would, with every tag "".
func TestCheckProgramSingleModuleEquivalent(t *testing.T) {
	src := `
interface User { id: int64; name: string; }
let count: int64 = 0;
fn greet(u: User): string { return u.name; }
fn main(): void {}
`
	prog, pdiags := parser.ParseSource("main.ts", []byte(src))
	if pdiags.HasErrors() {
		t.Fatalf("parse: %s", pdiags.Error())
	}
	infoCheck, dCheck := Check(prog)
	if dCheck.HasErrors() {
		t.Fatalf("Check: %s", dCheck.Error())
	}

	info, codes := checkProgram(t, map[string]string{"main.ts": src}, "main.ts")
	wantEqual(t, codes, nil)

	if info.Kind != infoCheck.Kind {
		t.Fatalf("Kind: CheckProgram %v, Check %v", info.Kind, infoCheck.Kind)
	}
	if len(info.Globals) != len(infoCheck.Globals) {
		t.Fatalf("Globals: CheckProgram %d, Check %d", len(info.Globals), len(infoCheck.Globals))
	}
	// Every tag must be empty for a single-file program (byte-identical C).
	for _, g := range info.Globals {
		if g.Tag != "" {
			t.Fatalf("single-module global %s has non-empty tag %q", g.Name, g.Tag)
		}
	}
	for _, f := range info.Funcs {
		if f.Tag != "" {
			t.Fatalf("single-module func %s has non-empty tag %q", f.Name, f.Tag)
		}
	}
}

// ---------------------------------------------------------------------------
// Import tables and cross-module calls

func TestCheckProgramNamedImportCall(t *testing.T) {
	files := map[string]string{
		"util.ts": `export fn helper(): int64 { return 7; }`,
		"main.ts": `
import { helper } from "./util";
fn main(): void { let x: int64 = helper(); }
`,
	}
	_, codes := checkProgram(t, files, "main.ts")
	wantEqual(t, codes, nil)
}

func TestCheckProgramNotExported(t *testing.T) {
	files := map[string]string{
		"util.ts": `fn helper(): int64 { return 7; }`, // declared, NOT exported
		"main.ts": `
import { helper } from "./util";
fn main(): void {}
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	// FR-16: a declared-but-not-exported name is "not exported".
	wantEqual(t, codes, []string{"E-IMPORT"})
}

func TestCheckProgramNoSuchMember(t *testing.T) {
	files := map[string]string{
		"util.ts": `export fn helper(): int64 { return 7; }`,
		"main.ts": `
import { nope } from "./util";
fn main(): void {}
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	// FR-17: a name the module has no declaration for is "no exported member".
	wantEqual(t, codes, []string{"E-IMPORT"})
}

func TestCheckProgramAliasedImport(t *testing.T) {
	files := map[string]string{
		"util.ts": `export fn helper(): int64 { return 7; }`,
		"main.ts": `
import { helper as h } from "./util";
fn main(): void { let x: int64 = h(); }
`,
	}
	_, codes := checkProgram(t, files, "main.ts")
	wantEqual(t, codes, nil)
}

// ---------------------------------------------------------------------------
// Namespace import, type position, m-as-value

func TestCheckProgramNamespaceCallAndType(t *testing.T) {
	files := map[string]string{
		"models.ts": `
export interface User { id: int64; name: string; }
export fn make(): int64 { return 1; }
`,
		"main.ts": `
import * as m from "./models";
fn use(u: m.User): string { return u.name; }
fn main(): void { let x: int64 = m.make(); }
`,
	}
	_, codes := checkProgram(t, files, "main.ts")
	wantEqual(t, codes, nil)
}

func TestCheckProgramNamespaceAsValue(t *testing.T) {
	files := map[string]string{
		"models.ts": `export fn make(): int64 { return 1; }`,
		"main.ts": `
import * as m from "./models";
fn main(): void { let x: int64 = m; }
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	// m used as a value is E-TYPE (a namespace, not a value), plus the
	// let-type mismatch is suppressed by the invalid recovery.
	if len(codes) == 0 || codes[0] != "E-TYPE" {
		t.Fatalf("want E-TYPE for namespace-as-value, got %v", codes)
	}
}

func TestCheckProgramQualifiedTypeUnknownMember(t *testing.T) {
	files := map[string]string{
		"models.ts": `export interface User { id: int64; }`,
		"main.ts": `
import * as m from "./models";
fn use(u: m.Nope): void {}
fn main(): void {}
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	wantEqual(t, codes, []string{"E-IMPORT"})
}

// ---------------------------------------------------------------------------
// Default imports

func TestCheckProgramDefaultImport(t *testing.T) {
	files := map[string]string{
		"id.ts": `export default fn identity(): int64 { return 1; }`,
		"main.ts": `
import id from "./id";
fn main(): void { let x: int64 = id(); }
`,
	}
	_, codes := checkProgram(t, files, "main.ts")
	wantEqual(t, codes, nil)
}

func TestCheckProgramDefaultTypeImport(t *testing.T) {
	files := map[string]string{
		"user.ts": `export default interface User { id: int64; }`,
		"main.ts": `
import U from "./user";
fn use(u: U): int64 { return u.id; }
fn main(): void {}
`,
	}
	_, codes := checkProgram(t, files, "main.ts")
	wantEqual(t, codes, nil)
}

func TestCheckProgramDefaultImportNoDefault(t *testing.T) {
	files := map[string]string{
		"util.ts": `export fn helper(): int64 { return 1; }`,
		"main.ts": `
import d from "./util";
fn main(): void {}
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	wantEqual(t, codes, []string{"E-IMPORT"})
}

func TestCheckProgramTwoDefaultExports(t *testing.T) {
	files := map[string]string{
		"two.ts": `
export default fn a(): void {}
export default fn b(): void {}
`,
		"main.ts": `
import x from "./two";
fn main(): void {}
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	if len(codes) == 0 || codes[0] != "E-IMPORT" {
		t.Fatalf("want E-IMPORT for a second default export, got %v", codes)
	}
}

// ---------------------------------------------------------------------------
// Re-exports

func TestCheckProgramReExportChain(t *testing.T) {
	files := map[string]string{
		"a.ts": `export fn deep(): int64 { return 1; }`,
		"b.ts": `export { deep } from "./a";`,
		"main.ts": `
import { deep } from "./b";
fn main(): void { let x: int64 = deep(); }
`,
	}
	_, codes := checkProgram(t, files, "main.ts")
	wantEqual(t, codes, nil)
}

func TestCheckProgramReExportAliased(t *testing.T) {
	files := map[string]string{
		"a.ts": `export fn deep(): int64 { return 1; }`,
		"b.ts": `export { deep as shallow } from "./a";`,
		"main.ts": `
import { shallow } from "./b";
fn main(): void { let x: int64 = shallow(); }
`,
	}
	_, codes := checkProgram(t, files, "main.ts")
	wantEqual(t, codes, nil)
}

func TestCheckProgramReExportCycle(t *testing.T) {
	files := map[string]string{
		"a.ts": `export { x } from "./b";`,
		"b.ts": `export { x } from "./a";`,
		"main.ts": `
import { x } from "./a";
fn main(): void {}
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	if len(codes) == 0 {
		t.Fatalf("want an E-IMPORT for a re-export cycle, got none")
	}
	found := false
	for _, c := range codes {
		if c == "E-IMPORT" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want E-IMPORT, got %v", codes)
	}
}

// ---------------------------------------------------------------------------
// Local-vs-import collision

func TestCheckProgramLocalVsImport(t *testing.T) {
	files := map[string]string{
		"util.ts": `export fn helper(): int64 { return 1; }`,
		"main.ts": `
import { helper } from "./util";
fn helper(): int64 { return 2; }
fn main(): void {}
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	if len(codes) == 0 || codes[0] != "E-NAME" {
		t.Fatalf("want E-NAME for a local-vs-import collision, got %v", codes)
	}
}

// ---------------------------------------------------------------------------
// Whole-program entry

func TestCheckProgramEntryInNonRoot(t *testing.T) {
	files := map[string]string{
		"lib.ts": `
export fn helper(): int64 { return 1; }
fn main(): void {}
`,
		"main.ts": `
import { helper } from "./lib";
fn start(): int64 { return helper(); }
`,
	}
	info, codes := checkProgram(t, files, "main.ts")
	wantEqual(t, codes, nil)
	if info.Kind != types.ProgramScript {
		t.Fatalf("entry in a non-root module should still be found; Kind = %v", info.Kind)
	}
}

func TestCheckProgramZeroEntryIsLibrary(t *testing.T) {
	files := map[string]string{
		"util.ts": `export fn helper(): int64 { return 1; }`,
		"main.ts": `
import { helper } from "./util";
fn start(): int64 { return helper(); }
`,
	}
	info, codes := checkProgram(t, files, "main.ts")
	wantEqual(t, codes, nil)
	if info.Kind != types.ProgramUnknown {
		t.Fatalf("no entry anywhere should be ProgramUnknown, got %v", info.Kind)
	}
}

func TestCheckProgramTwoMainsAcrossModules(t *testing.T) {
	files := map[string]string{
		"a.ts": `
export fn ping(): void {}
fn main(): void {}
`,
		"main.ts": `
import { ping } from "./a";
fn main(): void { ping(); }
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	if len(codes) == 0 || codes[0] != "E-ENTRY" {
		t.Fatalf("two mains in two modules should be E-ENTRY, got %v", codes)
	}
}

func TestCheckProgramTwoDispatchersAcrossModules(t *testing.T) {
	files := map[string]string{
		"a.ts": `
export fn ping(): void {}
fn route_dispatcher(ctx: Context): void {}
`,
		"main.ts": `
import { ping } from "./a";
fn route_dispatcher(ctx: Context): void { ping(); }
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	if len(codes) == 0 || codes[0] != "E-ENTRY" {
		t.Fatalf("two route_dispatchers in two modules should be E-ENTRY, got %v", codes)
	}
}

// ---------------------------------------------------------------------------
// Cross-module and same-module global-init cycles (E-INIT)

func TestCheckProgramGlobalInitCycleCrossModule(t *testing.T) {
	files := map[string]string{
		"a.ts": `
import { b } from "./b";
export let a: int64 = b;
`,
		"b.ts": `
import { a } from "./a";
export let b: int64 = a;
`,
		"main.ts": `
import { a } from "./a";
fn main(): void {}
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	found := false
	for _, c := range codes {
		if c == "E-INIT" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cross-module global-init cycle should be E-INIT, got %v", codes)
	}
}

func TestCheckProgramGlobalInitSelfCycle(t *testing.T) {
	files := map[string]string{
		"main.ts": `
let a: int64 = b;
let b: int64 = a;
fn main(): void {}
`,
	}
	_, codes := checkProgramRaw(t, files, "main.ts")
	found := false
	for _, c := range codes {
		if c == "E-INIT" {
			found = true
		}
	}
	if !found {
		t.Fatalf("same-module backward global-init cycle should be E-INIT, got %v", codes)
	}
}

// ---------------------------------------------------------------------------
// Tag-clash collision (AC-36)

// Two modules each export a global whose tagged C name could clash only if
// tags were empty; with distinct module tags they do NOT clash, so a correct
// two-module program with same-named globals type-checks cleanly.
func TestCheckProgramDistinctTagsNoClash(t *testing.T) {
	files := map[string]string{
		"a.ts": `export let shared: int64 = 1;`,
		"main.ts": `
import { shared as s } from "./a";
let shared: int64 = 2;
fn main(): void {}
`,
	}
	_, codes := checkProgram(t, files, "main.ts")
	wantEqual(t, codes, nil)
}
