package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestURIToPathWindows(t *testing.T) {
	cases := []struct {
		uri  string
		want string
		ok   bool
	}{
		{"file:///c%3A/Users/x/a.tlang", `C:\Users\x\a.tlang`, true},
		{"file:///C:/Users/x/a%20b.tlang", `C:\Users\x\a b.tlang`, true},
		{"file:///d%3a/proj/main.ts", `D:\proj\main.ts`, true},
		{"FILE:///c:/x.ts", `C:\x.ts`, true},
		{"file://localhost/c%3A/x.ts", `C:\x.ts`, true},
		{"file:///c%3A", `C:\`, true},
		{"file://server/share/a.tlang", `\\server\share\a.tlang`, true},
		{"file:///c%3A/dir/%23hash.ts", `C:\dir\#hash.ts`, true},
		{"file:/C:/x/a.ts", `C:\x\a.ts`, true}, // authority-less form
		{"file:/c%3A/x", `C:\x`, true},
		{"untitled:Untitled-1", "", false},
		{"file://", "", false},
		{"file:", "", false},
		{"file:C:/x", "", false},
		{"file:///c%3A/bad%zz.ts", "", false},
	}
	for _, tc := range cases {
		got, ok := uriToPathOS(tc.uri, true)
		if ok != tc.ok || got != tc.want {
			t.Errorf("uriToPathOS(%q, windows) = %q, %v; want %q, %v", tc.uri, got, ok, tc.want, tc.ok)
		}
	}
}

func TestURIToPathPOSIX(t *testing.T) {
	cases := []struct {
		uri  string
		want string
		ok   bool
	}{
		{"file:///home/u/a.tlang", "/home/u/a.tlang", true},
		{"file:///home/u/a%20b.tlang", "/home/u/a b.tlang", true},
		{"file://localhost/tmp/x.ts", "/tmp/x.ts", true},
		{"file:///tmp/%23x.ts", "/tmp/#x.ts", true},
		{"file:/home/u/a.tlang", "/home/u/a.tlang", true}, // authority-less form
		{"file:/", "/", true},
		{"file:home/u", "", false},
		{"file://server/share/a.tlang", "", false},
		{"untitled:Untitled-1", "", false},
	}
	for _, tc := range cases {
		got, ok := uriToPathOS(tc.uri, false)
		if ok != tc.ok || got != tc.want {
			t.Errorf("uriToPathOS(%q, posix) = %q, %v; want %q, %v", tc.uri, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPathToURI(t *testing.T) {
	cases := []struct {
		path    string
		windows bool
		want    string
	}{
		{`C:\Users\x\a b.tlang`, true, "file:///c%3A/Users/x/a%20b.tlang"},
		{`d:\proj\main.ts`, true, "file:///d%3A/proj/main.ts"},
		{`\\server\share\a.tlang`, true, "file://server/share/a.tlang"},
		{"/home/u/a b.tlang", false, "file:///home/u/a%20b.tlang"},
		{"/tmp/#x.ts", false, "file:///tmp/%23x.ts"},
	}
	for _, tc := range cases {
		if got := pathToURIOS(tc.path, tc.windows); got != tc.want {
			t.Errorf("pathToURIOS(%q, %v) = %q, want %q", tc.path, tc.windows, got, tc.want)
		}
		back, ok := uriToPathOS(tc.want, tc.windows)
		wantBack := tc.path
		if tc.windows && wantBack[1] == ':' {
			wantBack = string(wantBack[0]&^0x20) + wantBack[1:] // drive letter upper-cased
		}
		if !ok || back != wantBack {
			t.Errorf("round trip of %q = %q, %v; want %q", tc.path, back, ok, wantBack)
		}
	}
}

// TestDocPathNativeRoundTrip: on the host OS, an absolute path survives
// pathToURI then docPath (modulo the canonical drive-letter case).
func TestDocPathNativeRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dir with space", "a.tlang")
	got, ok := docPath(pathToURI(p))
	if !ok || pathKey(got) != pathKey(p) {
		t.Fatalf("docPath(pathToURI(%q)) = %q, %v", p, got, ok)
	}
}

func TestProjectRoot(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	sub := filepath.Join(ws, "sub")
	roots := []string{ws, sub}

	if got := projectRoot(roots, filepath.Join(sub, "x", "a.ts")); got != sub {
		t.Errorf("nested document: root = %q, want the longest folder %q", got, sub)
	}
	if got := projectRoot(roots, filepath.Join(ws, "a.ts")); got != ws {
		t.Errorf("root = %q, want %q", got, ws)
	}
	// A sibling sharing a name prefix is not inside the folder.
	other := filepath.Join(base, "ws2", "a.ts")
	if got := projectRoot(roots, other); got != filepath.Dir(other) {
		t.Errorf("outside every folder: root = %q, want the document's directory", got)
	}
	if got := projectRoot(nil, other); got != filepath.Dir(other) {
		t.Errorf("no folders: root = %q, want the document's directory", got)
	}
}

func TestWorkspaceRoots(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	got := workspaceRoots(InitializeParams{
		RootURI:          pathToURI(b),
		WorkspaceFolders: []WorkspaceFolder{{URI: pathToURI(a)}, {URI: "untitled:x"}},
	})
	if len(got) != 1 || pathKey(got[0]) != pathKey(a) {
		t.Errorf("workspace folders win: got %v, want [%s]", got, a)
	}
	if got := workspaceRoots(InitializeParams{RootURI: pathToURI(b), RootPath: a}); len(got) != 1 || pathKey(got[0]) != pathKey(b) {
		t.Errorf("rootUri before rootPath: got %v, want [%s]", got, b)
	}
	if got := workspaceRoots(InitializeParams{RootPath: a}); len(got) != 1 || pathKey(got[0]) != pathKey(a) {
		t.Errorf("rootPath fallback: got %v, want [%s]", got, a)
	}
	if got := workspaceRoots(InitializeParams{}); got != nil {
		t.Errorf("no workspace: got %v, want none", got)
	}
}

// TestOverlayFS: open buffers win over disk, and missing files fall through
// to disk.
func TestOverlayFS(t *testing.T) {
	dir := t.TempDir()
	onDisk := filepath.Join(dir, "disk.ts")
	if err := os.WriteFile(onDisk, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newDocumentStore()
	store.open(pathToURI(onDisk), "buffer")
	store.open(pathToURI(filepath.Join(dir, "new.ts")), "unsaved")
	store.open("untitled:Untitled-1", "ignored")
	o := newOverlay(store)

	if b, err := o.ReadFile(onDisk); err != nil || string(b) != "buffer" {
		t.Errorf("ReadFile(open file) = %q, %v; want the buffer", b, err)
	}
	if !o.IsFile(filepath.Join(dir, "new.ts")) {
		t.Error("an open buffer with no file on disk should exist")
	}
	if o.IsFile(dir) {
		t.Error("a directory is not a file")
	}
	if _, err := o.ReadFile(filepath.Join(dir, "missing.ts")); err == nil {
		t.Error("ReadFile of a missing file should fail")
	}
	if len(o.files) != 2 {
		t.Errorf("overlay holds %d files, want the 2 file: documents", len(o.files))
	}
}
