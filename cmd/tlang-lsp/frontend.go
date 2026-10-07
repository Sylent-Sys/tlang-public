package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"tlang/ast"
	"tlang/checker"
	"tlang/diag"
	"tlang/module"
	"tlang/parser"
	"tlang/types"
)

// analysis is the result of running the front end on one document's text.
type analysis struct {
	prog  *ast.Program
	info  *types.Info
	src   []byte
	diags []diag.Diagnostic
	// mc is set when the document was analyzed in its module graph (a file:
	// document); nil on the single-file path.
	mc *moduleContext
}

// analyze parses and type-checks text as a single file, mirroring cmd/tlang's
// runFrontend: checker.Check always runs, even after parse errors (the tree is
// walkable), and the parser/lexer diagnostics are concatenated before the
// checker's, in that order. It never reads from disk. The server uses it for
// documents without a file: URI (untitled buffers), where imports cannot be
// resolved.
func analyze(uri, text string) analysis {
	src := []byte(text)
	prog, parseDiags := parser.ParseSource(fileName(uri), src)
	info, checkDiags := checker.Check(prog)
	diags := make([]diag.Diagnostic, 0, len(parseDiags.Items)+len(checkDiags.Items))
	diags = append(diags, parseDiags.Items...)
	diags = append(diags, checkDiags.Items...)
	return analysis{prog: prog, info: info, src: src, diags: diags}
}

// analyzeModule analyzes the document at docPath (whose current text is text)
// in its module graph, the way cmd/tlang's runFrontendGraph does:
// module.BuildWith from the document as root, bounded by rootDir and reading
// through fsys (open buffers first, then disk), then checker.CheckProgram.
// CheckProgram runs even when the graph has build errors (it skips unresolved
// edges), so completion, hover and definition keep working while a line is
// half typed. The published diagnostics are chosen by documentDiagnostics.
func analyzeModule(docPath, rootDir string, fsys module.FileSystem, text string) analysis {
	graph, buildDiags := module.BuildWith(docPath, module.BuildOptions{FS: fsys, RootDir: rootDir})
	root := graph.Root
	if root == nil {
		// The document could not be loaded as a module (it lies outside
		// rootDir); analyze it alone and surface why.
		a := analyze(filepath.Base(docPath), text)
		a.diags = append(append([]diag.Diagnostic{}, buildDiags.Items...), a.diags...)
		return a
	}
	info, checkDiags := checker.CheckProgram(graph.Modules)
	return analysis{
		prog:  root.Prog,
		info:  info,
		src:   []byte(text),
		diags: documentDiagnostics(graph, buildDiags.Items, checkDiags.Items),
		mc:    &moduleContext{graph: graph, mod: root, info: info, fsys: fsys, rootDir: rootDir},
	}
}

// documentDiagnostics selects what the graph's root document publishes. Each
// open document publishes its own file's diagnostics:
//   - its build diagnostics (lex, parse, E-IMPORT resolution), plus any that
//     name no file;
//   - its checker diagnostics, unless its own file has build errors. As in
//     cmd/tlang, a document whose own text does not build gets no checker
//     noise; a broken dependency does not hide them, since CheckProgram skips
//     what did not resolve;
//   - an E-IMPORT at each import or re-export specifier whose target, itself
//     or through its imports, has build errors: those errors belong to another
//     file, which may not be open, and `tlang check` on this document fails
//     because of them.
func documentDiagnostics(graph *module.Graph, buildDiags, checkDiags []diag.Diagnostic) []diag.Diagnostic {
	root := graph.Root
	own := func(d diag.Diagnostic) bool { return d.File == root.ID || d.File == "" }
	broken := map[string]bool{} // module IDs with build errors
	ownErrors := false
	var out []diag.Diagnostic
	for _, d := range buildDiags {
		if d.Severity == diag.Error {
			broken[d.File] = true
		}
		if own(d) {
			out = append(out, d)
			ownErrors = ownErrors || d.Severity == diag.Error
		}
	}
	for _, e := range root.Imports {
		if e.Target == nil {
			continue // unresolved: already an E-IMPORT of this document
		}
		bad := firstBroken(e.Target, root, broken)
		if bad == nil {
			continue
		}
		msg := fmt.Sprintf("imported module %q has errors", e.Target.ID)
		if bad != e.Target {
			msg = fmt.Sprintf("imported module %q depends on %q, which has errors", e.Target.ID, bad.ID)
		}
		out = append(out, diag.Diagnostic{File: root.ID, Pos: e.Pos, Severity: diag.Error, Code: "E-IMPORT", Message: msg})
	}
	if !ownErrors {
		for _, d := range checkDiags {
			if own(d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// firstBroken returns the first module with build errors reachable from m
// through resolved imports (m itself first, then depth-first in source
// order), or nil. root is never counted: its own errors are shown already.
func firstBroken(m, root *module.Module, broken map[string]bool) *module.Module {
	visited := map[*module.Module]bool{root: true}
	var walk func(*module.Module) *module.Module
	walk = func(m *module.Module) *module.Module {
		if visited[m] {
			return nil
		}
		visited[m] = true
		if broken[m.ID] {
			return m
		}
		for _, e := range m.Imports {
			if e.Target != nil {
				if b := walk(e.Target); b != nil {
					return b
				}
			}
		}
		return nil
	}
	return walk(m)
}

// fileName derives a display name from a document URI: the last path segment,
// percent-decoded. It performs no OS path operations, so it is cross-platform
// and handles Windows URIs such as file:///c%3A/dir/app.tlang. The name is
// cosmetic (it only appears in diag.File); the LSP mapping does not use it.
func fileName(uri string) string {
	s := uri
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	if decoded, err := url.PathUnescape(s); err == nil {
		s = decoded
	}
	if s == "" {
		return uri
	}
	return s
}
