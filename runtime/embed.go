// Package runtime embeds the TLang C runtime sources so that the tlang CLI is a
// single binary (DESIGN.md §1, §4.1). The driver extracts them into a cache
// directory and compiles them together with the generated program.
//
// The embedded tree mirrors the repository's runtime directory:
//
//	include/tlang.h        public API, the only header generated code includes
//	src/tlang_internal.h   private API shared by the runtime modules
//	src/*.c                runtime modules (see docs/RUNTIME.md)
//
// The Makefile, the C unit tests and build outputs are not embedded.
package runtime

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

//go:embed include src
var files embed.FS

// IncludeDir and SourceDir are the directories of the embedded tree, relative
// to the extraction root: compile with -I<root>/include -I<root>/src.
const (
	IncludeDir = "include"
	SourceDir  = "src"
)

var (
	listOnce sync.Once
	allFiles []string
	cSources []string

	hashOnce sync.Once
	treeHash string
)

func list() {
	listOnce.Do(func() {
		err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				allFiles = append(allFiles, p)
			}
			return nil
		})
		if err != nil {
			// The tree is compiled into the binary; failing to walk it is a
			// build defect, not a runtime condition.
			panic("runtime: walking embedded files: " + err.Error())
		}
		sort.Strings(allFiles)
		for _, p := range allFiles {
			if path.Dir(p) == SourceDir && path.Ext(p) == ".c" {
				cSources = append(cSources, p)
			}
		}
	})
}

// FS returns the embedded tree. Paths are slash-separated and relative to the
// runtime directory, for example "include/tlang.h" or "src/arena.c".
func FS() fs.FS { return files }

// Files returns the slash-separated paths of all embedded files in lexical
// order. The caller may modify the returned slice.
func Files() []string {
	list()
	return append([]string(nil), allFiles...)
}

// CSources returns the C translation units ("src/*.c") in lexical order: the
// files to compile into the runtime library. The caller may modify the
// returned slice.
func CSources() []string {
	list()
	return append([]string(nil), cSources...)
}

// ReadFile returns the contents of one embedded file, named as in Files.
func ReadFile(name string) ([]byte, error) { return files.ReadFile(name) }

// Hash returns the hex SHA-256 of the embedded tree: every path, its length
// and its contents, in lexical path order. It changes whenever any runtime
// file changes, so a cache directory named after it never serves stale
// sources or objects.
func Hash() string {
	hashOnce.Do(func() {
		h := sha256.New()
		for _, name := range Files() {
			data, err := files.ReadFile(name)
			if err != nil {
				panic("runtime: reading embedded file: " + err.Error())
			}
			h.Write([]byte(name))
			h.Write([]byte{0})
			h.Write([]byte(strconv.Itoa(len(data))))
			h.Write([]byte{0})
			h.Write(data)
		}
		treeHash = hex.EncodeToString(h.Sum(nil))
	})
	return treeHash
}

// Extract writes the embedded tree below dir (dir/include/..., dir/src/...),
// creating directories as needed. A file whose current contents already match
// is left untouched, including its modification time. Other files are written
// to a temporary file in the same directory and renamed into place, so a
// concurrent extraction into the same directory never exposes a partially
// written file. Files in dir that are not part of the tree are left alone.
func Extract(dir string) error {
	for _, name := range Files() {
		data, err := files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("runtime: extract %s: %w", name, err)
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, data) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("runtime: extract %s: %w", name, err)
		}
		if err := writeFileAtomic(dst, data); err != nil {
			return fmt.Errorf("runtime: extract %s: %w", name, err)
		}
	}
	return nil
}

// writeFileAtomic writes data to a temporary file next to dst and renames it
// over dst.
func writeFileAtomic(dst string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
