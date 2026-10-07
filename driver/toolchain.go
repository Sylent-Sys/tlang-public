package driver

import "os/exec"

// CompilerKind identifies a supported C compiler family.
type CompilerKind int

const (
	// CompilerGCC is the GNU C compiler (gcc).
	CompilerGCC CompilerKind = iota + 1
	// CompilerClang is the LLVM C compiler (clang).
	CompilerClang
	// CompilerTCC is TinyCC (tcc).
	CompilerTCC
)

// String returns the executable base name of the compiler family.
func (k CompilerKind) String() string {
	switch k {
	case CompilerGCC:
		return "gcc"
	case CompilerClang:
		return "clang"
	case CompilerTCC:
		return "tcc"
	default:
		return "unknown"
	}
}

// Compiler is one discovered C compiler.
type Compiler struct {
	// Kind is the compiler family.
	Kind CompilerKind
	// Path is the executable path returned by LookPath.
	Path string
}

// Toolchain is the result of discovery: the compilers found on PATH, in a
// stable preference order (gcc, clang, tcc).
type Toolchain struct {
	// Compilers are the discovered compilers, in preference order.
	Compilers []Compiler
}

// Has reports whether a compiler of the given kind was found.
func (t Toolchain) Has(k CompilerKind) bool {
	_, ok := t.Lookup(k)
	return ok
}

// Lookup returns the discovered compiler of kind k, or ok=false.
func (t Toolchain) Lookup(k CompilerKind) (Compiler, bool) {
	for _, c := range t.Compilers {
		if c.Kind == k {
			return c, true
		}
	}
	return Compiler{}, false
}

// Release returns the preferred compiler for a release build (gcc, then
// clang), or ok=false if neither is present.
func (t Toolchain) Release() (Compiler, bool) {
	if c, ok := t.Lookup(CompilerGCC); ok {
		return c, true
	}
	if c, ok := t.Lookup(CompilerClang); ok {
		return c, true
	}
	return Compiler{}, false
}

// Dev returns the preferred compiler for a fast dev build (tcc, then gcc, then
// clang), or ok=false if none is present.
func (t Toolchain) Dev() (Compiler, bool) {
	if c, ok := t.Lookup(CompilerTCC); ok {
		return c, true
	}
	return t.Release()
}

// LookPathFunc resolves an executable name to a path, like exec.LookPath.
type LookPathFunc func(name string) (string, error)

// Discover finds gcc, clang and tcc using lookPath. A nil lookPath uses
// exec.LookPath (the real host PATH). The returned Toolchain lists the
// compilers found in preference order (gcc, clang, tcc). Discover never returns
// an error for "nothing found": an empty Toolchain is a valid result that
// callers test with Has/Release/Dev. Any lookup error is treated as "absent".
func Discover(lookPath LookPathFunc) Toolchain {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	var tc Toolchain
	for _, k := range []CompilerKind{CompilerGCC, CompilerClang, CompilerTCC} {
		if p, err := lookPath(k.String()); err == nil && p != "" {
			tc.Compilers = append(tc.Compilers, Compiler{Kind: k, Path: p})
		}
	}
	return tc
}
