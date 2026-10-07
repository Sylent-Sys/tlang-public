package tests

import (
	"os"
	"path/filepath"
	"testing"
)

// seedKind selects which curated seed set addFuzzSeeds feeds to a fuzz target
// (design §5.3). The front-end kinds share one curated golden/reject list; the
// JSON kind seeds from the json_*.tl goldens plus JSON-shaped payloads.
type seedKind int

const (
	// seedFrontEnd seeds FuzzLexer, FuzzParser and FuzzCheck from a curated
	// subset of the golden/reject corpus plus the shared edge seeds.
	seedFrontEnd seedKind = iota
	// seedJSONBind seeds FuzzJSONBind from the json_*.tl goldens plus
	// JSON-shaped edge payloads.
	seedJSONBind
)

// curatedFrontEnd is a small, hand-picked subset of tests/golden/*.tl and
// tests/golden/reject/*.tl (not every file) that exercises real language
// shapes: generics, interfaces, control flow, strings, a server dispatcher and
// a couple of already-known-bad reject inputs the front end must reject without
// crashing (design §5.3). Paths are relative to the tests/ package directory.
var curatedFrontEnd = []string{
	filepath.Join("golden", "generics_basic.tl"),
	filepath.Join("golden", "types_interfaces.tl"),
	filepath.Join("golden", "control_flow.tl"),
	filepath.Join("golden", "strings_basic.tl"),
	filepath.Join("golden", "route_match.tl"),
	filepath.Join("golden", "try_catch.tl"),
	filepath.Join("golden", "skeleton_script.tl"),
	filepath.Join("golden", "reject", "void_let.tl"),
	filepath.Join("golden", "reject", "param_dup.tl"),
}

// curatedJSON is the curated subset of JSON-mapped golden sources for the
// FuzzJSONBind seed set (design §5.3). Paths are relative to tests/.
var curatedJSON = []string{
	filepath.Join("golden", "json_nested.tl"),
	filepath.Join("golden", "json_flags.tl"),
	filepath.Join("golden", "json_top_array.tl"),
}

// edgeSeeds are the hand-written edge inputs shared by the front-end targets
// (design §5.3): empty input, a lone brace, deep nesting, a very long
// identifier, invalid UTF-8, a NUL byte, a deeply-nested (non-recursive)
// Box<...> generic, and the self-referential Node<T> generic — the latter now
// terminates in the checker (FEAT-001), so it is seeded rather than carved out.
// The module-system shapes (FEAT-004 / AC-32) are appended: import/export/from
// in every form plus malformed specifiers, so the import-phase parser is
// fuzzed from the lexer up without crashing or hanging.
func edgeSeeds() [][]byte {
	seeds := [][]byte{
		[]byte(""),
		[]byte("{"),
		[]byte("{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{{"),
		[]byte("fn " + longName() + "(): void {}"),
		{0xff, 0xfe, 0xfd, 0xfc},
		{'f', 'n', ' ', 0x00, '(', ')', ':', ' ', 'v', 'o', 'i', 'd', ' ', '{', '}'},
		[]byte("fn f(): void { let x: Box<Box<Box<Box<Box<int64>>>>>; }"),
		[]byte("interface Node<T> { next: Node<T> | null; }\nfn main(): void { let n: Node<int64> = new Node<int64>(); }\n"),
	}
	return append(seeds, moduleShapeSeeds()...)
}

// moduleShapeSeeds are the import/export/re-export shapes and malformed
// specifiers that exercise the module front end (FEAT-004 / AC-32). The
// well-formed shapes cover a plain named import, an aliased import, a namespace
// import, a default import, a mixed default+named import, an exported
// declaration of each form, a default export, and a re-export (plain and
// aliased). The malformed shapes cover an absolute, bare and root-escaping
// specifier, an empty import list, a duplicate local name, a namespace mixed
// with a named list, a re-export with no `from`, and an import after a
// declaration. None may panic or hang; each yields diagnostics on a bad shape.
func moduleShapeSeeds() [][]byte {
	shapes := []string{
		`import { a } from "./m";`,
		`import { a as b } from "./m";`,
		`import * as m from "./m";`,
		`import d from "./m";`,
		`import d, { a, b as c } from "./m";`,
		"export interface User { id: int64; }\nexport type Id = int64;\nexport let g: int64 = 1;\nexport fn f(): void {}",
		"export default fn run(): void {}",
		`export { a } from "./m";`,
		`export { a as b } from "./m";`,
		// Malformed / rejected shapes.
		`import { a } from "/abs/path";`,
		`import { a } from "bare";`,
		`import { a } from "../escape";`,
		`import {} from "./m";`,
		`import { a, b as a } from "./m";`,
		`import * as m, { a } from "./m";`,
		`export { a } "./m";`,
		"fn first(): void {}\nimport { a } from \"./m\";",
	}
	out := make([][]byte, 0, len(shapes))
	for _, s := range shapes {
		out = append(out, []byte(s))
	}
	return out
}

// edgeJSONSeeds are the hand-written JSON-shaped edge payloads for FuzzJSONBind
// (design §5.3). They are TLang-source bytes defining a JSON-mapped interface
// with a route_dispatcher that binds it, plus degenerate JSON-looking inputs.
func edgeJSONSeeds() [][]byte {
	return [][]byte{
		[]byte(""),
		[]byte("{}"),
		[]byte("[]"),
		[]byte("interface R { id: int64; }\nfn route_dispatcher(ctx: Context): void {\n\tlet r = new R();\n\tif (!ctx.bindJson(r)) { ctx.text(400, \"bad\"); return; }\n\tctx.json(200, r);\n}\n"),
		[]byte("interface R { name: string; vals: int64[]; }\nfn route_dispatcher(ctx: Context): void {\n\tlet r = new R();\n\tctx.bindJson(r);\n\tctx.json(200, r);\n}\n"),
	}
}

// longName returns a very long identifier for the long-identifier edge seed.
func longName() string {
	b := make([]byte, 300)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

// addFuzzSeeds f.Add()s the seeds for kind: a curated subset of golden/reject
// sources read at runtime (reusing the golden/ layout from golden_test.go)
// plus the hand-written edge seeds. Seeds are []byte. Missing curated files are
// skipped rather than failing, so a corpus rename never breaks the fuzz build.
func addFuzzSeeds(f *testing.F, kind seedKind) {
	f.Helper()
	switch kind {
	case seedJSONBind:
		for _, rel := range curatedJSON {
			if src, err := os.ReadFile(rel); err == nil {
				f.Add(src)
			}
		}
		for _, s := range edgeJSONSeeds() {
			f.Add(s)
		}
	default:
		for _, rel := range curatedFrontEnd {
			if src, err := os.ReadFile(rel); err == nil {
				f.Add(src)
			}
		}
		for _, s := range edgeSeeds() {
			f.Add(s)
		}
	}
}
