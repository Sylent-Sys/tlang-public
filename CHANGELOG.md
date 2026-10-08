# Changelog

User-facing changes are recorded here for each TLang release.

## [0.2.0] - 2026-10-08

This is the first release from the clean `Sylent-Sys/tlang-public` repository.
It includes the TLang v1 compiler/runtime and the improvements listed below.

### Added

- ES-style multi-file modules with imports, exports, re-exports, namespace
  imports, and default exports.
- A Go language server with diagnostics, completion, hover, and go-to-definition,
  plus a VS Code extension that bundles platform-specific language servers.
- Chunked HTTP/1.1 request-body decoding and `@After` post-handler hooks.
- CI and tagged GitHub release workflows for compiler, language-server, and
  VS Code extension packages.

### Fixed

- Server shutdown and signal handling using a main-thread `signalfd` wait.
- Partial server-startup resource cleanup and script-mode SIGINT/SIGTERM handling.

### Notes

- The runtime is licensed under Apache-2.0 WITH LLVM-exception; the rest of the
  repository is licensed under MPL-2.0.
- The repository is currently private, so GitHub release assets are only
  downloadable by collaborators until its visibility changes.
