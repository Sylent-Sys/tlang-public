# TLang for VS Code

A thin VS Code client that connects to the `tlang-lsp` language server over
stdio. The server does all the real work (diagnostics, completion, hover, and
go-to-definition, all aware of `import`/`export` across files); this extension
only discovers the binary, launches it, wires it to the editor, and ships the
TextMate grammar for syntax highlighting.

The server is the Go binary built from `cmd/tlang-lsp/` in the TLang
repository. See [`docs/LSP.md`](https://github.com/Sylent-Sys/tlang-public/blob/main/docs/LSP.md)
for the architecture and the full feature set.

## Install

Each GitHub release ships one `.vsix` per platform (`tlang-<version>-<target>.vsix`
for `win32-x64`, `win32-arm64`, `linux-x64`, `linux-arm64`, `darwin-x64`,
`darwin-arm64`), each with the matching `tlang-lsp` binary inside, so nothing
else needs installing. In VS Code or Kiro run **Extensions: Install from
VSIX...** and pick the file for your platform. See
[`RELEASING.md`](https://github.com/Sylent-Sys/tlang-public/blob/main/RELEASING.md).

## Build

```sh
cd editors/vscode
npm ci
npm run compile
```

`npm run compile` runs `tsc -p ./` and produces `out/extension.js`. Use
`npm run watch` for incremental rebuilds while developing. To try the extension,
open this folder in VS Code and run the **Run Extension** launch configuration;
it opens an Extension Development Host with the extension loaded.

To build a platform package yourself, put a `tlang-lsp` build for that platform
in `bin/` (`bin/tlang-lsp.exe` on Windows) and run, for example:

```sh
npm run package -- --target linux-x64 -o tlang-0.2.0-linux-x64.vsix
```

`.vscodeignore` keeps the package to `out/`, `syntaxes/`,
`language-configuration.json`, `package.json`, `README.md`, `LICENSE`, `bin/`,
and the production `node_modules`.

## The language server binary

The extension looks for `tlang-lsp` in this order:

1. **`tlang.lsp.path` setting** — the absolute path of a binary. When this
   setting is non-empty it always wins.
2. **The bundled binary** — `bin/tlang-lsp` (`bin/tlang-lsp.exe` on Windows)
   inside the extension, present in the platform-specific `.vsix` packages. On
   Linux and macOS the extension restores the exec bit at activation if the
   install lost it.
3. **PATH fallback** — the bare name `tlang-lsp`, so a copy on your `PATH` is
   used. This is what a development checkout without `bin/` uses; build the
   binary from the repository root with `go build -o tlang-lsp ./cmd/tlang-lsp`.

If the binary cannot be launched, the extension shows an error naming the
command it tried and all three options.

## File handling

The extension registers the `tlang` language id for the `.tlang` extension only.
It never changes how VS Code treats `.ts` files, so TypeScript keeps its default
behavior out of the box.

If you keep TLang sources with a `.ts` extension and want them analyzed as
TLang, opt in explicitly — neither option is forced on you:

- Per file: run **Change Language Mode** (from the Command Palette) and pick
  **TLang** for the open file.
- Per workspace: add an association in `.vscode/settings.json`:

  ```json
  {
    "files.associations": {
      "*.ts": "tlang"
    }
  }
  ```

## Multi-file projects

Open the project folder (or a parent of it) as the workspace. The server
resolves each file's imports against the workspace folder that contains it,
using the text of open editors (unsaved changes included) and the disk for
everything else, so imported names resolve, hover, complete, and jump to their
declarations (F12 / Ctrl+click). A file outside every workspace folder resolves
imports against its own directory. Buffers that were never saved (`untitled:`)
are analyzed alone. See [`docs/LSP.md`](https://github.com/Sylent-Sys/tlang-public/blob/main/docs/LSP.md)
for the details.
