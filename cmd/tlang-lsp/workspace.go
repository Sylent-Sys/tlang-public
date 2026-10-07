package main

import (
	"os"
	"path/filepath"
)

// overlayFS is the module.FileSystem the server analyzes through: the text of
// every open file: document (the editor's possibly unsaved buffer), falling
// back to the disk for files that are not open. Keys are pathKey values.
type overlayFS struct {
	files map[string][]byte
}

// ReadFile implements module.FileSystem.
func (o *overlayFS) ReadFile(absPath string) ([]byte, error) {
	if b, ok := o.files[pathKey(absPath)]; ok {
		return b, nil
	}
	return os.ReadFile(absPath)
}

// IsFile implements module.FileSystem.
func (o *overlayFS) IsFile(absPath string) bool {
	if _, ok := o.files[pathKey(absPath)]; ok {
		return true
	}
	fi, err := os.Stat(absPath)
	return err == nil && !fi.IsDir()
}

// newOverlay snapshots the open file: documents of store.
func newOverlay(store *documentStore) *overlayFS {
	o := &overlayFS{files: map[string][]byte{}}
	for _, uri := range store.uris() {
		p, ok := docPath(uri)
		if !ok {
			continue
		}
		text, _ := store.get(uri)
		o.files[pathKey(p)] = []byte(text)
	}
	return o
}

// projectRoot picks the directory that bounds import resolution for the
// document at docPath: the longest workspace root containing it, else the
// document's own directory (what `tlang check <file>` would use).
func projectRoot(roots []string, docPath string) string {
	best := ""
	for _, r := range roots {
		if withinDir(r, docPath) && len(r) > len(best) {
			best = r
		}
	}
	if best == "" {
		return filepath.Dir(docPath)
	}
	return best
}

// workspaceRoots converts the initialize request's workspace description to
// absolute directories: every workspace folder, else rootUri, else rootPath.
func workspaceRoots(p InitializeParams) []string {
	var roots []string
	for _, f := range p.WorkspaceFolders {
		if dir, ok := docPath(f.URI); ok {
			roots = append(roots, dir)
		}
	}
	if len(roots) > 0 {
		return roots
	}
	if p.RootURI != "" {
		if dir, ok := docPath(p.RootURI); ok {
			return []string{dir}
		}
	}
	if p.RootPath != "" {
		if dir, err := filepath.Abs(p.RootPath); err == nil {
			return []string{dir}
		}
	}
	return nil
}
