# Releasing TLang

Releases are built by [`.github/workflows/release.yml`](.github/workflows/release.yml)
and published to GitHub Releases only (no Marketplace or Open VSX). The
workflow uses nothing but the built-in `GITHUB_TOKEN`.

## Cutting a release

1. **Bump the extension version.** Set `"version"` in
   [`editors/vscode/package.json`](editors/vscode/package.json) to the release
   version without the `v` (e.g. `0.3.0`), then run `npm install` in
   `editors/vscode/` so `package-lock.json` matches. The release fails if the
   tag and this version differ; the workflow never rewrites it for you.
2. Update the release notes or other user-facing release documentation with
   what the release contains.
3. Merge both through a PR to `main` and wait for CI to pass on `main`.
4. **Tag and push the tag** from the merged commit on `main`:

   ```sh
   git tag v0.3.0
   git push origin v0.3.0
   ```

   A version with a `-` (e.g. `v0.3.0-rc.1`) is published as a prerelease.

The tag push starts the workflow. It publishes only after the full CI
(including the `tlang-dev` container matrix with PostgreSQL 17), the binaries
job and the extension job have all passed. If a step fails, fix it on `main`,
delete the tag (`git push origin :refs/tags/v0.3.0` and `git tag -d v0.3.0`) and
tag again.

## What the pipeline produces

Every run (real or dry) builds:

- `tlang_<v>_<os>_<arch>.tar.gz` and `tlang-lsp_<v>_<os>_<arch>.tar.gz` for
  linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64, and `.zip` instead
  of `.tar.gz` for windows/amd64 and windows/arm64. Each archive holds one
  directory with the binary, `LICENSE` (MPL-2.0), `LICENSE.runtime`
  (Apache-2.0 WITH LLVM-exception, from `runtime/LICENSE`) and `README.md`.
  Binaries are built with `CGO_ENABLED=0`, `-trimpath` and
  `-ldflags "-s -w -X main.version=<v>"`, so `tlang version` and the language
  server's `serverInfo` report the release version.
- `tlang-<v>-<target>.vsix` for the VS Code targets `win32-x64`,
  `win32-arm64`, `linux-x64`, `linux-arm64`, `darwin-x64` and `darwin-arm64`.
  Each bundles the matching `tlang-lsp` in `bin/`, with the exec bit set on
  Linux and macOS.

A real release also attaches `SHA256SUMS` and generates release notes from the
merged PRs.

## Dry runs

A dry run builds and uploads everything as workflow artifacts
(`release-binaries`, `release-vsix`) and never creates a release. Dry runs
use the version `0.0.0-dev+<short sha>` unless they run on a tag.

- **Manually:** Actions → Release → Run workflow. `dry_run` defaults to true.
  Unticking it creates a real release, which is only allowed when the run is
  started from a `v*` tag (pick the tag under "Use workflow from").
- **On PRs:** any PR that touches `.github/workflows/**`, `editors/vscode/**`,
  `cmd/**` or `LICENSE` runs a dry run with the fast CI checks (the full
  container matrix is skipped on PRs to save Actions minutes).

## Installing a release

GitHub Releases are publicly visible when the repository is public. Download
with the browser, or with
`gh release download v0.3.0 --repo Sylent-Sys/tlang-public`.

### The compiler

Download the `tlang` archive for your platform, extract it and put `tlang`
(`tlang.exe` on Windows) on your `PATH`. Check it with `tlang version`.

`tlang check` and `tlang emit-c` work on every platform. `tlang build` and
`tlang run` need **Linux** with a C compiler (gcc, clang or tcc), plus libpq
from PostgreSQL 17 for programs that use the database. The
[`tlang-dev` container](docker/README.md) has all of them.

On macOS a downloaded binary may be quarantined; clear it with
`xattr -d com.apple.quarantine tlang`.

### The editor extension (VS Code and Kiro)

Download the `.vsix` for your platform. The language server is inside it, so
you don't need to install `tlang-lsp` separately.

- **VS Code:** Extensions view → `...` menu → **Install from VSIX...**, or
  `code --install-extension tlang-<v>-<target>.vsix`.
- **Kiro:** Extensions view → `...` menu → **Install from VSIX...** and pick
  the file.

The `tlang.lsp.path` setting still overrides the bundled server if you want
to run a different build. The standalone `tlang-lsp` archives are for other
editors or for that override.
