package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"tlang/checker"
	"tlang/codegen"
	"tlang/lexer"
	"tlang/module"
	"tlang/parser"
	"tlang/token"
)

// jsonHelperRe matches generated JSON helper identifiers (tlj_parse_X /
// tlj_write_X) in emitted C.
var jsonHelperRe = regexp.MustCompile(`tlj_(?:parse|write)_[A-Za-z0-9_]+`)

// jsonHelperNames returns the distinct generated JSON helper names referenced
// in the emitted C.
func jsonHelperNames(c string) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range jsonHelperRe.FindAllString(c, -1) {
		if !seen[m] {
			seen[m] = true
			names = append(names, m)
		}
	}
	return names
}

// FuzzLexer drives lexer.Lex over arbitrary bytes. The lexer must never panic;
// malformed input yields ILLEGAL tokens + E-LEX diagnostics (design §5.1,
// front-end contract). Post-conditions: an EOF-terminated token slice and a
// non-nil *diag.List.
func FuzzLexer(f *testing.F) {
	addFuzzSeeds(f, seedFrontEnd)
	f.Fuzz(func(t *testing.T, src []byte) {
		toks, diags := lexer.Lex("fuzz.tl", src)
		if diags == nil {
			t.Fatalf("Lex returned a nil diag list")
		}
		if len(toks) == 0 || toks[len(toks)-1].Type != token.EOF {
			t.Fatalf("token slice is not EOF-terminated (len=%d)", len(toks))
		}
	})
}

// FuzzParser drives both parser entry points over arbitrary bytes. The lexer
// returns two values, so the token-slice entry needs the two-statement form
// (design §5.1). Post-condition: both programs are non-nil even on error (the
// recovery contract) and neither call panics.
func FuzzParser(f *testing.F) {
	addFuzzSeeds(f, seedFrontEnd)
	f.Fuzz(func(t *testing.T, src []byte) {
		toks, _ := lexer.Lex("fuzz.tl", src)
		prog, _ := parser.Parse("fuzz.tl", toks)
		prog2, _ := parser.ParseSource("fuzz.tl", src)
		if prog == nil {
			t.Fatalf("Parse returned a nil program")
		}
		if prog2 == nil {
			t.Fatalf("ParseSource returned a nil program")
		}
	})
}

// FuzzModuleGraph drives the whole multi-file front end over arbitrary bytes
// written as the root module: module.Build resolves and parses the graph, and
// a clean build is type-checked whole-program by checker.CheckProgram (FEAT-004
// / AC-32). The fuzz bytes stand in for a module whose imports (if any) resolve
// to nothing on disk, so the resolver's malformed-specifier paths — absolute,
// bare, root-escaping, empty list, ambiguous extension — are all reachable from
// the seed corpus. Post-conditions: neither Build nor CheckProgram panics or
// hangs, both diag lists are non-nil, and a build error leaves no modules to
// check (the graph short-circuits). A malformed input must yield diagnostics,
// never a crash.
func FuzzModuleGraph(f *testing.F) {
	addFuzzSeeds(f, seedFrontEnd)
	f.Fuzz(func(t *testing.T, src []byte) {
		dir := t.TempDir()
		root := filepath.Join(dir, "main.ts")
		if err := os.WriteFile(root, src, 0o644); err != nil {
			t.Fatalf("write root: %v", err)
		}
		graph, bdiags := module.Build(root)
		if bdiags == nil {
			t.Fatalf("module.Build returned a nil diag list")
		}
		if graph == nil {
			t.Fatalf("module.Build returned a nil graph")
		}
		if bdiags.HasErrors() {
			return // a bad specifier/parse: diagnostics, not a crash
		}
		_, cdiags := checker.CheckProgram(graph.Modules)
		if cdiags == nil {
			t.Fatalf("CheckProgram returned a nil diag list")
		}
	})
}

// FuzzCheck chains ParseSource into checker.Check on a clean front end. It uses
// NO recover() (design §5.2): any panic, hang or OOM is let to propagate as the
// HANDOVER gap §3 STOP-and-raise signal. The self-referential-generic shape is
// now seeded (FEAT-001 made the monomorphization closure terminate), so the
// default seed run must check it without crashing.
func FuzzCheck(f *testing.F) {
	addFuzzSeeds(f, seedFrontEnd)
	f.Fuzz(func(t *testing.T, src []byte) {
		prog, pdiags := parser.ParseSource("fuzz.tl", src)
		if pdiags.HasErrors() {
			return
		}
		checker.Check(prog)
	})
}

// FuzzJSONBind feeds fuzzed bytes as TLang source through parse -> check ->
// codegen.Emit, exercising the JSON codegen surface reachable from Go (design
// §5.1). It must not panic; on a clean front end that emits C, the output must
// be a self-consistent parser (presence of the generated tlj_parse_* /
// tlj_write_* helpers when the program binds/serializes JSON).
func FuzzJSONBind(f *testing.F) {
	addFuzzSeeds(f, seedJSONBind)
	f.Fuzz(func(t *testing.T, src []byte) {
		prog, pdiags := parser.ParseSource("fuzz.tl", src)
		if pdiags.HasErrors() {
			return
		}
		info, cdiags := checker.Check(prog)
		if cdiags.HasErrors() {
			return
		}
		out, err := codegen.Emit(prog, info)
		if err != nil {
			return
		}
		// Self-consistency presence check: every generated JSON helper name
		// that is referenced must also be defined (codegen's "every called
		// tlj_* is defined" guarantee, json_emit.go). A dangling tlj_parse_* /
		// tlj_write_* reference — a name appearing exactly once, i.e. called
		// but never defined — would be a malformed emitted parser.
		for _, name := range jsonHelperNames(string(out)) {
			if strings.Count(string(out), name+"(") < 2 {
				t.Fatalf("emitted JSON helper %q is referenced but not defined", name)
			}
		}
	})
}
