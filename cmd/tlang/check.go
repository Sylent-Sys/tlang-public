package main

import (
	"fmt"
	"io"
)

// runCheck implements `tlang check <file.ts>`: lex+parse+type-check and report
// all diagnostics to stderr. Returns exitFail if either diagnostic list has
// errors, otherwise exitOK. It does NOT run codegen.
func runCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: tlang check <file.ts>")
		return exitUsage
	}
	path := args[0]

	if _, ok := loadSource(path, stderr); !ok {
		return exitUsage
	}

	res := runFrontendGraph(path, stderr)
	if res.hasErrs {
		return exitFail
	}
	fmt.Fprintf(stdout, "ok: %s\n", path)
	return exitOK
}
