package main

import (
	"fmt"
	"io"
	"os"

	"tlang/ast"
	"tlang/checker"
	"tlang/module"
	"tlang/parser"
	"tlang/types"
)

// frontendResult holds everything the OS-independent subcommands need after
// running the front end on a single source file.
type frontendResult struct {
	prog    *ast.Program
	info    *types.Info
	src     []byte
	hasErrs bool
}

// loadSource reads a single source-file path. On a read error it writes a
// usage-style message to stderr and returns ok=false; callers map that to the
// usage exit code.
func loadSource(path string, stderr io.Writer) (src []byte, ok bool) {
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "tlang: cannot read %s: %v\n", path, err)
		return nil, false
	}
	return src, true
}

// runFrontend parses and type-checks src, rendering every diagnostic from
// BOTH the parser/lexer list and the checker list to stderr. ParseSource and
// Check return separate *diag.List values, so both are rendered. The returned
// hasErrs is true iff either list HasErrors(); warnings are rendered but do
// not by themselves fail the build.
func runFrontend(path string, src []byte, stderr io.Writer) frontendResult {
	prog, parseDiags := parser.ParseSource(path, src)
	info, checkDiags := checker.Check(prog)

	if parseDiags.Len() > 0 {
		fmt.Fprint(stderr, parseDiags.Render(src))
	}
	if checkDiags.Len() > 0 {
		fmt.Fprint(stderr, checkDiags.Render(src))
	}

	return frontendResult{
		prog:    prog,
		info:    info,
		src:     src,
		hasErrs: parseDiags.HasErrors() || checkDiags.HasErrors(),
	}
}

// runFrontendGraph is the multi-file front end (DESIGN-modules.md §7): it
// builds the module graph from rootPath, type-checks every reachable module
// together with checker.CheckProgram, and assembles a synthetic merged
// *ast.Program that concatenates every module's top-level DECLARATION
// statements (not ImportDecl/ReExportDecl — pass 0 consumed those) in
// module-topological-then-source order, each statement keeping its own
// file-bearing position. codegen then walks that one merged program with the
// merged *types.Info, emitting one C translation unit.
//
// A single import-free root collapses to one module whose Statements are its
// own declarations with every tag "", so the emitted C is byte-identical to
// the single-file path.
func runFrontendGraph(rootPath string, stderr io.Writer) frontendResult {
	graph, buildDiags := module.Build(rootPath)
	if buildDiags.Len() > 0 {
		fmt.Fprint(stderr, buildDiags.Render(nil))
	}
	if buildDiags.HasErrors() {
		return frontendResult{hasErrs: true}
	}

	info, checkDiags := checker.CheckProgram(graph.Modules)
	if checkDiags.Len() > 0 {
		fmt.Fprint(stderr, checkDiags.Render(nil))
	}

	merged := mergePrograms(graph)
	return frontendResult{
		prog:    merged,
		info:    info,
		hasErrs: checkDiags.HasErrors(),
	}
}

// mergePrograms builds the synthetic merged program codegen consumes: the
// declaration statements of every module in graph order, excluding the import
// and re-export statements the checker already resolved. Each statement keeps
// its own position (which still names its own source file), and merged.File
// is the root module's file name.
func mergePrograms(graph *module.Graph) *ast.Program {
	merged := &ast.Program{}
	if graph.Root != nil && graph.Root.Prog != nil {
		merged.File = graph.Root.Prog.File
		merged.EOF = graph.Root.Prog.EOF
	}
	for _, m := range graph.Modules {
		for _, s := range m.Prog.Statements {
			switch s.(type) {
			case *ast.ImportDecl, *ast.ReExportDecl:
				// Consumed by pass 0; not part of the emitted program.
			default:
				merged.Statements = append(merged.Statements, s)
			}
		}
	}
	return merged
}
