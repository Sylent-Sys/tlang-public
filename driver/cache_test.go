package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tlang/runtime"
)

func TestSourceRootContainsHash(t *testing.T) {
	c := NewRuntimeCache("/base")
	h := runtime.Hash()
	if c.Hash != h {
		t.Errorf("cache.Hash = %q, want %q", c.Hash, h)
	}
	sr := c.SourceRoot()
	if !strings.Contains(sr, h) {
		t.Errorf("SourceRoot %q does not contain hash %q", sr, h)
	}
	if filepath.Base(sr) != "runtime-"+h {
		t.Errorf("SourceRoot base = %q, want %q", filepath.Base(sr), "runtime-"+h)
	}
}

func TestArtifactDirPerCompiler(t *testing.T) {
	c := NewRuntimeCache("/base")
	gcc := c.ArtifactDir(CompilerGCC)
	clang := c.ArtifactDir(CompilerClang)
	tcc := c.ArtifactDir(CompilerTCC)
	if gcc == clang || gcc == tcc || clang == tcc {
		t.Errorf("artifact dirs collide: gcc=%q clang=%q tcc=%q", gcc, clang, tcc)
	}
	for k, dir := range map[CompilerKind]string{CompilerGCC: gcc, CompilerClang: clang, CompilerTCC: tcc} {
		if filepath.Base(dir) != k.String() {
			t.Errorf("ArtifactDir(%v) base = %q, want %q", k, filepath.Base(dir), k.String())
		}
		if filepath.Base(filepath.Dir(dir)) != "build" {
			t.Errorf("ArtifactDir(%v) parent = %q, want build", k, filepath.Base(filepath.Dir(dir)))
		}
	}
}

func TestExtractWritesHeaders(t *testing.T) {
	c := NewRuntimeCache(t.TempDir())
	if err := c.Extract(); err != nil {
		t.Fatalf("Extract() error: %v", err)
	}
	sr := c.SourceRoot()
	for _, rel := range []string{
		filepath.Join("include", "tlang.h"),
		filepath.Join("src", "tlang_internal.h"),
	} {
		if _, err := os.Stat(filepath.Join(sr, rel)); err != nil {
			t.Errorf("expected %s under SourceRoot: %v", rel, err)
		}
	}
}

func TestDefaultCacheRootNonEmpty(t *testing.T) {
	if DefaultCacheRoot() == "" {
		t.Error("DefaultCacheRoot() = empty")
	}
}
