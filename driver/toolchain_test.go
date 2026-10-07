package driver

import (
	"errors"
	"testing"
)

// fakeLookPath builds a LookPathFunc over a present-map; names mapped to a
// non-empty path resolve, all others return an error (absent).
func fakeLookPath(present map[string]string) LookPathFunc {
	return func(name string) (string, error) {
		if p, ok := present[name]; ok && p != "" {
			return p, nil
		}
		return "", errors.New("not found: " + name)
	}
}

func TestCompilerKindString(t *testing.T) {
	cases := map[CompilerKind]string{
		CompilerGCC:   "gcc",
		CompilerClang: "clang",
		CompilerTCC:   "tcc",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("CompilerKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

func TestDiscoverOrderAndPresence(t *testing.T) {
	tc := Discover(fakeLookPath(map[string]string{
		"gcc":   "/usr/bin/gcc",
		"clang": "/usr/bin/clang",
		"tcc":   "/usr/bin/tcc",
	}))
	if len(tc.Compilers) != 3 {
		t.Fatalf("got %d compilers, want 3", len(tc.Compilers))
	}
	wantOrder := []CompilerKind{CompilerGCC, CompilerClang, CompilerTCC}
	for i, k := range wantOrder {
		if tc.Compilers[i].Kind != k {
			t.Errorf("compiler[%d].Kind = %v, want %v", i, tc.Compilers[i].Kind, k)
		}
	}
	if c, ok := tc.Lookup(CompilerGCC); !ok || c.Path != "/usr/bin/gcc" {
		t.Errorf("Lookup(gcc) = %+v,%v", c, ok)
	}
	for _, k := range wantOrder {
		if !tc.Has(k) {
			t.Errorf("Has(%v) = false, want true", k)
		}
	}
}

func TestDiscoverEmpty(t *testing.T) {
	tc := Discover(fakeLookPath(map[string]string{}))
	if len(tc.Compilers) != 0 {
		t.Fatalf("got %d compilers, want 0", len(tc.Compilers))
	}
	if _, ok := tc.Release(); ok {
		t.Error("Release() ok = true on empty toolchain, want false")
	}
	if _, ok := tc.Dev(); ok {
		t.Error("Dev() ok = true on empty toolchain, want false")
	}
	if tc.Has(CompilerGCC) {
		t.Error("Has(gcc) = true on empty toolchain")
	}
}

func TestDiscoverPartial(t *testing.T) {
	// Only clang and tcc present: gcc absent.
	tc := Discover(fakeLookPath(map[string]string{
		"clang": "/opt/clang",
		"tcc":   "/opt/tcc",
	}))
	if tc.Has(CompilerGCC) {
		t.Error("Has(gcc) = true, want false")
	}
	if !tc.Has(CompilerClang) || !tc.Has(CompilerTCC) {
		t.Error("expected clang and tcc present")
	}
	// Order preserved (clang before tcc).
	if tc.Compilers[0].Kind != CompilerClang || tc.Compilers[1].Kind != CompilerTCC {
		t.Errorf("order = %v, want [clang tcc]", tc.Compilers)
	}
}

func TestReleasePrefersGCCOverClang(t *testing.T) {
	tc := Discover(fakeLookPath(map[string]string{
		"gcc":   "/g",
		"clang": "/c",
	}))
	c, ok := tc.Release()
	if !ok || c.Kind != CompilerGCC {
		t.Errorf("Release() = %+v,%v, want gcc", c, ok)
	}

	// gcc absent -> falls to clang.
	tc = Discover(fakeLookPath(map[string]string{"clang": "/c"}))
	c, ok = tc.Release()
	if !ok || c.Kind != CompilerClang {
		t.Errorf("Release() with no gcc = %+v,%v, want clang", c, ok)
	}

	// Both absent -> ok=false.
	tc = Discover(fakeLookPath(map[string]string{"tcc": "/t"}))
	if _, ok := tc.Release(); ok {
		t.Error("Release() ok = true with only tcc, want false")
	}
}

func TestDevPrefersTCCThenGCCThenClang(t *testing.T) {
	tc := Discover(fakeLookPath(map[string]string{
		"gcc":   "/g",
		"clang": "/c",
		"tcc":   "/t",
	}))
	if c, ok := tc.Dev(); !ok || c.Kind != CompilerTCC {
		t.Errorf("Dev() = %+v,%v, want tcc", c, ok)
	}

	// No tcc -> gcc.
	tc = Discover(fakeLookPath(map[string]string{"gcc": "/g", "clang": "/c"}))
	if c, ok := tc.Dev(); !ok || c.Kind != CompilerGCC {
		t.Errorf("Dev() no tcc = %+v,%v, want gcc", c, ok)
	}

	// Only clang -> clang.
	tc = Discover(fakeLookPath(map[string]string{"clang": "/c"}))
	if c, ok := tc.Dev(); !ok || c.Kind != CompilerClang {
		t.Errorf("Dev() only clang = %+v,%v, want clang", c, ok)
	}
}
