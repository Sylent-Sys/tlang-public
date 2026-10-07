//go:build e2e

// Unit tests for the harness's ASan log-file check (design §4.4): pure
// filesystem checks, no server or toolchain needed.

package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeASanLog(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestASanLogEmptyDir(t *testing.T) {
	found, err := scanSanitizerLogs(t.TempDir())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("found %d logs in an empty dir, want 0", len(found))
	}
}

func TestASanLogZeroByteIsClean(t *testing.T) {
	dir := t.TempDir()
	writeASanLog(t, dir, "asan.123", "")
	found, err := scanSanitizerLogs(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("found %d logs for a zero-byte file, want 0", len(found))
	}
}

func TestASanLogNonEmptyFound(t *testing.T) {
	dir := t.TempDir()
	const report = "==456==ERROR: AddressSanitizer: heap-use-after-free\n"
	p := writeASanLog(t, dir, "asan.456", report)
	found, err := scanSanitizerLogs(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("found %d logs, want 1", len(found))
	}
	if found[0].path != p {
		t.Errorf("path = %q, want %q", found[0].path, p)
	}
	if string(found[0].content) != report || found[0].truncated {
		t.Errorf("content = %q (truncated=%v), want %q", found[0].content, found[0].truncated, report)
	}
}

func TestASanLogMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	writeASanLog(t, dir, "asan.1", "report one")
	writeASanLog(t, dir, "asan.2", "")
	writeASanLog(t, dir, "asan.3", "report three")
	writeASanLog(t, dir, "other.log", "unexpected file")
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := scanSanitizerLogs(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := map[string]string{}
	for _, l := range found {
		got[filepath.Base(l.path)] = string(l.content)
	}
	want := map[string]string{"asan.1": "report one", "asan.3": "report three", "other.log": "unexpected file"}
	if len(got) != len(want) {
		t.Fatalf("found %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestASanLogTruncated(t *testing.T) {
	dir := t.TempDir()
	writeASanLog(t, dir, "asan.9", strings.Repeat("x", asanLogCap+10))
	found, err := scanSanitizerLogs(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(found) != 1 || !found[0].truncated || len(found[0].content) != asanLogCap {
		t.Fatalf("want one truncated log of %d bytes, got %d logs", asanLogCap, len(found))
	}
}

func TestASanLogMissingDir(t *testing.T) {
	if _, err := scanSanitizerLogs(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("scan of a missing dir succeeded, want an error")
	}
}

func TestASanLogSafeTestName(t *testing.T) {
	cases := map[string]string{
		"TestE2E/clang-asan-ubsan/echo": "TestE2E_clang-asan-ubsan_echo",
		"TestX/a b:c*?":                 "TestX_a_b_c",
		"":                              "test",
		"///":                           "test",
		"Test.v1":                       "Test.v1",
	}
	for in, want := range cases {
		if got := safeTestName(in); got != want {
			t.Errorf("safeTestName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := safeTestName(strings.Repeat("a", 300)); len(got) != 100 {
		t.Errorf("long name length = %d, want 100", len(got))
	}
}

func TestASanLogDirUnderRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	t.Setenv("TLANG_ASAN_LOG_DIR", root)
	dir, err := newASanLogDir(t)
	if err != nil {
		t.Fatalf("newASanLogDir: %v", err)
	}
	if filepath.Dir(dir) != root {
		t.Errorf("dir %s not directly under %s", dir, root)
	}
	if !strings.HasPrefix(filepath.Base(dir), "TestASanLogDirUnderRoot-") {
		t.Errorf("dir name %s lacks the test-name prefix", filepath.Base(dir))
	}
	env := sanitizerEnv(dir)
	if !strings.HasSuffix(env[0], ":log_path="+filepath.Join(dir, "asan")) {
		t.Errorf("ASAN_OPTIONS = %q, want log_path=%s", env[0], filepath.Join(dir, "asan"))
	}
}
