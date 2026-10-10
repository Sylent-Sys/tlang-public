# TLang Modules — Implemented Design and Approved Standard-Module Contracts

> **Status**
>
> - Local-file modules (§§1–11): implemented design of record.
> - Compilation model: one generated C translation unit.
> - Standard modules (§12): Phase 2 contracts approved; implementation status is
>   recorded in §12.6.
> - §0 records the historical pre-module baseline.
>
> Any divergence between §§1–11 and shipped behavior is a documentation or
> implementation defect and should be resolved explicitly.

This document records the implemented design for the local module system
(`import` / `export`) and the approved design contracts for compiler-provided
standard modules, so a program can span many files instead of living in one
"god file." Unimplemented standard-module behavior is labeled as such. It is
deliberately written to the same bar as
`docs/DESIGN.md`: every claim about the existing compiler is checked against the
current source, and every open choice is named with a recommendation and a
reason.

Section references like §5.3 point into the spec
(`TLang_Technical_Specification_1.2.1.md`); DESIGN §x points into
`docs/DESIGN.md`.

---

## 0. Motivation and the current state

### 0.1 Historical baseline before modules

TLang v1 is **single-file**. There is no `import`, `export`, `module`,
`package`, or `namespace` — not in the spec, the lexer, the token set, or the
parser. Concretely (verified against source):

- `parser.ParseSource(file, src)` lexes+parses exactly one file into one
  `*ast.Program`. `ast.Program` is `{ File string; Statements []Statement; EOF }`
  — a flat list whose only legal top-level forms are `fn`, `interface`, `type`,
  and `let`/`const` (`parser.parseTopDecl`).
- `checker.Check(prog *ast.Program) (*types.Info, *diag.List)` type-checks that
  one program in six passes; all top-level names share one flat namespace.
- `codegen.Emit(prog, info)` produces **one C11 translation unit**.
- `cmd/tlang` reads **one** path (`os.ReadFile(path)`), and the driver assembles
  a build plan for that single translation unit linked against the runtime.
- Name mangling (`types.StructCName` = `tl_` + `Mangle`, `FuncCName`,
  `MethodCName`) is injective **within one file**, guaranteed by
  `types.CheckDeclName` (historically hardened in PR #8 to reserve `tl_`/`globals`/
  `_init_globals` and reject cross-declaration C-name collisions).

So a real program must put every interface, function, and route in one file.
That is fine for the spec's §13 example and does not scale.

### 0.2 Goals

1. **Multi-file programs** with a real namespace per file, so names in one file
   do not collide with names in another unless deliberately shared.
2. **Explicit, readable sharing** — a declaration is private to its file unless
   exported; another file names it through an `import`.
3. **TypeScript-flavored surface**, consistent with the language's look (`fn`,
   `interface`, `let`/`const`, decorators). ES-style `import`/`export` fits.
4. **No change to the runtime ABI or the single-translation-unit codegen model**
   if we can avoid it. The runtime (`runtime/`) is frozen and reviewed; a module
   system should be a *front-end + driver* feature, not a runtime feature.
5. **Determinism preserved** — the whole project enforces byte-identical codegen
   (the determinism suite). Module order must not leak into output.

### 0.3 Non-goals (for this first cut)

- Separate compilation to multiple `.o` files, a stable cross-module binary ABI,
  or incremental/partial rebuilds. (Discussed in §6 as a possible later step.)
- A package registry, versioning, or remote/third-party imports. v1+ modules are
  **local files in the project tree only**.
- Export-star re-exports, side-effect-only imports, anonymous default exports,
  and circular *value* initialization semantics beyond what §4.4 defines.
  Named aliases, namespace imports, default imports, named/default declaration
  exports, and named re-exports are implemented.
- Visibility finer than file-level (no package-private tiers, no submodule trees).

---

## 1. The core decision: compilation model

This is the decision everything else hangs on. Two options.

### Option A — merge to a single translation unit (RECOMMENDED)

The compiler resolves the import graph, lexes+parses every reachable file, type-
checks them **together** as one logical program with per-file namespaces, and
`codegen` emits **one C11 translation unit** containing every reachable
declaration (module-qualified so names stay unique). The driver and runtime are
unchanged: still one `.c`, still one link against the runtime.

- **Pros:** the runtime ABI, the driver's build plan, and the "one TU" codegen
  model are all untouched. No separate-compilation linking story, no cross-TU
  ABI to stabilize. Dead-code elimination is trivial (only reachable modules are
  emitted). This is conceptually how the Go front-end treats a package: many
  files, one compiled unit.
- **Cons:** no incremental build (any change recompiles the whole program) — but
  the compiler is fast and this matches today's behavior. The one emitted `.c`
  grows with the program.

### Option B — separate compilation (one `.o` per module)

Each module compiles to its own C translation unit and object file; the driver
links them. Requires a **stable cross-module C ABI**: exported functions need
stable, collision-free external C names and prototypes shared via generated
headers; the mangling scheme becomes an ABI contract.

