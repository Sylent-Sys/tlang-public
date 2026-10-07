package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cInclude = `#include "tlang.h"`

func TestRunEmitC(t *testing.T) {
	valid := writeFixture(t, "ok.ts", validProgram)
	invalid := writeFixture(t, "bad.ts", invalidProgram)

	t.Run("valid program emits C to stdout", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"emit-c", valid}, &out, &errb)
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, errb.String())
		}
		if !strings.Contains(out.String(), cInclude) {
			t.Errorf("stdout = %q, want substring %q", out.String(), cInclude)
		}
	})

	t.Run("invalid program emits no C and fails", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"emit-c", invalid}, &out, &errb)
		if code != exitFail {
			t.Fatalf("exit code = %d, want %d", code, exitFail)
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want no C output", out.String())
		}
		if !strings.Contains(errb.String(), "error:") {
			t.Errorf("stderr = %q, want a rendered diagnostic", errb.String())
		}
	})

	t.Run("-o writes C to a file", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "out.c")
		var out, errb bytes.Buffer
		code := run([]string{"emit-c", valid, "-o", outPath}, &out, &errb)
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, errb.String())
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want nothing when -o is given", out.String())
		}
		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("reading output file: %v", err)
		}
		if !strings.Contains(string(data), cInclude) {
			t.Errorf("output file = %q, want substring %q", data, cInclude)
		}
	})

	t.Run("-o before the file path also works", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "out.c")
		var out, errb bytes.Buffer
		code := run([]string{"emit-c", "-o", outPath, valid}, &out, &errb)
		if code != exitOK {
			t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, errb.String())
		}
		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("reading output file: %v", err)
		}
		if !strings.Contains(string(data), cInclude) {
			t.Errorf("output file = %q, want substring %q", data, cInclude)
		}
	})

	t.Run("missing file is a usage error", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"emit-c", filepath.Join(t.TempDir(), "nope.ts")}, &out, &errb)
		if code != exitUsage {
			t.Fatalf("exit code = %d, want %d", code, exitUsage)
		}
	})

	t.Run("no file arg is a usage error", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"emit-c"}, &out, &errb)
		if code != exitUsage {
			t.Fatalf("exit code = %d, want %d", code, exitUsage)
		}
	})
}
