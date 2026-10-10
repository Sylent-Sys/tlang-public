# Handover: Standard Library Phase 2

## Task

Execute Phase 2 of `.kilo/plans/1791435552742-standard-library-development-plan.md`, covering project manifests, whole-program features, stable errors, grant handoff, environment access, and structured logging. The user approved a concrete v1 contract during this session:

- Strict versioned `tlang.json` (`schemaVersion: 1`, `language: "1"`), project-relative entry, targets, capability requests, named databases, and limits. Reject unknown/duplicate fields and redact document data from errors.
- Separate versioned grants JSON bound to the SHA-256 of the exact manifest bytes. Secrets such as database URLs live in grants, not the manifest or generated C. Runtime validates before side effects.
- Entry-rooted deterministic program feature summary; `UsesDB` remains a compatibility projection.
- Error keeps `status`/`message` and appends stable `category`/`code`.
- `env.get(name): string | null` is exact-name and fallible; an unauthorized name is a permission error, while an authorized unset name returns null.
- JSONL console logging to stdout for all levels (`debug/info/warn/error`), UTC RFC3339 timestamps, nested fields that cannot override envelope keys, valid escaping of invalid UTF-8 bytes, and whole-record 1 MiB cap/drop behavior.
- Minimal `JsonValue` constructors/accessors are included in Phase 2 to support nested structured log fields.

## Current Progress

Phase 2 work was performed on top of `main` at `660d591` (initial worktree was clean). No progress commit has been made yet.

Implemented or present in the current worktree:

- New `project/` package parses and validates strict manifest/grant JSON, rejects duplicate/unknown fields and invalid values, provides upward discovery and manifest digest binding, and includes unit tests.
- CLI and LSP use manifest discovery for project roots; CLI selects the manifest entry for a project directory and checks explicit file arguments. CLI `run` validates and forwards grants; build does not bake grants into output.
- Checker/types include deterministic entry-rooted feature analysis, generic database/runtime/native feature reporting, and reachable `UsesDB` projection.
- Error ABI adds `category`/`code` across language types, code generation and runtime throw/catch behavior, with stable runtime codes for several existing errors.
- Generated program metadata carries non-secret manifest bytes/digest; runtime has a strict bounded grants parser (`runtime/src/grants.c`) and startup validation before scheduler/listener/global/user startup. Grant validation is covered by runtime tests.
- `env`/`env.get` is registered under `tlang/system`, lowered to `tlang_env_get`; runtime exact-name grant checks, permission errors and authorized-unset behavior are present in `runtime/src/env.c` and tests.
- Structured console and minimal generic JSON implementation are now also present in the worktree: appended builtin IDs, type/checker/codegen paths, runtime JSON value and console implementations, migrated some source fixtures, and added C tests. This work was interrupted before successful end-to-end verification and is not yet complete.
- `docs/DESIGN-modules.md`, `docs/DESIGN.md`, and `docs/RUNTIME.md` were updated to describe the manifest/grants and API contracts, but their implementation-status wording may need refresh after validating the latest logging/env work.

## Verification So Far

Passed:

- `go build ./...`
- `go vet ./...`
- `gofmt -l .` returned no files; `git diff --check` passed.
- `scripts/dev.ps1 "make -C runtime test PG=0"` passed all non-PostgreSQL runtime tests, including new grants, environment, structured console, Error ABI, and startup tests. PostgreSQL runtime tests were intentionally skipped under `PG=0`.

Not passing:

- `go test ./...` fails and must be repaired before claiming Phase 2 complete.
- `cmd/tlang`: `TestCheckManifestDirectorySelectsEntryAndUsesProjectRoot` does not find the manifest digest in generated C metadata.
- `cmd/tlang-lsp`: `TestProbeListDriftGuard` still expects legacy `console.error` membership.
- `codegen`: `TestHandlerNesting` still type-checks a legacy `console.error(...)` call; it must migrate to the structured signature.
- `tests`: source fixtures and goldens are only partially migrated. Legacy `console.error` calls fail checking; generated C differs across many goldens due Error ABI, metadata, slices/JsonValue, and structured logging changes. Do not blindly regenerate before resolving the type/lowering discrepancies and updating fixtures.

## Immediate Next Steps

1. Inspect the currently staged/unstaged diff and finish the partially interrupted JSON/structured console implementation. The previous logging subagent was cancelled at the user's request; do not resume it without explicit user direction.
2. Fix structured console namespace/member lookup (`console.error` must resolve to the new structured signature) and migrate remaining source fixtures/tests such as `codegen/handlers_test.go` and the LSP completion drift expectation.
3. Fix manifest metadata propagation/code generation so `TestCheckManifestDirectorySelectsEntryAndUsesProjectRoot` sees the digest, while ensuring generated C contains no grant data/secrets.
4. Audit JsonValue type layout, slice/mangling support, constructors/accessors, code generation, record escaping, and UTF-8 handling. Runtime C tests passing alone does not establish source-to-runtime correctness.
5. Once checker/codegen issues are fixed, regenerate intended golden C outputs with the repository's documented mechanism (`go test ./tests -run 'TestGolden|TestModulesGolden' -update`), inspect the full golden diff, and run `go test ./...` to completion.
6. Re-run `go build ./...`, `go vet ./...`, `gofmt -l .`, `git diff --check`, and runtime C tests. Run PostgreSQL-enabled/runtime/e2e legs if the dev environment supports them.
7. Review the docs' implementation-status claims against verified behavior, update this handover if needed, then commit and push the progress branch requested by the user.

## Worktree Scope

The current worktree contains roughly 64 modified tracked files plus new files under `checker/`, `cmd/tlang/`, `cmd/tlang-lsp/`, `project/`, `runtime/src/`, `runtime/tests/`, and `types/`. All listed modifications stem from this Phase 2 task; none were present at task start. Preserve the uncommitted progress and do not revert it while addressing the failures.