- **Pros:** incremental rebuilds; smaller individual TUs.
- **Cons:** large change to driver (per-module compile + link graph), codegen
  (emit a header per module, import others' headers), and the mangling scheme
  (now an ABI). Interacts badly with monomorphized generics (a `Page<User>`
  instantiated in two modules must be emitted once — needs a shared-instance
  owner, i.e. a link-time dedup or a designated owning module). Much more risk
  against the frozen runtime/driver contracts.

### 1.3 Real-world impact of the compilation model

The choice is not mainly "which is less work" — it is "what does each do to
someone actually building and running TLang programs." Three facts about TLang
specifically shape the answer: dev mode uses **TCC**, whose entire selling point
is near-instant compiles (the spec targets ~15 ms); release mode uses **clang
`-O3 -flto`**, i.e. whole-program optimization by design; and the hand-written
runtime (~5,200 lines of C) is compiled regardless. Scenario by scenario:

| Real-life dimension | Winner | Why it matters for TLang |
| :--- | :--- | :--- |
| Edit-compile-run loop (dev, TCC) | **B** | B recompiles only the changed module (`O(changed)`); A recompiles the whole program + runtime (`O(program)`). But TCC is so fast that A is effectively instant for small/medium programs; B only pulls ahead once a program is *large* (many dozens of files). **This is the only case B wins.** |
| Release build (`-O3 -flto`) | **A** | A hands the optimizer the entire program in one TU — cross-"module" inlining, DCE, devirtualization are free. B sees modules in isolation; to recover that it needs LTO, which re-merges everything at link time anyway, erasing B's compile-time edge for release. TLang's release mode is *already* whole-program LTO, so A is the natural input. |
| Generics / monomorphization | **A** | A emits a shared instance (`Page<User>` used in 5 modules) exactly once via whole-program dedup. B must pick an owning module or dedup at link time, or risk duplicate symbols / bloat — real bugs with no runtime upside. |
| Binary size / tree-shaking | **A** | A emits only *reachable* modules (unimported files simply aren't in the TU). B tends to link every compiled `.o` unless `--gc-sections`-style DCE is tuned. |
| Runtime behavior / correctness | tie | Modules are a compile-time concept; the generated code and the running binary are identical either way. |
| Debug stack-trace readability | B (slight) | B's per-module generated files map a little more naturally to the developer's mental model; minor, since generated C is not read like source. |
| Build-system robustness | **A** | A keeps the driver as-is (one `.c`, one compile, one link — the toolchain we hardened across PRs #1–#8 does not move). B grows a build graph: per-module compile, stale-object detection, a link step, generated per-module headers, and a cross-module C ABI that becomes a *stability contract*. More machinery = more build-bug surface (the stale-`.o` class every C project eventually hits). |

**Bottom line for TLang's actual use case.** TLang is pitched for focused,
high-performance HTTP services (fibers, epoll, p99 ≤ 2.5 ms, deploy-as-one-
binary). That profile is small-to-medium programs where **fast release builds,
lean binaries, and a bulletproof toolchain** matter more than incremental dev
rebuilds — because TCC already makes the dev loop fast and the programs are not
500-file monoliths. That is squarely Option A's sweet spot. The *only* future in
which A becomes a drag is a large monorepo-style codebase where "recompile
everything" is slow even under TCC — and the right response to that is not "do B
now," but "ship A now and add separate compilation later if usage ever demands
it" (the specifier grammar and §6 already reserve that path, so B remains a
non-breaking future step).

### Recommendation

**Option A (merge to one TU).** It delivers real modularity — multi-file
programs with per-file namespaces and explicit exports — at a fraction of the
risk, wins every real-life dimension that matches TLang's use case (release
optimization, generics, binary size, toolchain robustness), and leaves the
runtime and the single-TU codegen model (both frozen, reviewed) completely
untouched. The one dimension B wins — incremental dev rebuilds on a large
codebase — is blunted by TCC's speed and only bites at a scale TLang is not yet
aimed at. Separate compilation stays a documented *later* step (§6) for exactly
that scenario. The rest of this document assumes Option A.

---

## 2. Surface syntax

TypeScript-flavored, path-based, explicit.

### 2.1 Exporting

A top-level declaration is **private to its file** unless prefixed with
`export`:

```ts
// models/user.ts
export interface User { id: int64; name: string; }
export fn (u: User) displayName(): string { return u.name; }

interface Internal { secret: string; }      // private: not importable
```

`export` is allowed on exactly the four top-level forms the grammar already
has: `fn` (including receiver functions / methods and generics), `interface`,
`type`, and `let`/`const`. A method's visibility follows its receiver type's
module; `export fn (u: User) ...` exports the method (callable on a `User`
wherever `User` is in scope). Decorated functions may be exported:
`export @Use(g) fn (ctx: Context) handler(): void`.

### 2.2 Importing

```ts
// api/routes.ts
import { User, displayName } from "./models/user";
import DefaultThing, { helper as localHelper } from "./other";
import * as strings from "../util/strings";
import { greet } from "../util/strings";

fn route_dispatcher(ctx: Context): void { ... }   // uses User, displayName
```

- The specifier is a **relative path** (`"./x"`, `"../y/z"`) resolved against the
  importing file's directory, with the extension optional (`.ts`/`.tlang`;
  resolution order defined in §3.2). Absolute paths and bare specifiers
  (`"user"`, `"@scope/pkg"`) are **rejected** in v1+ (reserved for a future
  package system, §9).
- Named imports name specific exported symbols and may use `as` aliases. A
  default import binds the target's default export and may combine with a named
  list. A namespace import binds the target module object and does not combine
  with default/named imports. Each imported name is bound
  in the importing file's namespace and must resolve to an `export`ed
  declaration in the target file (else `E-IMPORT`).
- Importing a name that exists but is not exported is `E-IMPORT` ("X is not
  exported by ./models/user"), distinct from "no such name."

### 2.3 Lexical / grammar additions

New tokens: `IMPORT`, `EXPORT`, `FROM` (keywords). `import`/`export`/`from`
join the keyword table. (The string specifier reuses the existing string-literal
token; `{` `}` `,` already exist.)

Implemented grammar (extending DESIGN §2.2):

```
program    := (importDecl | reExportDecl)* topDecl*
importDecl := 'import' (IDENT (',' namedImports)? | namedImports | '*' 'as' IDENT)
              'from' STRING ';'
namedImports := '{' importName (',' importName)* ','? '}'
importName := IDENT ('as' IDENT)?
reExportDecl := 'export' namedImports 'from' STRING ';'
topDecl    := ('export' 'default'?)? decorator* fnDecl
            | ('export' 'default'?)? interfaceDecl
            | ('export' 'default'?)? typeDecl
            | ('export' 'default'?)? letDecl
            | ('export' 'default'?)? constDecl
```

Rules the parser enforces locally (no cross-file knowledge needed): all
`import` declarations come first, before any `topDecl` (TypeScript allows
interleaving, but requiring imports first keeps the grammar and reader simple;
this can be relaxed later with no breaking change). A duplicate imported name in
one file, or an empty `{}`, is a parse-level `E-IMPORT`/`E-PARSE`.

The implemented syntax does not include side-effect-only imports, export-star
re-exports, or anonymous default exports.

### 2.4 The entry rule across modules

Today "exactly one of `main` / `route_dispatcher`" is enforced over one file
(the checker treats a program with neither as a library fragment;
`driver.CheckEntry` is the gate). With modules, the rule becomes **exactly one
entry across the whole reachable program**: exactly one `main` **or** one
`route_dispatcher`, in exactly one module. The **root module** (the file passed
to `tlang build`/`run`) is where the entry is expected; an entry defined in a
non-root module is allowed but the "exactly one" count is global. Zero entries
across the program, or more than one, is the existing `ErrNoEntry` /
entry-conflict error, now computed over the merged program.

---

## 3. Module resolution

### 3.1 What is a module

**One file = one module.** Its identity is its normalized, project-root-relative path
(e.g. `models/user.ts`). There is no `package` clause and no directory grouping
in v1+ (that is a Go-style alternative considered and rejected in §8).

### 3.2 Specifier resolution

Given an importing file at `dir/` and a specifier `"./models/user"`:

1. Resolve the specifier relative to `dir`, producing a candidate path.
2. An explicit `.ts` or `.tlang` extension is used as written. A different
   explicit extension is rejected. Without an extension, try `<path>.ts` then `<path>.tlang`
   (the two accepted source extensions, DESIGN §1); if both exist it is an
   `E-IMPORT` ambiguity error (deterministic, not a silent pick).
3. Normalize to a project-root-relative, slash-separated path for the module identity so
   two specifiers that reach the same file (`./a` from one dir, `../x/a` from
   another) are the **same module**, loaded once.
4. The resolved path must stay lexically **within the project root** (the
   directory of the root module, or a configured source root). Escaping it
   (`../../etc/...`) is an `E-IMPORT` error. Absolute paths are rejected. The
   current resolver follows filesystem symlinks and is not a sandbox against a
   symlink inside the source tree that targets outside the root; builds must not
   treat an untrusted project tree as a security boundary.
5. A missing file is `E-IMPORT` with the specifier and the resolved path (never
   echoing anything outside the project).

### 3.3 The dependency graph

The front-end builds a module graph by transitive closure from the root module:

- Start at the root file, parse it, collect its `import` specifiers, resolve and
  enqueue each target, repeat until closure.
- Each module is parsed **once** and memoized by its normalized identity.
- **Cycles:** a cycle in the *import graph* is allowed for types and functions
  (mutually-recursive interfaces/functions across files are legitimate, exactly
  as they are within one file today). A cycle is only a problem for **global
  initializer ordering** — see §4.4. Pure type/function cycles get no error.
- Processing order for determinism: the graph is walked in a **fixed,
  dependency-first order** with normalized module identity as the tie-break.
  When an import cycle remains, its members are emitted in deterministic module
  identity order. Thus the order is total and byte-identical regardless of
  filesystem iteration order.

---

## 4. Type checking across modules

The checker is the package that changes the most. The guiding principle: **keep
the six-pass structure; add a resolution layer in front of it that makes
imported names visible, and qualify every declaration with its owning module.**

### 4.1 Representation choices

Two sub-options for how multi-file reaches the checker:

- **(a) One merged `*ast.Program` with per-declaration module tags.** The
  front-end concatenates all modules' statements into one program (in the §3.3
  fixed order), tagging each top-level declaration with its module identity, and
  the checker gains a per-module scope layered under the existing global scope.
