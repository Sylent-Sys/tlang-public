package module

import (
	"path"
	"path/filepath"
)

// codeImport is the open-string diagnostic code for the module resolution
// family (DESIGN-modules.md §4.5, NFR-6: diag/ stays additive, so the code is
// a literal here rather than a new diag constant).
const codeImport = "E-IMPORT"

// resolution is the successful result of resolving one specifier.
type resolution struct {
	standard *StandardModule
	// id is the module identity: a normalized repo-relative slash path with
	// extension (DESIGN-modules.md §3.2). The root module has id "" turned
	// into the empty-dir identity by the caller.
	id string
	// absPath is the OS path to read the file from. It is never echoed in a
	// diagnostic (NFR-9).
	absPath string
}

// resolveErr describes why a specifier failed to resolve. message is safe to
// show: it never contains a path outside the project root (NFR-9).
type resolveErr struct {
	message string
}

func (e *resolveErr) Error() string { return e.message }

// resolve maps a specifier written in the module whose directory identity is
// dirID (a repo-relative slash path, "." for the root module's directory) to
// a resolution, per DESIGN-modules.md §3.2 with the review finding-1 pins:
//
//   - form check: the specifier must begin "./" or "../"; a bare specifier or
//     an absolute path is rejected with distinct wording;
//   - rel = path.Clean(path.Join(dirID, spec)) on slash paths;
//   - root escape is purely lexical: rel == ".." or rel begins "../";
//   - extension resolution: an explicit extension is used as written; else
//     probe <rel>.ts then <rel>.tlang (both exist -> ambiguous; neither ->
//     missing);
//   - identity is the normalized repo-relative slash path (so ./a and ../x/a
//     reaching one file share one Module).
//
// rootDir is the absolute OS directory of the root module (or the configured
// BuildOptions.RootDir); a resolved id is joined onto it to form the OS path,
// and candidates are probed through fsys. No diagnostic ever echoes a path
// outside the root.
func resolve(fsys FileSystem, rootDir, dirID, spec string) (resolution, *resolveErr) {
	if spec == "tlang" || hasPrefix(spec, "tlang/") {
		if spec == "tlang" || !validStandardSpecifier(spec) {
			return resolution{}, &resolveErr{message: "invalid standard module specifier " + quote(spec)}
		}
		m, ok := LookupStandardModule(spec)
		if !ok {
			return resolution{}, &resolveErr{message: "unknown standard module " + quote(spec)}
		}
		if !m.Available {
			return resolution{}, &resolveErr{message: "standard module " + quote(spec) + " is not available"}
		}
		return resolution{standard: m}, nil
	}
	if !hasRelPrefix(spec) {
		if path.IsAbs(spec) || filepath.IsAbs(spec) {
			return resolution{}, &resolveErr{message: "absolute import specifier " + quote(spec) + " is not allowed; use a relative path (\"./\" or \"../\")"}
		}
		return resolution{}, &resolveErr{message: "bare import specifier " + quote(spec) + " is not allowed; use a relative path (\"./\" or \"../\")"}
	}

	rel := path.Clean(path.Join(dirID, spec))
	if rel == ".." || hasParentPrefix(rel) {
		return resolution{}, &resolveErr{message: "import specifier " + quote(spec) + " escapes the project root"}
	}

	// Candidate identities in resolution order. An explicit source extension
	// is honored as written; otherwise probe .ts then .tlang.
	var candidates []string
	if ext := path.Ext(rel); ext == ".ts" || ext == ".tlang" {
		candidates = []string{rel}
	} else {
		candidates = []string{rel + ".ts", rel + ".tlang"}
	}

	found := ""
	foundCount := 0
	for _, id := range candidates {
		if fsys.IsFile(filepath.Join(rootDir, filepath.FromSlash(id))) {
			if found == "" {
				found = id
			}
			foundCount++
		}
	}

	switch foundCount {
	case 0:
		return resolution{}, &resolveErr{message: "cannot resolve import " + quote(spec) + "; no file at " + candidateList(candidates)}
	case 1:
		return resolution{id: found, absPath: filepath.Join(rootDir, filepath.FromSlash(found))}, nil
	default:
		return resolution{}, &resolveErr{message: "ambiguous import " + quote(spec) + "; both " + candidateList(candidates) + " exist"}
	}
}

func validStandardSpecifier(spec string) bool {
	if !hasPrefix(spec, "tlang/") || len(spec) == len("tlang/") {
		return false
	}
	segmentStart := len("tlang/")
	for i := segmentStart; i <= len(spec); i++ {
		if i != len(spec) && spec[i] != '/' {
			continue
		}
		segment := spec[segmentStart:i]
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		segmentStart = i + 1
	}
	return true
}

// hasRelPrefix reports whether spec begins "./" or "../" (slash form; the
// specifier grammar is slash-only).
func hasRelPrefix(spec string) bool {
	return spec == "." || spec == ".." ||
		hasPrefix(spec, "./") || hasPrefix(spec, "../")
}

// hasParentPrefix reports whether a cleaned slash path begins "../" and thus
// escapes its base directory.
func hasParentPrefix(rel string) bool { return hasPrefix(rel, "../") }

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// quote wraps s in double quotes for a diagnostic. s is always a specifier
// written in the source, never a resolved path, so it is safe to echo.
func quote(s string) string { return "\"" + s + "\"" }

// candidateList renders the probed identities (repo-relative, inside the
// root) for a diagnostic.
func candidateList(ids []string) string {
	s := ""
	for i, id := range ids {
		if i > 0 {
			s += " or "
		}
		s += quote(id)
	}
	return s
}
