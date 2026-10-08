package module

import (
	"hash/fnv"
	"path"
	"path/filepath"
	"sort"
	"strconv"

	"tlang/ast"
	"tlang/diag"
	"tlang/parser"
	"tlang/token"
)

// zeroPos is the whole-file position used for diagnostics not tied to a
// source location (a missing or unreadable module).
func zeroPos() token.Position { return token.Position{} }

// Build constructs the module graph by transitive closure from the root file
// at rootPath (DESIGN-modules.md §3.3). It parses the root, resolves each
// import/re-export specifier, enqueues unseen targets, parses every reachable
// file exactly once, and merges each module's lex/parse diagnostics into
// Graph.Diags under its own file name. Import cycles are not an error here
// (FR-21). Modules are returned in a deterministic topological order with an
// ID-lexicographic tie-break, and each module is assigned its mangling Tag.
//
// An import-free root is a single Module with Tag == "" (the single-file fast
// path), so a program that uses no imports mangles byte-for-byte as before.
func Build(rootPath string) (*Graph, *diag.List) {
	return BuildWith(rootPath, BuildOptions{})
}

// BuildWith is Build with file access and the project root configurable (the
// language server analyzes unsaved editor buffers through opts.FS). With
// opts.RootDir set, module IDs are slash paths relative to it, resolution is
// bounded by it, and a root file outside it is an E-IMPORT with no root.
func BuildWith(rootPath string, opts BuildOptions) (*Graph, *diag.List) {
	diags := diag.NewList("")
	g := &Graph{Diags: diags}
	fsys := opts.FS
	if fsys == nil {
		fsys = osFS{}
	}

	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		diags.Errorf(zeroPos(), codeImport, "cannot resolve root path: %v", err)
		return g, diags
	}
	rootDir := filepath.Dir(absRoot)
	rootID := filepath.ToSlash(filepath.Base(absRoot))
	if opts.RootDir != "" {
		absDir, err := filepath.Abs(opts.RootDir)
		if err != nil {
			diags.Errorf(zeroPos(), codeImport, "cannot resolve project root: %v", err)
			return g, diags
		}
		rel, err := filepath.Rel(absDir, absRoot)
		if err != nil || rel == ".." || hasParentPrefix(filepath.ToSlash(rel)) {
			// Never echo either path (NFR-9).
			diags.Errorf(zeroPos(), codeImport, "root module is outside the project root")
			return g, diags
		}
		rootDir = absDir
		rootID = filepath.ToSlash(rel)
	}

	byID := map[string]*Module{}
	// queue holds modules whose Prog is parsed but whose imports are not yet
	// walked.
	type pending struct {
		mod   *Module
		dirID string
	}
	var queue []pending

	parse := func(id, absPath, dirID string) *Module {
		if m, ok := byID[id]; ok {
			return m
		}
		src, err := fsys.ReadFile(absPath)
		if err != nil {
			// A missing root is reported here; imported targets are checked
			// for existence during resolution, so this is effectively the
			// root-only path. Never echo absPath (NFR-9); name the ID.
			diags.Errorf(zeroPos(), codeImport, "cannot read module %q", id)
			return nil
		}
		prog, fileDiags := parser.ParseSource(id, src)
		mergeDiags(diags, fileDiags)
		m := &Module{ID: id, AbsPath: absPath, Prog: prog}
		byID[id] = m
		queue = append(queue, pending{mod: m, dirID: dirID})
		return m
	}

	root := parse(rootID, absRoot, path.Dir(rootID))
	g.Root = root
	if root == nil {
		return g, diags
	}

	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, spec := range importSpecs(p.mod.Prog) {
			edge := &Edge{Spec: spec.text, Pos: spec.pos}
			res, rerr := resolve(fsys, rootDir, p.dirID, spec.text)
			if rerr != nil {
				diags.Add(diag.Diagnostic{File: p.mod.ID, Pos: spec.pos, Severity: diag.Error, Code: codeImport, Message: rerr.Error()})
			} else if res.standard != nil {
				edge.Target = StandardImportTarget(res.standard)
			} else {
				edge.Target = SourceImportTarget(parse(res.id, res.absPath, path.Dir(res.id)))
			}
			p.mod.Imports = append(p.mod.Imports, edge)
		}
	}

	g.Modules = topoOrder(byID, root)
	for _, m := range g.Modules {
		m.Tag = assignTag(byID, m)
	}
	return g, diags
}