- **(b) A new `checker.CheckProgram(modules []*ast.Module) (*types.Info, …)`**
  entry that takes the parsed modules and their import tables explicitly, keeping
  `checker.Check(prog)` as the single-file path.

**Recommendation: (b)** — a new multi-module entry, with the existing
`checker.Check` preserved (single-file stays a special case: one module, no
imports). This keeps `types.Info`'s *shape* stable (codegen still consumes the
same `Info`), adds the module concept explicitly rather than smuggling it through
a flat `Program`, and avoids perturbing the single-file path that the whole test
corpus exercises.

### 4.2 Visibility and name resolution

- **Pass 0 (new) — module resolution.** Build the graph (§3.3). For each module,
  build its *import table*: imported name → (target module, exported symbol). An
  import of a non-exported or non-existent symbol is `E-IMPORT` here, before any
  type work.
- **Pass 1 (collect names) becomes per-module.** Each module gets its own
  top-level scope containing its own declarations **plus** its imported names
  (as references into other modules' symbols). A name collision *within* a module
  between a local declaration and an import is `E-NAME`. Two modules may both
  declare `User` with no conflict — they are different symbols in different
  scopes.
- **Passes 2-6 run over the whole program** (resolve signatures, check bodies,
  escape, may-fail, select-entry) using module-qualified symbol identity, so a
  function in module A that calls an imported function in module B resolves
  across the boundary. The may-fail fixed point and the escape analysis already
  operate over the call graph; they extend naturally once the call graph spans
  modules.
- **`selectEntry` (pass 6) is now whole-program** (§2.4): exactly one entry
  across all modules.

### 4.3 Nominal typing across modules

TLang is nominally typed (DESIGN §2.3): two interfaces with identical fields are
different types. With modules this is unchanged and important: `User` imported
from `models/user` is one nominal type everywhere it is imported — the *same*
symbol, not a structural copy. A different `User` declared in another module is a
*different* type and cannot be passed where the first is expected (no structural
compatibility). This falls out of symbol identity being module-qualified.

### 4.4 Global initialization order (the one real semantic subtlety)

Top-level `let`/`const` run their initializers once per scheduler at startup.
Across modules their baseline order is the graph's deterministic dependency-first
order (§3.3), source order within each module. For import cycles, module identity
breaks the cycle deterministically; a cyclic import graph therefore still has a
total initialization order.

- The checker separately builds a dependency graph from **direct global reads**
  appearing in initializer expressions and rejects every cycle as `E-INIT`.
  Called function bodies are not traversed by the current analysis, although a
  function invoked by an initializer executes immediately. Consequently an
  indirect read/cycle through a called function is not statically diagnosed and
  observes the established initialization order. This is a documented analysis
  limitation, not a claim that the function runs later; production hardening
  should either add interprocedural initializer dependencies or reject calls
  whose global-read effects cannot be proven safe.
- A **cycle in global-initializer dependencies** (module A's global reads module
  B's global and vice versa, where the import graph is cyclic *through globals*)
  is an `E-INIT` error — the same spirit as the existing non-optional-field-cycle
  rejection. Pure type/function import cycles are fine (§3.3); only a cyclic
  *global-value* dependency is rejected.
- The generated `tl__init_globals` is a sequence of per-module init blocks in
  that total order (still one function, still per-scheduler).

### 4.5 Diagnostics

New error family **`E-IMPORT`** (unresolved specifier, path escapes root,
ambiguous extension, importing a non-exported or non-existent symbol, duplicate
import name). Reuse `E-NAME` for a local-vs-import collision, and `E-INIT` for a
cyclic global-init dependency. All carry the importing file's position (the
`import` line) so errors point at real source.

---

## 5. Code generation and name mangling

### 5.1 Module-qualified mangling (the key codegen change)

Today `StructCName(n) = "tl_" + Mangle(n)` and `FuncCName`/`MethodCName`
guarantee uniqueness within one file. With two modules each declaring `User`,
both want `tl_User` — a collision. The mangling scheme must gain a **module
qualifier**:

- Give every module a stable **module tag** derived deterministically from its
  normalized path. The current implementation uses a sanitized path plus a
  32-bit FNV-1a suffix; this is deterministic and compact, not mathematically
  injective. The current implementation does not yet verify hash collisions;
  this is a conformance gap. Before code generation, the frontend must detect
  duplicate module tags or generated C names across the complete graph and
  reject them deterministically. Code generation must never silently emit
  colliding names.
- Qualify user-declared C names with the module tag: `tl_<mod>__User`,
  `tl_f_<mod>__greet`, `tl_m_<mod>__User__displayName`. The existing
  `tl_`/`tl_f_`/`tl_m_` family and the reserved `globals`/`_init_globals` shapes
  are preserved; the module tag slots in as an additional, already-reserved
  segment.
- **`CheckDeclName` / collision checking (historical PR #8) extends, not changes:** within a
  module the same reservations apply; across modules the module tag keeps names
  injective by construction, so the cross-declaration collision pass now runs
  per module; graph-wide collision verification establishes global uniqueness.
  This is an *implementation*
  change to the (currently locked) mangling in `types/`, done as a deliberate,
  reviewed opening of that package — exactly as the pg.c and checker fixes were.
- **Monomorphized generics** (`Page<User>`): a generic instantiated in multiple
  modules must be emitted **once** with one mangled name. Under Option A (single
  TU) this is a whole-program dedup keyed on the instantiation's canonical
  identity (the type args, which are themselves module-qualified) — the existing
  `NamedInstances()` dedup extends to the whole program. (Under Option B this
  would need a designated owning module; another reason Option A is simpler.)

### 5.2 Emission

Under Option A, codegen walks the merged, dependency-ordered program and emits
one TU exactly as today: typedefs (topologically ordered across all modules),
structs, the single `tl_globals` (now the union of every module's globals,
tagged), per-module `tl__init_globals` blocks in dependency order, JSON
functions, user functions, and the entry. Determinism is preserved by the fixed
module order (§3.3) feeding a stable declaration order.

### 5.3 No runtime change

The runtime (`runtime/`) is **untouched**. Modules are a pure front-end + driver
feature: the emitted C still `#include "tlang.h"`, still links the same way, and
the `tlang_program`/`tlang_main` entry is unchanged. This is the single biggest
reason to prefer Option A.

---

## 6. Driver and CLI

- **CLI:** `tlang check|emit-c|run|build <root-file>` still takes **one** path —
  the *root* module. Multi-file is transparent: the compiler discovers the rest
  through imports. (A future `--source-root <dir>` flag could bound resolution;
  default is the root file's directory tree.)
- **Driver:** unchanged under Option A — it still assembles a build plan for one
  emitted `.c` linked against the runtime. The front-end does the multi-file
  work before codegen; the driver never sees modules.
- **`emit-c`** prints the single merged TU (useful for inspection and golden
  tests).
- **Separate compilation (Option B) would** change the driver substantially
  (per-module compile + link graph, generated per-module headers). Explicitly out
  of scope here; revisit only if build time on very large programs demands it.

---

## 7. Implemented testing strategy

Mirrors the existing suites (`docs/DESIGN.md` §5, `tests/`):

- **Golden:** multi-file fixtures (a directory per case: a root file + imported
  files) → one `.c.golden`. Exercise per-module namespaces, cross-module calls,
  nominal-type identity across modules, module-qualified mangling, and generic
  instances shared across modules.
- **Reject:** `E-IMPORT` cases (missing file, non-exported symbol, path escaping
  root, ambiguous extension, duplicate import, bare/absolute specifier), the
  local-vs-import `E-NAME` collision, the whole-program entry conflict, and the
  cyclic-global-init `E-INIT`.
- **Determinism:** the same multi-file program must emit byte-identical C across
  runs regardless of filesystem iteration order — a direct test of §3.3's fixed
  ordering.
- **ccompile / e2e:** a multi-file server fixture must object-compile on
  gcc/clang/tcc and run end-to-end exactly as the single-file §13 server does.
- **Fuzz:** extend the parser/checker fuzz with `import`/`export` shapes and
  malformed specifiers (no crash/hang).

---

## 8. Alternatives considered

- **Go-style packages (directory = package, capitalization = visibility).**
  Rejected for v1+: TLang is TypeScript-flavored, so capitalization-based export
  would clash with the language's look, and directory-as-unit is a bigger
  conceptual change than file-as-module. ES-style `import`/`export` is the
  natural fit.
- **Implicit exports (everything top-level is importable).** Rejected: it
  removes the privacy that motivates modules and makes every rename a potential
  cross-file break. Explicit `export` is the point.
- **Separate compilation first (Option B).** Rejected as the first cut for the
  risk/benefit reasons in §1 — it reopens the frozen runtime/driver ABI story
  and complicates generics. Kept as a documented future option.
- **A single flat `*ast.Program` with module tags (§4.1a).** Rejected in favor
  of an explicit multi-module checker entry (§4.1b) to keep `checker.Check` and
  the single-file test corpus undisturbed.

---

## 9. Future extensions (explicitly out of this first cut)

- Export-star re-exports (`export * from "./m"`), side-effect-only imports
  (`import "./m"`), and anonymous default exports.
- A package system: bare specifiers (`"strings"`), a project manifest, versioned
  / third-party / remote dependencies, and a resolution algorithm beyond relative
  paths. (The specifier grammar already reserves bare/absolute forms by rejecting
  them, so adding this later is non-breaking.) Compiler-provided standard modules
  using a reserved `"tlang/..."` specifier are proposed separately in §12; they
  are not user packages and do not imply a registry or remote dependency system.
- Separate compilation (Option B) for incremental builds.
- Finer visibility tiers (package-private) if directory grouping is ever added.
- Relaxing "imports must come first" to TypeScript's interleaved form.

---

## 10. Impact summary — which packages this opens

| Package | Change | Risk |
| :--- | :--- | :--- |
| `token/`, `lexer/` | Add `import`/`export`/`from` keywords + tokens | Low (additive) |
| `parser/`, `ast/` | `importDecl` production, `export` flag on top-level decls, a module/import AST shape | Medium (new grammar, error recovery) |
| `checker/` | New multi-module entry (`CheckProgram`), pass-0 resolution + import tables, per-module scopes, whole-program entry/escape/may-fail, cyclic-global-init `E-INIT`, `E-IMPORT` family | **High** (the deepest change) |
| `types/` | Module-qualified mangling; collision checking extends per module | Medium (reopens the PR-#8 mangling contract) |
| front-end driver (new) | Module graph builder: resolve specifiers, parse closure, fixed ordering, feed the checker | Medium (new component) |
| `codegen/` | Walk the merged dependency-ordered program; module-tagged names; per-module init blocks; whole-program generic dedup | Medium |
| `driver/`, `cmd/tlang/` | Essentially unchanged under Option A (still one TU, one root path) | Low |
| `runtime/` | **No change** | None |
| `tests/` | New multi-file golden/reject/determinism/ccompile/e2e/fuzz coverage | Medium |

This was a **heavy, multi-package feature** that deliberately reopened several
packages (`parser`, `checker`, `types` mangling, `codegen`). It was net-new scope
the spec never defined. The local-file module system described above is
implemented; the standard-library proposal in §12 is not.

---

## 11. Recommendation in one paragraph

Add **ES-style `import`/`export`** with **file-as-module**, **explicit per-file
privacy**, **relative-path resolution bounded to the project root**, and
**merge-to-one-translation-unit** codegen (Option A). This delivers real
multi-file modularity and a genuine namespace story while leaving the runtime
ABI and the single-TU codegen model — both frozen and reviewed — untouched. The
cost concentrates in the front end: new grammar, a module-graph builder, a
multi-module checker entry with per-file scopes, and module-qualified mangling.
Separate compilation and a package/registry system are deferred, with the
specifier grammar reserved so they can arrive later without breaking changes.

---

## 12. Compiler-provided standard modules — design direction

**Status: contracts approved; implementation is partial.** This section records
the acceptance requirements for compiler-provided modules. In this section,
**must** denotes a requirement for a conforming implementation, **should**
denotes a preferred choice that requires documented justification to change,
and **may** denotes permission. Statements about current behavior are explicitly
labeled "currently". The local-file module implementation remains unchanged.

### 12.1 Agreed direction

- **Standard modules are explicit.** Capabilities that are currently injected
  implicitly, initially `db` and `console`, should become available only through
  imports. There is no migration warning period: during implementation on the
  development branch, using these names without importing them becomes an
  error. Existing programs and examples must be migrated before release.
- **Initial module roots.** The planned roots are `tlang/db`, `tlang/http`, and
  `tlang/system`. `db` and HTTP APIs remain separate; `system` is the home for
  console, JSON helpers, environment/configuration, UUID, date/time, and
  platform APIs. The approved environment and structured console contracts are
  specified in §12.6; other exports remain staged by the roadmap.
- **Use the implemented import/export forms.** Standard specifiers reuse the
  existing import declaration and binding syntax: named imports and aliases,
  default imports (optionally combined with named imports), namespace imports,
  named re-exports, and named/default declaration exports. This does not enable
  syntax that local modules do not already support. Direct imports use forms
  such as `import { db } from "tlang/db"`; §9 remains authoritative for syntax
  still deferred.
- **Reserve the `"tlang/..."` namespace.** It identifies compiler-provided
  standard modules permanently. Third-party packages must not use this prefix;
  future package specifiers use other names. Standard-module APIs evolve with
  the TLang language version; breaking standard API changes require a language
  version change rather than a versioned import path.
- **Resolve standard specifiers virtually.** Import declarations and bindings
  reuse existing syntax, but resolution does not use local-file semantics. A
  case-sensitive specifier beginning exactly `tlang/` is looked up in a
  compiler-owned virtual-module registry before filesystem resolution. It is
  never relative to the importing file and never probes disk. `tlang`,
  `tlang/`, empty/repeated path segments, `.`/`..`, and unknown module names are
  `E-IMPORT`; other bare specifiers remain rejected until a package resolver is
  designed. Unknown exports from a known virtual module are separately reported
  as `E-IMPORT` at the imported name.
- **Imports are file-scoped.** An import makes its exported names available only
  in the importing module. Other modules must import the names they use.
- **Preserve compiler/runtime lowering initially.** Making a namespace
  importable does not require immediately rewriting its implementation as
  ordinary TLang library code. Existing checker and codegen special cases can
  remain behind the imported standard-module export during an initial phase.
- **Performance is a contract to measure, not a claim of zero-cost abstraction.**
  Standard APIs should preserve TLang's AOT C generation, monomorphization,
  request-arena lifetimes, and fiber scheduler. Prefer generated/direct paths
  where static type information exists; add generic/dynamic APIs where they
  provide real value, with explicit allocation and streaming behavior. Benchmark
  throughput, tail latency, allocations, binary size, and compile time against
  the current lower-level runtime APIs before declaring regressions acceptable.
- **No `Row` marker.** The proposed `Row` export is dropped. The type argument in
  `db.query<T>` / `db.queryOne<T>` remains the concrete user-defined interface
  whose supported scalar fields describe returned columns; no base marker is
  required.

### 12.2 Database engine linkage and configuration direction

Importing `db` controls whether the `db` name is available in that module. The
generated program should avoid linking/configuring a database engine unless an
operation for that engine is reachable from the selected program entry.
Reachability is determined through the interprocedural call graph; an unused
`db` import or an operation only in an unreachable function does not enable an
engine. Re-exports and calls across imported modules participate in the same
whole-program analysis. If reachable operations use more than one engine, link
and configure each used engine independently; configuration for one engine must
not silently substitute for another.

### 12.3 System API contracts

Phase 1 compiler support is limited to the virtual exports `tlang/db` (`db`)
and `tlang/system` (`console`). Those imports are resolved before filesystem
resolution and never create source modules or emitted declarations. `db` and
`console` are no longer implicit universe names. The compiler rejects
`tlang/http` as unavailable until its declarations ship. The initial console
surface is `info` and `error`; `info` preserves the existing line-oriented
stdout runtime behavior, while `console.log` is not source-visible.

Standard APIs are explicit imports, not implicit authority. The project
manifest defines the program's maximum requested authority. Runtime grants
must cover every requested static capability or startup fails before user code;
a grant not requested by the manifest confers no authority. After successful
validation, effective authority is exactly the manifest request, further
constrained on each resource operation by the validated resource grant. Grants are scoped by
resource and grouped into filesystem, process, network, signal, and future
device families. `tlang/system` owns console, JSON, environment, UUID, time,
filesystem, process, network, and lifecycle cancellation APIs. Device-specific
APIs are excluded until their own design; ordinary filesystem APIs reject
device nodes and other special files.

Unless a subsystem says otherwise, standard operations fail through TLang's
existing `Error`/throw mechanism. Each API specification must assign stable,
portable error categories (for example permission, invalid input, not found,
limit, timeout, cancelled, unavailable, and I/O). Native errno/database/backend
details are optional diagnostic metadata, not portable control-flow values.
Retryability must be stated per operation; callers must not infer it from a
platform error number.

#### 12.3.1 Environment and console

- The approved source contract is `env.get(name: string): string | null`.
  Environment access is read-only and lookup-only; enumeration and mutation
  are not provided. Lookup uses the exact name supplied, without prefix matching
  or case folding. The name must be listed in the project manifest and separately
  granted at runtime. An undeclared or ungranted read throws an `Error` with
  category `permission`; an authorized but unset variable returns `null`.
  Permission denial and absence are therefore distinct. Denials carry status
  403, category `permission`, and code `env.permission_denied`.
- The approved console signatures are
  `console.debug/info/warn/error(message: string, fields?: Record<string, JsonValue | null>): void`;
  `Record` here is specification notation for a string-keyed JSON object, not a
  claim that this type constructor is implemented in TLang. Each call emits one
  JSON Lines record with UTC RFC 3339 `timestamp`, `level`, `message`, and a
  nested `fields` object (empty when omitted). Field values are `JsonValue` or
  `null`. The envelope owns `timestamp`, `level`, and `message`; application
  fields stay nested and cannot replace envelope metadata, even when a field has
  one of those names. Logging is best-effort and must not fail an application
  operation; complete records are serialized so scheduler-thread writes do not
  interleave. Invalid UTF-8 bytes in message and field strings are escaped as
  individual `\\u00XX` sequences. Complete records are limited to 1 MiB and an
  oversized record is dropped with a runtime diagnostic.

#### 12.3.2 JSON

- Typed TLang interface binding and emission remain compiler-generated.
- The typed fast path emits direct field reads/writes and static metadata; it
  must not introduce runtime reflection, per-field dynamic dispatch, or a
  mandatory intermediate generic JSON tree. Generic JSON is an opt-in path.
- Generic JSON uses a tagged value model: null, boolean, float64 number,
  string, array, and object. Checked integer accessors report range or
  precision failures; large integers otherwise follow JavaScript Number-style
  precision semantics. Parse/stringify return typed errors.
- Parsing applies runtime-configurable default byte and nesting-depth limits,
  also bounded by hard runtime ceilings. Malformed and over-limit input returns
  a typed error and never aborts the process.
- Parsing/stringifying should support stream-oriented operation where the input
  or output can exceed practical request-arena limits. Convenience whole-value
  APIs are bounded; request-scoped results live in the request arena, and data
  retained beyond that lifetime requires an explicit clone/ownership transition.
  Phase 2.5 ships only the `JsonValue` tagged constructors, kind and scalar
  accessors, array/object extraction, and object lookup needed to construct
  structured console fields; parsing/stringifying remain future work.

#### 12.3.3 Time and date

JavaScript's existing `Date` and ECMAScript date-time string behavior is the
primary compatibility reference. Temporal is not assumed. Java or C# may be
used only for a specific capability absent from JavaScript or where JavaScript
behavior is unsuitable; the selected precedent and reason must be documented.
Compatibility does not require reproducing legacy bugs, implementation-defined
parsing, or unsafe implicit behavior. The language specification must identify
every intentional difference. This is a compatibility policy, not a claim that
TLang's complete temporal API is equivalent to JavaScript `Date`.

The first-release value model is `Instant`, fixed `Duration`, calendar `Period`,
`LocalDate`, `LocalDateTime`, explicit IANA `Zone`, `ZonedDateTime`, and a
distinct process-local `MonotonicInstant`. `Duration` is fixed elapsed
nanoseconds; `Period` is signed calendar components applied in an explicit zone,
and the two are not implicitly convertible. `Instant` uses the Unix epoch and ignores leap
seconds as JavaScript `Date` does, but supports nanosecond precision as an
explicit extension beyond `Date`'s millisecond precision. Civil fields use the
proleptic Gregorian calendar. TLang's supported `Instant` and civil range is
years 0001–9999; values outside it return range errors even though JavaScript
Date supports a much wider range. Wall/monotonic clock APIs use nanosecond
units; actual clock resolution is platform-dependent. Clock-read failure is a
typed error. There is no implicit local timezone; calendar operations require
an explicit zone.

- Wall-clock and monotonic reads, checked instant/duration arithmetic,
  instant-to-zone conversion, local construction/resolution, parsing, and
  formatting are in scope. Setting the system clock, timers, alarms, and
  scheduling APIs are out of scope. `MonotonicInstant` is nondecreasing within
  a process and supports comparison/subtraction to `Duration`; it cannot be
  persisted or compared across processes.
- Duration arithmetic and Instant range overflow return typed errors; values
  never wrap or saturate. Local-to-instant conversion outside the supported
  range returns a range error.
- Local times in a daylight-saving overlap follow JavaScript `Date` compatible
  disambiguation: choose the earlier instant. Local times in a gap move forward
  by the gap duration. This retains JavaScript's behavior while requiring the
  zone explicitly instead of using the host's implicit local zone.
- `Period` values contain signed integer years, months, weeks, days, hours,
  minutes, and seconds; nonzero fields must share one sign and apply in that
  order, with weeks as seven calendar days. Calendar addition uses component
  overflow normalization analogous to JavaScript Date setters (for example,
  adding one month to January 31 advances from February 1 by the original
  day-offset, producing a date in March). Normalize fields in order: construct
  a normalized year/month pair, then add the day/week/hour/minute/second
  components as calendar overflow. Apply years/months first, then weeks/days,
  then local clock fields; resolve the resulting local date-time once so DST
  disambiguation is deterministic. Reject a final result outside the supported
  range; do not silently clamp to month-end. All fields resolve
  as local calendar changes in the explicit zone; fractional elapsed changes
  use Duration.
- IANA zones use host tzdb data, including host-recognized aliases; unknown or
  unavailable zones return typed errors without fallback. The requested zone
  identifier is preserved; Zone equality compares identifiers. Results may
  change after host tzdb updates. `ZonedDateTime` value equality and ordering
  compare only the represented instant, even when zone identifiers differ. A
  separate representation comparison checks both the instant and zone/local
  representation; hashing, if provided, must follow value equality.
- Parsing is explicit by target type: `LocalDate` parses a date-only value;
  `LocalDateTime` parses an offset-free local date-time; `Instant` parses an
  offset-bearing ECMAScript date-time string. This preserves the standardized
  ECMAScript grammar without inheriting `Date.parse`'s historical rule that a
  date-only string means UTC while an offset-free date-time means host-local
  time. TLang requires an explicit `Zone` when converting local values to an
  instant and rejects implementation-defined host-specific formats. Canonical
  `Instant` output follows the UTC extended-year/ISO shape of
  `Date.prototype.toISOString`, extended for exact nanoseconds and restricted
  to TLang's supported year range; it is deterministic, not byte-for-byte
  equivalent for sub-millisecond values.
- Localized formatting follows JavaScript `Intl.DateTimeFormat` conventions:
  callers provide a BCP 47 locale and options; locale identifiers are
  canonicalized; host locale data is used; no implicit process locale or zone
  is used. Unsupported locale/options return typed errors without fallback.
  Calendar/numbering/style options are limited to what host locale services
  support. Locale formatting needs no capability and may vary with host
  locale-data/ICU updates; ISO output remains the serialization format.

#### 12.3.4 UUID

UUIDs are 128-bit values using RFC 9562 network byte order and canonical
hex-and-dash text (case-insensitive input, lowercase output). The API supports
v3, v4, v5, and v7 only; v1/v2/v6 and custom v8 generation are excluded. V3/v5
use RFC 9562 namespace bytes and canonical name octets, with built-in DNS/URL
namespaces and caller-provided namespace UUIDs; v5 is recommended for new
name-based IDs, while v3 remains for compatibility. V4 uses a cryptographically
secure system random source and returns a typed error if unavailable. UUIDv3 is
for interoperability, not a security primitive; it must not be used for
secrets or adversarially chosen names. V7 uses
Unix milliseconds and a process-wide generator state shared by all scheduler
threads. Generation is linearizable: every successful call has a serialization
point in this state, and UUID byte order is strictly increasing in that
serialization order within one process, including same-millisecond
bursts and wall-clock rollback (retain a logical timestamp and advance the
monotonic tail). If the monotonic tail is exhausted, wait for the clock to
advance under a bounded deadline based on the monotonic clock, then return a
typed clock/generator error;
never wrap the tail or emit an out-of-order UUID. Ordering is not guaranteed
across process restarts or hosts. UUID parsing validates length, hex, and
separators; storage parsing accepts any RFC 9562 variant/version, while a
version-specific parser also checks the requested version. Generation APIs
create only supported versions. UUID bytes and text conversions are explicit;
UUID is not a numeric type.

#### 12.3.5 Filesystem

Filesystem APIs use a `Root` capability handle plus relative `Path` values.
Manifest/runtime grants identify allowed roots. Reject absolute paths and
traversal outside the root; follow symlinks only when their resolved targets
remain within the root. Containment checks must be race-safe: resolve and open
relative to an authorized directory handle. On Linux, use `openat2`-style
`RESOLVE_BENEATH`/`RESOLVE_IN_ROOT` and `RESOLVE_NO_MAGICLINKS` constraints
where available; fallback implementations must walk directory handles without
following untrusted symlinks. Do not authorize by string-prefix checks or by
checking a path and opening it later. Expose file and directory basics: read/write/append,
metadata, create/list/remove directories, rename, and open/read/write/close
streams. Whole-file writes use same-filesystem temporary-file-and-rename for
atomic visibility (not power-loss durability); append and streams are explicit
nontransactional operations. Directory entries have name/type/metadata and are
sorted by name. Whole-file operations use runtime-configurable defaults and
hard size ceilings; streams are process-scoped, explicitly closeable, and
cleaned up on process shutdown. Only ordinary files/directories are exposed.
Operations return typed errors for not-found, permission, invalid path, I/O,
and limits. Platform support is specified per feature; unsupported targets
produce clear build diagnostics.

#### 12.3.6 Process, sockets, and lifecycle

- Process APIs accept an allowlisted executable identity and argument vector;
  there is no shell-string execution. Grants may constrain executable, cwd,
  arguments, and environment. Child processes are tracked, explicitly
  awaitable/terminable, and reaped on shutdown. Expose optional stdin/stdout/
  stderr pipes and bounded capture. A timeout requests termination, waits a
  bounded grace period, force-kills if needed, then reaps and reports timeout.
  Child-process I/O and collected output have explicit byte limits; stream
  handles are process-scoped and cleaned up if not closed.
- Portable socket APIs cover TCP streams/listeners and UDP unicast/multicast;
  raw OS descriptors and platform-specific options are not exposed. Handles
  have explicit close and runtime cleanup. Grants distinguish listen/connect
  and constrain address/host, port, protocol, and multicast group. Hostnames
  are resolved and every candidate address is checked against grants; connect
  to the checked/pinned address, preserve TLS hostname verification for HTTPS,
  and reauthorize after DNS refresh or redirect. Runtime default deadlines
  apply, with per-operation overrides bounded by runtime ceilings.
- Lifecycle APIs expose a cancellation event for SIGINT/SIGTERM, not arbitrary
  signal handlers or signal sending. The first signal requests graceful
  cancellation; repeated signals may force termination. Device-specific API
  design remains out of scope.
- HTTP client outbound destinations use manifest requests and runtime grants
  scoped to host/port (and scheme when relevant), including redirect targets.
  Redirects are not followed automatically by default; opt-in policies are
  bounded and re-resolve/re-authorize every hop.

### 12.4 Module and compiler boundaries

- Standard specifiers permanently reserve `tlang/`; third-party package names
  use other specifiers. Breaking standard API changes require a TLang language
  version change, not a versioned import path. Unknown standard modules/exports
  are `E-IMPORT`. Implemented local-module binding/export semantics apply
  unchanged. Syntax not yet supported for local imports (export-star,
  side-effect-only imports, and anonymous default exports) is unavailable for
  standard imports until separately implemented and specified.
- Standard library APIs are declarations/imports at the language surface.
  Keep compiler intrinsics only where special typing, compile-time metadata,
  whole-program analysis, or lowering is required. Initially retain DB query
  typing/row descriptors/transaction lowering, route registration lowering,
  generated typed JSON binding, and capability-use analysis as compiler
  services. Ordinary console/env/UUID/time/JSON operations should be callable
  through imported standard APIs and runtime implementations. Refactor away
  special cases only when equivalent diagnostics and behavior can be preserved.
- DB is a PostgreSQL-and-SQLite ORM with decorated user interfaces, CRUD,
  query building, explicit one-to-one/one-to-many/many-to-many relations,
  joins/includes, raw SQL, callback and explicit transactions, and no schema
  migrations. Raw SQL parameters are always separately bound. Raw query results
  may be typed interfaces or named-column rows of tagged `DbValue`s. The tagged
  value model covers the built-in type families supported by each engine.
  Types absent from an engine (for example, PostgreSQL ranges/enums/composites
  in SQLite) remain engine-specific or require explicit adapters; the API never
  implies that one backend natively supports another backend's type.
  Extension-defined types require adapters.
  Portable query/API semantics are shared where possible, with documented
  engine-specific type/query capabilities. No hidden relation queries are
  issued; includes/joins are explicit.
- HTTP is a framework-style module with server and client APIs. Server routes
  can use builder registration or decorators; decorators lower to the same
  deterministic builder registration model. Middleware is ordered and receives
  request/response context plus a single-use `next()` returning the downstream
  response. Handlers may use immutable Request/Response values or a Context
  convenience adapter. Exactly one response may be produced; double sends or
  return-after-send are typed framework errors. A program has exactly one entry
  mode: configured App entry or `main()` calling `serve()`, never both. Routes
  match explicit methods and raw path templates; captured parameters decode
  once, malformed escapes and encoded slash/backslash are rejected. Request and
  response bodies stream with bounded convenience helpers. Runtime defaults
  and hard ceilings govern body sizes and request/idle/connect/total timeouts;
  app/client configuration may override within ceilings.
  Ordinary request/response values live for the request operation/fiber; data
  retained beyond that lifetime requires explicit cloning. Stream-based paths
  are preferred for large payloads and must propagate backpressure rather than
  silently buffering without bound.
- HTTP client APIs include one-shot requests and reusable configured clients.
  Responses support bounded streaming; redirects are returned to callers by
  default, with opt-in bounded policies that re-check destination grants.
  Client redirects and socket networking share one destination-authorization
  mechanism; a grant alone never authorizes an unchecked resolved address.
- Platform APIs are portable high-level contracts, implemented Linux-first
  with a per-feature target support matrix; unsupported target features fail
  with clear compile/build diagnostics. The capability taxonomy and behaviors
  above are the contract. The executable receives validated capability
  requests and runtime grants through an explicit runtime-config input, not
  ambient undeclared variables. Before binding listeners, spawning children,
  or running `main`, startup verifies that every manifest request is covered.
  Static capability families are checked at startup; resource-scoped access is
  rechecked at use against the validated grants, including path containment and
  resolved network endpoints. Missing, malformed, unsupported, or insufficient
  grants fail closed with a diagnostic and nonzero startup status. The concrete
  manifest/grants syntax and deployment handoff format are defined in §12.6.
- DB operations, HTTP client/server I/O, and streaming system APIs use the
  existing fiber scheduler's suspension/cancellation model; they do not add a
  second futures/task runtime. Synchronous-looking APIs may suspend the current
  fiber but must not block a scheduler thread on network I/O. Cancellation,
  deadlines, bounded queues/buffers, and cleanup on request abort are mandatory
  parts of each asynchronous API contract. CPU-only work remains synchronous
  unless a separately designed worker-pool API is introduced.
- Database access starts from parameterized execution, transactions, pooling,
  prepared statements, and generated row mapping. ORM/query-builder syntax is
  compile-time lowered to those primitives; it must not hide per-record queries,
  reflection, implicit relation loads, or unbounded result materialization.
  Raw parameterized SQL remains available for queries the builder cannot
  express.

#### 12.4.1 HTTP application lifecycle

The initial HTTP implementation lowers a configured `App` to the existing
`tlang_program.dispatcher` server entry: route and middleware configuration
builds the dispatcher before `tlang_main`; the runtime remains responsible for
listeners, schedulers, request fibers, and graceful shutdown. `main()` plus
`serve()` is convenience syntax that lowers to the same server entry; it must
not start a blocking server from the current script-mode `main` callback.
Exactly one mode is emitted: app/server or script. An App value is
configuration, not a runtime server handle.

This requires a deliberate runtime/driver extension: the generated program
descriptor must carry the configured app/dispatcher and server mode; `serve()`
must not recursively invoke `tlang_main`. Preserve the current single-entry
invariant and define startup/shutdown ownership once in the runtime.

#### 12.4.2 Supported target and host-data contract

Linux is the initial guaranteed server/runtime target. Compiler-only operations
remain available on supported host platforms. Each standard API declares its
target support in a feature matrix; unsupported use is diagnosed at build time.
Host tzdb and locale data are explicit reproducibility boundaries: operations
are supported only where required host services are available, unavailable
data returns typed errors, and results may vary across host data versions.
These differences do not weaken deterministic compiler output or deterministic
ISO/UUID serialization.

The above decisions close the six design questions at the contract level. The
approved manifest/grants schemas and the exact environment/logging contracts are
specified in §12.6. Remaining standard API declarations and implementations must
conform to these contracts and do not reopen selected policy without a
language-design revision.

### 12.5 Recommended first implementation boundary

Deliver the approved design in independently testable phases rather than one
release-sized change:

1. **Virtual modules and migration:** implemented for `tlang/db` and
   `tlang/system` (`db`, `env`, and structured console logging);
   `tlang/http` remains reserved but unavailable.
2. **Capability foundation:** the version-1 manifest/grants schemas and
   validation contract are specified in §12.6; strict Go parsing, manifest
   discovery/CLI integration, generic feature analysis, and the Error category/
   code ABI exist. Runtime grant loading/enforcement and startup validation,
   `env.get`, and structured logging remain unimplemented.
3. **Generated/common data APIs:** UUID, time, generic JSON, bounded streaming,
   and explicit lifetime tests/benchmarks.
4. **HTTP:** App/dispatcher lowering, server framework, client, destination
   grants, cancellation/backpressure, and lifecycle integration.
5. **Privileged system APIs:** rooted filesystem, process, sockets, and
   lifecycle cancellation after capability enforcement is proven.
6. **Database expansion (separate DB API specification):** per-engine
   reachability/linkage, SQLite, compile-time query builder/ORM lowering,
   relations, adapters, and dynamic values. Do not couple this phase to the
   initial import migration.

Each phase must define public declarations, ownership/lifetimes, error surface,
target matrix, security tests, and performance budgets before implementation.
Preserve existing builtin IDs and lowerings behind imported exports initially.
The contracts are approved; only the implementation explicitly identified above
is present. This roadmap does not imply that runtime grants, environment access,
or structured logging are complete.

### 12.6 Approved Phase 2 contracts and implementation status

This subsection fixes the version-1 project document and API contracts. JSON
objects are closed schemas: unknown or duplicate keys, malformed/trailing JSON,
unsupported versions, invalid types or values, and documents larger than 1 MiB
are rejected. Every object member shown as required is required; optional members
may be omitted. Arrays are JSON arrays (not `null`). The Go `project` package
implements parsing/validation of both schemas; runtime grant consumption is a
separate, unimplemented integration. Parser and validation errors do not echo
document bytes, filesystem paths, field values, or grant URLs/credentials.

#### 12.6.1 Manifest (`tlang.json`)

The manifest has exactly these top-level members:

```json
{
  "schemaVersion": 1,
  "language": "1",
  "entry": "src/main.tlang",
  "target": {"os": ["linux"], "arch": ["amd64"]},
  "capabilities": {
    "env": [],
    "filesystem": [],
    "process": [],
    "network": {"connect": [], "listen": []},
    "lifecycle": {"signals": []}
  },
  "databases": {},
  "limits": {}
}
```

- `schemaVersion` is the integer `1`; `language` is the string `"1"`.
  `entry` is a project-relative `.ts` or `.tlang` path. `target.os` and
  `target.arch` are unique arrays of supported Go platform names; an empty
  array imposes no restriction.
- `capabilities.env` is a unique array of environment variable names.
  `filesystem` entries have exactly `root` (project-relative path) and `modes`
  (unique members of `read`, `write`, `create`, `delete`, `list`). `process`
  entries have exactly `executable` (an exact bare executable identity) and
  optional positive `maxArgs` / `maxOutputBytes` constraints.
- Each `network.connect` / `network.listen` rule has `protocol` (`tcp` or `udp`),
  optional `host` (an exact DNS name or IP address), nonempty `ports` (inclusive
  `{from, to}` ranges within 1..65535), and `groups` (unique named scopes).
  At least one of `host` or `groups` is required. `lifecycle.signals` contains
  unique `SIGINT` / `SIGTERM` values.
- `databases` maps each resource name to exactly `{ "engine": ..., "config":
  ... }`; engine is `postgres` or `sqlite`, and `config` is a non-secret
  identifier. URLs and credentials do not belong in the manifest.
- `limits` may contain nonnegative integer bounds: `maxWorkers` (256),
  `maxQueueCapacity` (65536), `maxDeadlineMs` (86400000), `maxFileBytes`
  (1073741824), `maxProcessOutputBytes` (67108864), `maxNetworkConnections`
  (65536), and `maxDatabaseConnections` (4096). Omitted or zero means no
  manifest-specified bound; nonzero values cannot exceed the listed ceiling.

Manifest discovery searches the supplied path and its ancestors for exactly one
`tlang.json`; none, multiple manifests, or an invalid manifest is an error. The
CLI uses the manifest's `entry` when invoked with the project directory and
requires an explicit file argument to match that entry. This discovery and CLI
integration is implemented. It does not authorize capabilities or cause runtime
grants to be loaded.

#### 12.6.2 Runtime grants

The grants document has exactly `schemaVersion`, `manifestSha256`,
`capabilities`, `databases`, and `limits`. `schemaVersion` is integer `1`;
`manifestSha256` is 64 lowercase hexadecimal characters and is SHA-256 of the
**exact manifest file bytes**, before parsing or normalization. A formatting or
line-ending change to the manifest therefore requires a matching grants digest.
The `capabilities` and `limits` objects use the same shapes and validation rules
as the manifest. `databases` maps the same resource names to `{ "url": "..." }`;
URLs/credentials are accepted here, not in the manifest, and must match the
declared engine (`postgres`/`postgresql` for PostgreSQL; `file`/`sqlite` for
SQLite).

Its JSON shape is:

```json
{
  "schemaVersion": 1,
  "manifestSha256": "<64 lowercase hex characters>",
  "capabilities": {
    "env": [],
    "filesystem": [],
    "process": [],
    "network": {"connect": [], "listen": []},
    "lifecycle": {"signals": []}
  },
  "databases": {},
  "limits": {}
}
```

The digest placeholder is explanatory, not a literal accepted value; the actual
field must contain the lowercase SHA-256 hex digest described above.

Validation binds the digest to the manifest, requires exact database-name and
capability coverage (sets are order-independent), allows process grant bounds to
be narrower than requested bounds, and rejects grant limits that loosen any
nonzero requested limit. Unspecified requested limits can be bounded by grants.
Malformed or insufficient documents fail closed. `ParseGrants`,
`ValidateGrants`, and `ParseAndValidateGrants` implement these checks in the Go
project package. No runtime or CLI path currently reads a grants document,
checks it at startup, or enforces a grant on an operation; do not treat these
library APIs as completed runtime capability enforcement.

#### 12.6.3 Environment and structured console APIs

The approved source contracts are:

```text
env.get(name: string): string | null
console.debug(message: string, fields?: Record<string, JsonValue | null>): void
console.info(message: string, fields?: Record<string, JsonValue | null>): void
console.warn(message: string, fields?: Record<string, JsonValue | null>): void
console.error(message: string, fields?: Record<string, JsonValue | null>): void
```

These signatures are contract notation, not shipped declarations. `env.get`
looks up one exact name; no enumeration, mutation, globbing, prefix lookup, or
case folding is provided. A name absent from manifest requests or runtime grants
produces a permission-category `Error`; an authorized name that is unset returns
`null`. The latter is not an error.

Each console call writes one JSON object followed by a newline. The record has
`timestamp` (UTC RFC 3339), `level` (`debug`, `info`, `warn`, or `error`),
`message`, and `fields` (a string-keyed object whose values are `JsonValue` or
`null`). Metadata keys are owned by the record envelope; application fields are
nested and cannot override the timestamp, level, or message. Logging is
best-effort and writes of complete records must not interleave across scheduler
threads. `JsonValue` here uses the generic JSON value domain defined in §12.3.2.

The standard-module registry exposes `db` from `tlang/db` and `console`/`env`
from `tlang/system`; it reserves but does not expose `tlang/http`. Phase 2.5
implements authorized `env.get`, all four structured console methods, and the
minimal `JsonValue` constructor/accessor surface. Feature analysis currently
reports generic reachable runtime APIs (`database`, `http`, `console`) and the
`libpq` native requirement; it does not infer database engines or validate
requested/granted capabilities. `Error` currently carries `status` and
`message`, with appended mutable `category` and `code` string fields and
`internal` defaults, in the language model, generated C ABI, and runtime throw /
catch implementation.
