// Package module builds the TLang module graph (DESIGN-modules.md §3): it
// parses the root file, resolves each import specifier to a file, parses
// every reachable module exactly once, produces a deterministic module
// order, and assigns each module a mangling Tag (§5).
//
// The package is a stdlib-only front-end builder. It imports only
// lexer/parser/ast/diag/token plus the Go standard library (path,
// path/filepath, os, sort, hash/fnv, strconv). It MUST NOT import
// checker/types/codegen so the compiler dependency graph stays a DAG; the
// checker consumes the Graph, not the other way round.
package module

import (
	"tlang/ast"
	"tlang/diag"
	"tlang/token"
)

// Module is one parsed source file in the graph.
type Module struct {
	// ID is the module identity: a normalized repo-relative slash path
	// (DESIGN-modules.md §3.2). Two specifiers that denote the same file
	// (./a and ../x/a) share one ID and therefore one *Module.
	ID string
	// Tag is the mangling tag derived purely from ID (§5); "" for the root
	// module of an import-free program (single-file fast path).
	Tag string
	// AbsPath is the absolute OS path the module was read from. It is used
	// only to open files; it never appears in a diagnostic (NFR-9).
	AbsPath string
	// Prog is the parsed program.
	Prog *ast.Program
	// Imports are the resolved edges of this module in source order (import
	// declarations first, then re-exports), one per import/re-export
	// specifier.
	Imports []*Edge
}

// Edge is a resolved import or re-export dependency.
type Edge struct {
	// Spec is the specifier string as written in the source ("./util").
	Spec string
	// Pos is the position of the specifier string literal.
	Pos token.Position
	// Target is the resolved module, or nil when the specifier failed to
	// resolve (a diagnostic was emitted on Graph.Diags).
	Target *Module
}

// Graph is the built module graph.
type Graph struct {
	// Root is the module built from the path passed to Build.
	Root *Module
	// Modules are all reachable modules in deterministic topological order
	// (DESIGN-modules.md §3.3): a module precedes the ones that import it,
	// with an ID-lexicographic tie-break. An allowed import cycle (SCC) is
	// emitted as a contiguous run ordered by ID.
	Modules []*Module
	// Diags collects the lexer/parser diagnostics of every module plus the
	// E-IMPORT resolution diagnostics.
	Diags *diag.List
}
