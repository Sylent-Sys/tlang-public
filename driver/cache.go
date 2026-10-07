package driver

import (
	"os"
	"path/filepath"

	"tlang/runtime"
)

// RuntimeCache describes where the embedded runtime is extracted and how
// per-compiler build artifacts are keyed.
type RuntimeCache struct {
	// Root is the base cache directory (e.g. <cacheDir>/tlang).
	Root string
	// Hash is runtime.Hash(): the content address of the embedded tree.
	Hash string
}

// NewRuntimeCache returns a RuntimeCache rooted at root, keyed by the current
// runtime.Hash().
func NewRuntimeCache(root string) RuntimeCache {
	return RuntimeCache{Root: root, Hash: runtime.Hash()}
}

// SourceRoot is the directory the runtime tree is extracted into:
//
//	<Root>/runtime-<Hash>
//
// It contains include/ and src/ after Extract. Named after the hash so it never
// serves stale sources.
func (c RuntimeCache) SourceRoot() string {
	return filepath.Join(c.Root, "runtime-"+c.Hash)
}

// ArtifactDir is the per-compiler directory for built runtime objects/archives:
//
//	<Root>/runtime-<Hash>/build/<compiler>   (<compiler> = "gcc"|"clang"|"tcc")
//
// Keyed by both runtime.Hash() (via SourceRoot) and compiler kind, so objects
// built by different compilers never collide: this is the ABI model.
func (c RuntimeCache) ArtifactDir(k CompilerKind) string {
	return filepath.Join(c.SourceRoot(), "build", k.String())
}

// Extract writes the embedded tree under SourceRoot using runtime.Extract.
// Idempotent (runtime.Extract leaves unchanged files alone). This is an
// execution-layer method: it touches the filesystem but creates no .c files of
// its own and compiles nothing. With no runtime .c files present it writes only
// the two headers; that is expected, not an error.
func (c RuntimeCache) Extract() error {
	return runtime.Extract(c.SourceRoot())
}

// DefaultCacheRoot is the cache root used when a caller does not override it:
// <os.UserCacheDir>/tlang, falling back to <os.TempDir>/tlang when
// os.UserCacheDir fails. It never panics.
func DefaultCacheRoot() string {
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "tlang")
	}
	return filepath.Join(os.TempDir(), "tlang")
}
