// Command tlang-lsp is the TLang language server. It speaks the Language
// Server Protocol over stdio: it reads Content-Length-framed JSON-RPC 2.0
// messages from standard input, writes responses and notifications to
// standard output, and logs to standard error. All compilation goes through
// the existing front end: a file: document is analyzed in its module graph
// (module.BuildWith over the open buffers, then checker.CheckProgram), any
// other document alone (parser.ParseSource and checker.Check). The server
// adds only a thin query layer over types.Info for diagnostics, completion,
// hover, and go-to-definition. The whole server runs inside run, which takes
// injected io.Reader/io.Writer values so it is unit-testable without a real
// process.
package main

import "os"

// serverName and version are reported in the initialize result's serverInfo.
// version is a var so release builds can stamp it with
// -ldflags "-X main.version=<v>".
const serverName = "tlang-lsp"

var version = "0.1.0-dev"

func main() { os.Exit(run(os.Stdin, os.Stdout, os.Stderr)) }