// assignTag returns the module's mangling Tag. A single import-free root
// keeps Tag "" (single-file fast path); every module in a multi-module graph
// gets a non-empty tag so its globals/types/functions mangle distinctly.
func assignTag(byID map[string]*Module, m *Module) string {
	if len(byID) == 1 {
		return ""
	}
	return Tag(m.ID)
}

// importSpec is one specifier read from a module's AST, with its position.
type importSpec struct {
	text string
	pos  token.Position
}

// importSpecs returns the specifiers of a module in source order: import
// declarations first (in statement order), then re-exports, matching the
// AST statement order since both forms are top-level statements.
func importSpecs(prog *ast.Program) []importSpec {
	var out []importSpec
	for _, s := range prog.Statements {
		switch d := s.(type) {
		case *ast.ImportDecl:
			out = append(out, importSpec{text: d.From, pos: d.FromPos})
		case *ast.ReExportDecl:
			out = append(out, importSpec{text: d.From, pos: d.FromPos})
		}
	}
	return out
}

// topoOrder returns the modules in deterministic topological order: a module
// precedes the modules that import it, with an ID-lexicographic tie-break
// (Kahn's algorithm over the dependency edges, picking the smallest ready ID
// at each step). An allowed import cycle leaves some nodes unqueued; they are
// appended by ID so the order is total and deterministic (FR-21).
func topoOrder(byID map[string]*Module, root *Module) []*Module {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// deps[a] = set of modules a imports (a depends on them); indeg counts
	// how many importers each module has resolved... we want dependencies
	// first, so order by "all of my imports are already emitted".
	remaining := map[string]int{}      // unmet dependency count per module
	importers := map[string][]string{} // target -> modules that import it
	for _, id := range ids {
		m := byID[id]
		seen := map[string]bool{}
		for _, e := range m.Imports {
			target := e.Target.Source
			if e.Target.Kind != ImportTargetSource || target == nil || target.ID == id || seen[target.ID] {
				continue
			}
			seen[target.ID] = true
			remaining[id]++
			importers[target.ID] = append(importers[target.ID], id)
		}
	}

	emitted := map[string]bool{}
	var order []*Module
	for len(order) < len(ids) {
		// Pick the lexicographically smallest module whose dependencies are
		// all emitted and that is not yet emitted.
		next := ""
		for _, id := range ids {
			if emitted[id] || remaining[id] > 0 {
				continue
			}
			next = id
			break
		}
		if next == "" {
			// A cycle remains: break it by emitting the smallest unemitted
			// ID (its SCC members follow by ID as their deps clear).
			for _, id := range ids {
				if !emitted[id] {
					next = id
					break
				}
			}
		}
		emitted[next] = true
		order = append(order, byID[next])
		for _, imp := range importers[next] {
			if remaining[imp] > 0 {
				remaining[imp]--
			}
		}
	}
	return order
}

// Tag returns the mangling tag of a module identity (DESIGN-modules.md §5):
//
//	Tag(ID) = sanitize(ID) + "_" + base36(fnv32a(ID))
//
// It is a pure function of ID (path-order independent, NFR-7). The base36
// disambiguator uses [0-9a-z] with no padding or uppercase, so it contains
// no '_' and never introduces "__" — the property the mangling injectivity
// proof depends on.
func Tag(id string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return sanitize(id) + "_" + strconv.FormatUint(uint64(h.Sum32()), 36)
}

// sanitize maps each run of non-[A-Za-z0-9] in id to a single '_', collapses
// any resulting "__" to "_", and prefixes 'm' to a leading digit so the tag
// is a valid C identifier fragment that never contains "__".
func sanitize(id string) string {
	b := make([]byte, 0, len(id)+1)
	prevUnderscore := false
	for i := 0; i < len(id); i++ {
		c := id[i]
		if isAlnum(c) {
			b = append(b, c)
			prevUnderscore = false
		} else if !prevUnderscore {
			b = append(b, '_')
			prevUnderscore = true
		}
	}
	// Trim a trailing underscore so the "_" + base36 join is the only
	// separator before the disambiguator (keeps the tag free of "__").
	for len(b) > 0 && b[len(b)-1] == '_' {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return "m"
	}
	if b[0] >= '0' && b[0] <= '9' {
		b = append([]byte{'m'}, b...)
	}
	return string(b)
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// mergeDiags appends src's items to dst, preserving their File stamps so a
// diagnostic keeps naming its own module.
func mergeDiags(dst, src *diag.List) {
	for _, d := range src.Items {
		dst.Add(d)
	}
}
