package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"tlang/ast"
	"tlang/checker"
	"tlang/diag"
	"tlang/module"
	"tlang/parser"
	"tlang/project"
	"tlang/types"
)

// frontendResult holds everything the OS-independent subcommands need after
// running the front end on a single source file.
type frontendResult struct {
	prog    *ast.Program
	info    *types.Info
	project *project.Project
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

// resolveProjectInput keeps ordinary file invocations on their legacy path,
// but when an ancestor tlang.json exists it makes the manifest entry the
// project's single CLI entry: a directory selects that entry, while a file
// must name it explicitly. Discovery errors are safe to display (project
// errors intentionally omit manifest contents and paths).
func resolveProjectInput(input string, stderr io.Writer) (sourcePath, rootDir string, ok bool) {
	sourcePath, rootDir, _, ok = resolveProjectInputWithManifest(input, stderr)
	return sourcePath, rootDir, ok
}

func resolveProjectInputWithManifest(input string, stderr io.Writer) (sourcePath, rootDir string, discovered *project.Project, ok bool) {
	projectInfo, err := project.Discover(input)
	if errors.Is(err, project.ErrManifestNotFound) {
		return input, "", nil, true
	}
	if err != nil {
		fmt.Fprintf(stderr, "tlang: %v\n", err)
		return "", "", nil, false
	}

	info, statErr := os.Stat(input)
	if statErr == nil && info.IsDir() {
		return projectInfo.EntryPath(), projectInfo.Root, projectInfo, true
	}
	inputAbs, inputErr := filepath.Abs(input)
	entryAbs, entryErr := filepath.Abs(projectInfo.EntryPath())
	if inputErr != nil || entryErr != nil || !samePath(inputAbs, entryAbs) {
		fmt.Fprintln(stderr, "tlang: project: input does not match manifest entry")
		return "", "", nil, false
	}
	return input, projectInfo.Root, projectInfo, true
}

func readProjectManifest(discovered *project.Project) []byte {
	if discovered == nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(discovered.Root, project.ManifestName))
	if err != nil || project.ManifestDigest(data) != discovered.ManifestSHA256 {
		return nil
	}
	return data
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
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
func runFrontendGraph(rootPath, projectRoot string, discovered *project.Project, stderr io.Writer) frontendResult {
	var graph *module.Graph
	var buildDiags *diag.List
	if projectRoot == "" {
		// A zero root preserves the exact pre-manifest module graph behavior.
		graph, buildDiags = module.Build(rootPath)
	} else {
		graph, buildDiags = module.BuildWith(rootPath, module.BuildOptions{RootDir: projectRoot})
	}
	if buildDiags.Len() > 0 {
		fmt.Fprint(stderr, buildDiags.Render(nil))
	}
	if buildDiags.HasErrors() {
		return frontendResult{hasErrs: true}
	}

	info, checkDiags := checker.CheckProgram(graph.Modules)
	if discovered != nil {
		manifestBytes := readProjectManifest(discovered)
		if manifestBytes == nil {
			fmt.Fprintln(stderr, "tlang: project manifest changed during compilation")
			return frontendResult{hasErrs: true}
		}
		info.ManifestJSON = string(manifestBytes)
		info.ManifestSHA256 = discovered.ManifestSHA256
	}
	if checkDiags.Len() > 0 {
		fmt.Fprint(stderr, checkDiags.Render(nil))
	}

	merged := mergePrograms(graph)
	return frontendResult{
		prog:    merged,
		info:    info,
		project: discovered,
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
