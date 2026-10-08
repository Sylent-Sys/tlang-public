// Command tlang is the user-facing CLI for the TLang compiler front end.
//
// Subcommands:
//
//	tlang version          print the compiler version
//	tlang check <file>     lex+parse+type-check and report diagnostics
//	tlang emit-c <file>    emit the generated C translation unit
//	tlang run <file>       (FEAT-002) compile and run
//	tlang build <file>     (FEAT-002) compile to an executable
//
// All real work lives in run(args, stdout, stderr), which takes injected
// writers so the whole CLI is unit-testable without spawning a process.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is the compiler version reported by `tlang version`. It is a var so
// release builds can stamp it with -ldflags "-X main.version=<v>".
var version = "0.2.0"

// Exit codes form a stable scheme shared by every subcommand:
//
//	exitOK    (0) success.
//	exitFail  (1) compilation failed: diagnostics with errors, or a
//	              codegen/driver error (e.g. ErrNoEntry/ErrNoCompiler/
//	              ErrNoArchiver).
//	exitUsage (2) CLI usage error: unknown subcommand, missing/extra args,
//	              an unreadable input file, or a bad flag.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// usageText is printed for help, no-args, and unknown-subcommand cases.
const usageText = `tlang is the TLang compiler.

Usage:
	tlang <command> [arguments]

Commands:
	version          print the compiler version
	check <file>     type-check a source file and report diagnostics
	emit-c <file>    emit the generated C translation unit (use -o to write a file)
	run <file>       compile and run a source file
	build <file>     compile a source file to an executable

Note: run and build assemble a driver plan and execute it against the embedded
C runtime, producing (build) or executing (run) a native binary. run is
tcc-oriented for fast iteration (an empty output selects tcc's -run form); with
--cc gcc/clang the program is compiled and then executed.
`

// run dispatches a single CLI invocation and returns the process exit code.
// stdout/stderr are injected so tests can capture output directly.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usageText)
		return exitOK
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return exitOK
	case "version":
		return runVersion(rest, stdout, stderr)
	case "check":
		return runCheck(rest, stdout, stderr)
	case "emit-c":
		return runEmitC(rest, stdout, stderr)
	case "run":
		return runRun(rest, stdout, stderr)
	case "build":
		return runBuild(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tlang: unknown command %q\n\n", cmd)
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
}

// runVersion implements `tlang version`.
func runVersion(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "tlang version: unexpected arguments %v\n", args)
		return exitUsage
	}
	fmt.Fprintf(stdout, "tlang %s\n", version)
	return exitOK
}
