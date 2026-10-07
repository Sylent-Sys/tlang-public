# TLang project rules

`HANDOVER.md` in the workspace root is the source of truth for project state
and working method. Read it before starting work. The user switches between
Claude Code and Kiro (which reads `.kiro/steering/tlang.md`, the same rules),
and `HANDOVER.md` is the only state the two share.

- **Orchestrate, don't hand-code stages.** Delegate substantial work to a
  workflow or sub-agent with a self-contained brief; verify and commit the
  result yourself. Details: `HANDOVER.md` → "How work gets done here".
- **Locked code.** Every Go package (`token/ diag/ ast/ types/ lexer/ parser/
  checker/ codegen/ driver/ module/ cmd/tlang/ cmd/tlang-lsp/`), the runtime
  headers, `runtime/embed.go`, `runtime/Makefile`, and `runtime/src/*.c` change
  only when the task is explicitly a change to them. A real bug in locked code:
  STOP and raise it first.
- **Git.** Branch off `main`; one squashed descriptive commit per coherent
  group. Never push, force-push, amend pushed commits, or change git config
  unless the user asks. `.agents/` is gitignored — keep it out of commits.
- **Verify before claiming done.** `go build ./...`, `go vet ./...`,
  `go test ./...`, `gofmt -l .` clean; C/e2e/DB legs in the `tlang-dev`
  container (`docker/README.md`, or `scripts/dev.ps1 <cmd>` from Windows).
- **Start and finish.** At the start, compare `git log` with `HANDOVER.md` —
  parallel sessions can land work it doesn't mention. At the end of any piece
  of work, update `HANDOVER.md`.
- **Licence:** MPL-2.0 (`LICENSE`), except `runtime/`, which is Apache-2.0
  WITH LLVM-exception (`runtime/LICENSE`). Generated C belongs to the user.
  Changing it is the user's call.
- **Releases:** tag `vX.Y.Z` per `RELEASING.md`; CI is `.github/workflows/`.
