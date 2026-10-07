// Package driver turns a type-checked TLang program into the plan and command
// lines needed to build a native executable. It discovers the host C toolchain
// and libpq, extracts the embedded C runtime into a content-addressed cache,
// assembles the full compiler flag set that builds the generated program with
// the runtime, enforces the compiler-matching (ABI) rule, and enforces the
// "exactly one entry" gate the checker deliberately omits.
//
// The package splits into two layers:
//
//   - Pure / injectable layer (fully unit-tested): toolchain discovery
//     (Discover), libpq discovery (DiscoverPQ), flag assembly (AssembleFlags),
//     build-plan assembly (AssemblePlan), compiler matching, and the entry gate
//     (CheckEntry). Each is a pure function of its inputs or depends only on
//     injected seams, so tests are hermetic: they pass on a host with no C
//     compiler, no libpq, and no runtime .c files.
//   - Execution layer (structured, not exercised by tests): BuildPlan.Run and
//     RuntimeCache.Extract assemble correct, inspectable argv / *exec.Cmd
//     values but are not driven against a real compiler. The C runtime's .c
//     files do not exist yet, so a full run/build end-to-end cannot execute and
//     is not a requirement.
package driver

import "errors"

// ErrNoEntry is returned when a program has no entry point (Kind ==
// ProgramUnknown with Entry == nil): a library fragment the checker accepts but
// the driver cannot build into an executable. HANDOVER gap #2.
var ErrNoEntry = errors.New("program has no entry point: define exactly one of fn main(): void or fn route_dispatcher(ctx: Context): void")

// ErrNoCompiler is returned when a build is requested but no suitable C
// compiler was discovered on PATH.
var ErrNoCompiler = errors.New("no C compiler found on PATH (looked for gcc, clang, tcc)")

// ErrNoArchiver is returned by AssemblePlan when the gcc/clang archive branch
// is selected (a non-LTO build with runtime sources to archive) but the
// archiver "ar" cannot be resolved on PATH. The archive step is the only step
// that uses "ar"; the LTO single-invocation and tcc -run branches never return
// this. It is resolved through the same LookPath seam as the compilers, so
// tests drive both present and absent cases hermetically.
var ErrNoArchiver = errors.New("no 'ar' archiver found on PATH (required for the gcc/clang archive build)")
