# TLang Language Server

This document describes the TLang Language Server Protocol (LSP) support: a Go
server plus a thin TypeScript VS Code client. It covers the architecture, the
feature set, module-aware analysis, the position-mapping contract, and the
stdlib-only constraint on the Go module.

## Architecture

Two pieces talk over LSP:

- **Server** — the Go binary built from `cmd/tlang-lsp/` (package `main`). It
  speaks JSON-RPC 2.0 with `Content-Length` framing over stdio and is written
  with the standard library only (no third-party Go modules). The editor owns
  the text of every open document and pushes it to the server via full-text
  sync; the server reads from disk only the files a document imports that are
  not open. Analysis reuses the compiler front end exactly as the CLI does (see
  [Module-aware analysis](#module-aware-analysis)). On top of that front end the
  server adds only a thin query layer over `types.Info` for completion, hover,
  and go-to-definition.

- **Client** — the VS Code extension under `editors/vscode/`. It is intentionally
  thin: it discovers the server binary, launches it over stdio, and registers
  the `tlang` language. It carries npm/TypeScript dependencies; those live only
  under `editors/vscode/` and are not part of the Go build. The client resolves
  the binary in this order: a non-empty `tlang.lsp.path` setting, then the
  `tlang-lsp` bundled inside a platform-specific `.vsix`
  (`<extensionPath>/bin/tlang-lsp`, `.exe` on Windows), then the bare name
  `tlang-lsp` on `PATH`; when the binary cannot be launched it shows an error
  naming all three. Releases publish one `.vsix` per platform (see
  [`../RELEASING.md`](../RELEASING.md)). See
  [`../editors/vscode/README.md`](../editors/vscode/README.md).

  The server reports its name and version in the `initialize` result's
  `serverInfo` (`tlang-lsp`, currently `0.1.0-dev` in a plain `go build`;
  release builds stamp the release version with `-ldflags
  "-X main.version=<v>"`). The VS Code extension has its own package version,
  currently `0.2.0`; it is not the compiler or language-server version.

```
VS Code  ──(open documents)──►  extension (editors/vscode)
                                      │  spawn stdio
                                      ▼
                                 tlang-lsp (cmd/tlang-lsp)
                                      │  module.BuildWith (open buffers, then disk)
                                      │  + checker.CheckProgram
                                      ▼
                     diagnostics / completion / hover / definition
```

## Feature set

The server advertises full-text sync, completion (triggered on `.` and `@`),
hover, go-to-definition, and workspace-folder change notifications.

- **Diagnostics** — published for every open document on open, change, save,
  and close, with severity mapped to the LSP scale and `source` set to `tlang`.
  A document receives only the diagnostics of its own file. Its build
  diagnostics (lex, parse, and `E-IMPORT` resolution) are always shown, and its
  checker diagnostics unless its own file has such errors (as `tlang check`
  stops at them). A file it imports that does not build (itself, or through
  its own imports) is reported once at the importing specifier, for example
  `imported module "a.ts" has errors`, since that file may not be open; it
  does not hide the document's other diagnostics. Closing a document clears
  its diagnostics.
- **Completion** — plain completions offer the TLang keywords (including
  `import`, `export`, `from`, `as`, `default`), universe names, the document's
  own top-level functions, globals and types, every import binding (named,
  aliased, default, and namespace), and the locals/params of the enclosing
  function. Member completions (after a `.`) resolve the receiver and enumerate
  its fields and methods, including the built-in namespaces such as `db.` and
  `console.`; after a namespace import `m.` they list the target module's
  exports, in an expression or a type annotation (`m.User`). Inside the braces
  of `import { … } from "./y"` or `export { … } from "./y"` they list the
  exports of `./y`, and after `@` (outside strings and comments) the
  decorators the checker accepts (`Use`, `After`).
- **Hover** — the type of a variable, the signature of a function or method, or
  the description of a type name or built-in, rendered as a `tlang` code block.
  This covers imported names, `m.X` and qualified types `m.User`, a namespace
  name (shown with the module it names), the names and the specifier inside
  import and re-export declarations, and the guard/hook names inside
  `@Use(...)` and `@After(...)`.
- **Go-to-definition** — returns one `Location`, or `null` when nothing at the
  position is declared in source (a built-in, white space). It resolves:
  - a use of a local or imported name, a member, a decorator argument, or a
    qualified type `m.User`, to the declaring identifier;
  - a name inside `import { X }`, `import { X as Y }` (either side), or
    `export { X } from`, to the original declaration, following re-export
    chains;
  - a default import binding to the `export default` declaration;
  - a namespace name `m` and a specifier string (`from "./y"`) to the start of
    the target file.

  A target in an open document uses that document's URI exactly as the client
  sent it; other targets get a `file:` URI. Positions in open documents refer
  to the editor buffer, not the saved file.

## Module-aware analysis

A document with a `file:` URI is analyzed in its module graph, the way `tlang
check` analyzes its root file:

1. `module.BuildWith(<document path>, BuildOptions{FS: overlay, RootDir:
   <project root>})` builds the graph with the document as the root module.
   The overlay serves the text of every open document (unsaved edits included)
   and falls back to the disk for files that are not open, so an import of a
   buffer that was never saved resolves.
2. `checker.CheckProgram(graph.Modules)` checks the graph as one program.

The **project root** bounds import resolution and is the base of module IDs.
It is the workspace folder that contains the document (from the `initialize`
request's `workspaceFolders`, else `rootUri`, else `rootPath`), choosing the
longest when folders nest; a document outside every folder uses its own
directory, which is what `tlang check <file>` uses. Inside a workspace folder,
`../` imports from a subdirectory therefore resolve as long as they stay inside
the folder. The server asks the client for workspace-folder changes, so a
folder added to or removed from a multi-root workspace takes effect for the
open documents right away.

The checker also runs on a graph with build errors (unresolved imports are
skipped), so completion, hover, and definition keep working while a line is
half typed; only its diagnostics are withheld, as above.

Any open, change, save, or close re-analyzes every open document, so an edit to
a file refreshes the diagnostics of every open document that imports it,
directly or transitively. The analysis of each open document is cached between
those events and serves completion, hover, and definition.

The server does not watch the disk. A file that is not open and changes on
disk (a `git checkout`, generated code, an edit in another program) is read
again only at the next open, change, save, or close of a document, or a
workspace-folder change; until then the open documents that import it show
results for its previous content.

A document without a `file:` URI (for example an `untitled:` buffer) has no
place on disk to resolve imports from, so it is analyzed alone with
`parser.ParseSource` and `checker.Check`; its imported names show as
unresolved.

## Position-mapping contract

LSP positions are `(line, character)` pairs; the compiler works in byte offsets
and 1-based `token.Position` coordinates. The server converts between them:

- LSP `(line, character)` → byte offset, and byte offset → LSP `(line,
  character)`, clamped to valid ranges.
- A compiler `token.Position` → an LSP range. A valid position maps its start to
  `(Line-1, Column-1)`; the end is widened over the trailing ASCII identifier run
  (`[A-Za-z0-9_]`) when present and is zero-width otherwise. An invalid position
  (`Line == 0`) maps to the `(0,0)-(0,0)` range so a diagnostic is never dropped
  and never produces negative coordinates.

**ASCII correct now, UTF-16 deferred.** The LSP specification measures
`character` in UTF-16 code units. The current mapping treats one byte as one
character, which is correct for ASCII source. Multi-byte UTF-8 (and surrogate-pair)
handling is deliberately deferred; columns in lines containing non-ASCII
characters may be off until that work lands.

`file:` URIs are converted to paths with the platform's rules: on Windows
`file:///c%3A/dir/a.tlang` and `file:///C:/dir/a.tlang` both name
`C:\dir\a.tlang` (paths are compared case-insensitively there), and
`file://host/share/a.tlang` is a UNC path. The authority-less form
(`file:/C:/dir/a.tlang`, `file:/home/a.tlang`) is accepted too.

## Editor setup

The VS Code extension activates for `.tlang` files only. TLang sources named
`.ts` need an explicit association, for example in `.vscode/settings.json`:

```json
{
  "files.associations": {
    "*.ts": "tlang"
  }
}
```

## Stdlib-only Go module (NFR1)

The server is built with the Go standard library only. The module stays
`module tlang` / `go 1.26` with no `require` block, and this feature adds no Go
dependency. All npm/TypeScript dependencies belong to the VS Code client under
`editors/vscode/` and never touch `go.mod`.
