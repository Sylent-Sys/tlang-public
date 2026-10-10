package main

import (
	"os"
	"path/filepath"
	"testing"
)

const lspManifest = `{"schemaVersion":1,"language":"1","entry":"app/main.tlang","target":{"os":[],"arch":[]},"capabilities":{"env":[],"filesystem":[],"process":[],"network":{"connect":[],"listen":[]},"lifecycle":{"signals":[]}},"databases":{},"limits":{}}`

// TestProjectRootDiscoversManifestAboveWorkspace proves a workspace opened at
// a source subdirectory still uses an ancestor manifest as the module import
// root. Manifest loading is metadata-only: open source buffers continue to be
// read through the overlay filesystem.
func TestProjectRootDiscoversManifestAboveWorkspace(t *testing.T) {
	root := t.TempDir()
	writeLSPProjectFile(t, root, "tlang.json", lspManifest)
	writeLSPProjectFile(t, root, "tlang-grants.json", `{"databaseUrl":"secret-must-not-be-read"}`)
	writeLSPProjectFile(t, root, "app/main.tlang", "import { dep } from \"../lib/dep\";\nfn main(): void { dep(); }\n")
	workspace := filepath.Join(root, "app")
	entry := filepath.Join(workspace, "main.tlang")
	dep := filepath.Join(root, "lib", "dep.tlang")

	if got := projectRoot([]string{workspace}, entry); pathKey(got) != pathKey(root) {
		t.Fatalf("projectRoot() = %q, want discovered manifest root %q", got, root)
	}
	fsys := &overlayFS{files: map[string][]byte{
		pathKey(entry): []byte("import { dep } from \"../lib/dep\";\nfn main(): void { dep(); }\n"),
		pathKey(dep):   []byte("export fn dep(): void {}\n"),
	}}
	a := analyzeModule(entry, projectRoot([]string{workspace}, entry), fsys, string(fsys.files[pathKey(entry)]))
	if a.mc == nil || a.mc.rootDir != root || len(a.diags) != 0 {
		t.Fatalf("manifest-root analysis = context %+v diagnostics %+v, want root %q and clean overlay analysis", a.mc, a.diags, root)
	}
}

// TestProjectRootWithoutManifestKeepsWorkspaceBoundary protects the existing
// workspace-root behavior when no tlang.json exists on its ancestor chain.
func TestProjectRootWithoutManifestKeepsWorkspaceBoundary(t *testing.T) {
	workspace := t.TempDir()
	entry := filepath.Join(workspace, "app", "main.tlang")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := projectRoot([]string{workspace}, entry); pathKey(got) != pathKey(workspace) {
		t.Fatalf("projectRoot() = %q, want legacy workspace root %q", got, workspace)
	}
}

func writeLSPProjectFile(t *testing.T, root, relative, contents string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
