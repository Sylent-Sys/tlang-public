package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"tlang/codegen"
)

// splitPositional pulls the single positional file argument out of args and
// returns it along with the remaining flag arguments, so flags may appear
// before or after the file path. It understands that -o / --o consume the
// following token as their value (also accepting the -o=value form). If more
// than one positional is present, the extras are left in flagArgs so Parse
// reports them via NArg() and the caller fails with a usage error.
func splitPositional(args []string) (file string, flagArgs []string) {
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-o" || a == "--o" {
			flagArgs = append(flagArgs, a)
			if i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			continue
		}
		if !found {
			file = a
			found = true
			continue
		}
		flagArgs = append(flagArgs, a)
	}
	return file, flagArgs
}

// runEmitC implements `tlang emit-c <file.ts> [-o out.c]`: lex+parse+check,
// then on a valid program emit the C translation unit. On diagnostics with
// errors it renders them and returns exitFail without emitting. codegen.Emit
// returns an error (never panics); on error it is printed and exitFail
// returned. On success the C bytes are written to the -o path, or to stdout
// when -o is absent. Warnings are still rendered to stderr.
func runEmitC(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("emit-c", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "write the generated C to this file instead of stdout")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: tlang emit-c <file.ts> [-o out.c]")
		fs.PrintDefaults()
	}
	// The standard flag package stops at the first non-flag argument, so a
	// trailing "-o out.c" after the file path would be left unparsed. Pull
	// the single positional file argument out first, then parse the rest as
	// flags regardless of their position relative to the file.
	file, flagArgs := splitPositional(args)
	if err := fs.Parse(flagArgs); err != nil {
		return exitUsage
	}
	if file == "" || fs.NArg() != 0 {
		fs.Usage()
		return exitUsage
	}
	path := file

	if _, ok := loadSource(path, stderr); !ok {
		return exitUsage
	}

	res := runFrontendGraph(path, stderr)
	if res.hasErrs {
		return exitFail
	}

	cbytes, err := codegen.Emit(res.prog, res.info)
	if err != nil {
		fmt.Fprintf(stderr, "tlang: %v\n", err)
		return exitFail
	}

	if *out != "" {
		if err := os.WriteFile(*out, cbytes, 0o644); err != nil {
			fmt.Fprintf(stderr, "tlang: cannot write %s: %v\n", *out, err)
			return exitFail
		}
		return exitOK
	}

	if _, err := stdout.Write(cbytes); err != nil {
		fmt.Fprintf(stderr, "tlang: cannot write output: %v\n", err)
		return exitFail
	}
	return exitOK
}
