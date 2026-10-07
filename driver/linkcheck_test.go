package driver

import (
	"errors"
	"testing"
)

// linkCheckCompiler returns a CompilerKind that is actually present on PATH
// (gcc preferred, then clang), or ok=false when neither is available so the
// caller can skip cleanly on a bare host.
func linkCheckCompiler() (CompilerKind, bool) {
	tc := Discover(nil)
	if _, ok := tc.Lookup(CompilerGCC); ok {
		return CompilerGCC, true
	}
	if _, ok := tc.Lookup(CompilerClang); ok {
		return CompilerClang, true
	}
	return 0, false
}

// TestMandatoryItems_floatMod_link mirrors the codegen matrix's one SKIP
// (TestMandatoryItems/float_mod/link). It compiles and links a real
// tlang_mod_f64 user against the real runtime via LinkCheckFloatMod. It skips
// cleanly only when no C compiler is present, so the suite stays green on a
// bare host without ever failing there.
func TestMandatoryItems_floatMod_link(t *testing.T) {
	kind, ok := linkCheckCompiler()
	if !ok {
		t.Skip("float-% link check: no C compiler (gcc/clang) on PATH")
	}
	d := New(Options{CacheRoot: t.TempDir()})
	if err := LinkCheckFloatMod(d, kind); err != nil {
		t.Fatalf("LinkCheckFloatMod(%v) = %v, want nil", kind, err)
	}
}

// TestLinkCheckFloatModNoCompiler covers the no-compiler branch hermetically:
// with a toolchain that reports nothing present, LinkCheckFloatMod returns
// ErrNoCompiler without touching a real compiler or the filesystem.
func TestLinkCheckFloatModNoCompiler(t *testing.T) {
	d := New(Options{
		LookPath:  func(string) (string, error) { return "", errors.New("absent") },
		PgConfig:  func(...string) (string, error) { return "", errors.New("absent") },
		CacheRoot: t.TempDir(),
	})
	if err := LinkCheckFloatMod(d, CompilerGCC); !errors.Is(err, ErrNoCompiler) {
		t.Fatalf("LinkCheckFloatMod = %v, want ErrNoCompiler", err)
	}
}
