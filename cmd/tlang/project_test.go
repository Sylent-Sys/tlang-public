package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cliManifest = `{"schemaVersion":1,"language":"1","entry":"app/main.tlang","target":{"os":[],"arch":[]},"capabilities":{"env":[],"filesystem":[],"process":[],"network":{"connect":[],"listen":[]},"lifecycle":{"signals":[]}},"databases":{},"limits":{}}`

// TestCheckManifestDirectorySelectsEntryAndUsesProjectRoot records the CLI
// contract: a directory input selects tlang.json's entry, and imports resolve
// from the manifest directory rather than the entry file's subdirectory.
func TestCheckManifestDirectorySelectsEntryAndUsesProjectRoot(t *testing.T) {
	root := t.TempDir()
	writeCLIProjectFile(t, root, "tlang.json", cliManifest)
	writeCLIProjectFile(t, root, "app/main.tlang", "import { dep } from \"../lib/dep\";\nfn main(): void { dep(); }\n")
	writeCLIProjectFile(t, root, "lib/dep.tlang", "export fn dep(): void {}\n")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", root}, &stdout, &stderr); code != exitOK {
		t.Fatalf("check project directory exit = %d, stderr: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), filepath.Join(root, "app", "main.tlang")) {
		t.Fatalf("stdout = %q, want selected manifest entry", stdout.String())
	}
	generated := mustEmitFrontend(t, filepath.Join(root, "app", "main.tlang"), root)
	digest := sha256.Sum256([]byte(cliManifest))
	if !strings.Contains(string(generated), hex.EncodeToString(digest[:])) {
		t.Fatalf("generated metadata missing manifest digest or containing grant material")
	}
}

func mustEmitFrontend(t *testing.T, entry, root string) []byte {
	t.Helper()
	res := runFrontendGraph(entry, root, nil, &bytes.Buffer{})
	if res.hasErrs {
		t.Fatal("manifest fixture failed type checking")
	}
	generated, err := emitProgram(res)
	if err != nil {
		t.Fatal(err)
	}
	return generated
}

// TestCheckManifestRequiresExplicitFileToMatchEntry documents the CLI rule
// that a file argument inside a manifest project cannot silently override its
// declared entry.
func TestCheckManifestRequiresExplicitFileToMatchEntry(t *testing.T) {
	root := t.TempDir()
	writeCLIProjectFile(t, root, "tlang.json", cliManifest)
	entry := writeCLIProjectFile(t, root, "app/main.tlang", "fn main(): void {}\n")
	other := writeCLIProjectFile(t, root, "app/other.tlang", "fn main(): void {}\n")

	var entryOut, entryErr bytes.Buffer
	if code := run([]string{"check", entry}, &entryOut, &entryErr); code != exitOK {
		t.Fatalf("check manifest entry exit = %d, stderr: %q", code, entryErr.String())
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", other}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("check non-entry exit = %d, want exitUsage (stderr: %q)", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "input does not match manifest entry") || strings.Contains(got, root) {
		t.Fatalf("entry mismatch diagnostic = %q, want a redacted project error", got)
	}
}

// TestCheckWithoutManifestKeepsLegacyRoot confirms manifest discovery is
// optional: legacy file input remains its own module root with no new boundary.
func TestCheckWithoutManifestKeepsLegacyRoot(t *testing.T) {
	root := t.TempDir()
	path := writeCLIProjectFile(t, root, "app/main.tlang", "import { dep } from \"../lib/dep\";\nfn main(): void { dep(); }\n")
	writeCLIProjectFile(t, root, "lib/dep.tlang", "export fn dep(): void {}\n")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", path}, &stdout, &stderr); code != exitFail {
		t.Fatalf("legacy check exit = %d, want exitFail from the old file-directory import boundary (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "escapes the project root") {
		t.Fatalf("legacy diagnostic = %q, want parent import rejected without a manifest", stderr.String())
	}
}

// TestManifestDiscoveryDiagnosticsDoNotExposeManifestContents checks the CLI
// boundary uses project package's content-redacted errors for malformed JSON.
func TestManifestDiscoveryDiagnosticsDoNotExposeManifestContents(t *testing.T) {
	root := t.TempDir()
	writeCLIProjectFile(t, root, "tlang.json", `{"password":"swordfish"}`)
	path := writeCLIProjectFile(t, root, "main.tlang", "fn main(): void {}\n")
	var stderr bytes.Buffer
	if _, _, ok := resolveProjectInput(path, &stderr); ok {
		t.Fatal("malformed manifest was accepted")
	}
	if strings.Contains(stderr.String(), "swordfish") || !strings.Contains(stderr.String(), "project: invalid manifest") {
		t.Fatalf("manifest diagnostic = %q, want redacted project error", stderr.String())
	}
}

func writeCLIProjectFile(t *testing.T, root, relative, contents string) string {
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
