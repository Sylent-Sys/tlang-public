# TLang Modules — Design Proposal (v1+)

**Status: IMPLEMENTED.** This was the RFC; it is now
the design of record. The open questions were signed off as: Option A (one C
translation unit), ES-style syntax, and the full sugar set (aliased imports,
re-exports, namespace imports, default exports). §0 describes TLang *before*
modules and is kept for context. Where the shipped code differs from this text,
the code wins.

*Original status:* PROPOSAL / RFC. This document records the design for adding a
module system (`import` / `export`) to TLang so a program can span many files
instead of living in one "god file." It is a design to discuss and approve, not
a committed decision. It is deliberately written to the same bar as
`docs/DESIGN.md`: every claim about the existing compiler is checked against the
current source, and every open choice is named with a recommendation and a
reason.

Section references like §5.3 point into the spec
(`TLang_Technical_Specification_1.2.1.md`); DESIGN §x points into
`docs/DESIGN.md`.

---

## 0. Motivation and the current state

### 0.1 Where TLang is today

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
- Re-exports, aliased imports (`import { X as Y }`), wildcard/`import *`, circular
  *value* initialization semantics beyond what §4.4 defines. These are listed as
  future extensions in §9.
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
import { greet } from "../util/strings";

fn route_dispatcher(ctx: Context): void { ... }   // uses User, displayName
```

- The specifier is a **relative path** (`"./x"`, `"../y/z"`) resolved against the
  importing file's directory, with the extension optional (`.ts`/`.tlang`;
  resolution order defined in §3.2). Absolute paths and bare specifiers
  (`"user"`, `"@scope/pkg"`) are **rejected** in v1+ (reserved for a future
  package system, §9).
- The import list names specific exported symbols. Each imported name is bound
  in the importing file's namespace and must resolve to an `export`ed
  declaration in the target file (else `E-IMPORT`).
- Importing a name that exists but is not exported is `E-IMPORT` ("X is not
  exported by ./models/user"), distinct from "no such name."

### 2.3 Lexical / grammar additions

New tokens: `IMPORT`, `EXPORT`, `FROM` (keywords). `import`/`export`/`from`
join the keyword table. (The string specifier reuses the existing string-literal
token; `{` `}` `,` already exist.)

Grammar (extending DESIGN §2.2):

```
program    := importDecl* topDecl*
importDecl := 'import' '{' importName (',' importName)* ','? '}' 'from' STRING ';'
importName := IDENT
topDecl    := 'export'? decorator* fnDecl
            | 'export'? interfaceDecl
            | 'export'? typeDecl
            | 'export'? letDecl | 'export'? constDecl
```

Rules the parser enforces locally (no cross-file knowledge needed): all
`import` declarations come first, before any `topDecl` (TypeScript allows
interleaving, but requiring imports first keeps the grammar and reader simple;
this can be relaxed later with no breaking change). A duplicate imported name in
one file, or an empty `{}`, is a parse-level `E-IMPORT`/`E-PARSE`.

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

**One file = one module.** Its identity is its normalized, repo-relative path
(e.g. `models/user.ts`). There is no `package` clause and no directory grouping
in v1+ (that is a Go-style alternative considered and rejected in §8).

### 3.2 Specifier resolution

Given an importing file at `dir/` and a specifier `"./models/user"`:

1. Resolve the specifier relative to `dir`, producing a candidate path.
2. If it has an extension, use it. Otherwise try `<path>.ts` then `<path>.tlang`
   (the two accepted source extensions, DESIGN §1); if both exist it is an
   `E-IMPORT` ambiguity error (deterministic, not a silent pick).
3. Normalize to a repo-relative, slash-separated path for the module identity so
   two specifiers that reach the same file (`./a` from one dir, `../x/a` from
   another) are the **same module**, loaded once.
4. The resolved path must stay **within the project root** (the directory of the
   root module, or a configured source root). Escaping it (`../../etc/...`) is an
   `E-IMPORT` error. No absolute paths, no symlink traversal outside the root.
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
  path-sorted order** so the merged declaration list and thus the emitted C are
  byte-identical regardless of filesystem iteration order (the determinism suite
  demands this).

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

Top-level `let`/`const` run their initializers once per scheduler at startup, in
**declaration order** today (DESIGN §2.10). Across modules this needs a defined
order. Proposal:

- Global initializers run in **dependency order**: a module's globals initialize
  after the globals of every module it imports (topological order of the import
  graph), and within a module in source order.
- A **cycle in global-initializer dependencies** (module A's global reads module
  B's global and vice versa, where the import graph is cyclic *through globals*)
  is an `E-INIT` error — the same spirit as the existing non-optional-field-cycle
  rejection. Pure type/function import cycles are fine (§3.3); only a cyclic
  *global-value* dependency is rejected.
- The generated `tl__init_globals` becomes a sequence of per-module init blocks
  emitted in that topological order (still one function, still per-scheduler).

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

- Give every module a stable, collision-free **module tag** derived
  deterministically from its normalized path (e.g. a sanitized path plus a short
  content-independent disambiguator; the exact scheme is an implementation
  detail, but it must be injective and path-order-independent).
- Qualify user-declared C names with the module tag: `tl_<mod>__User`,
  `tl_f_<mod>__greet`, `tl_m_<mod>__User__displayName`. The existing
  `tl_`/`tl_f_`/`tl_m_` family and the reserved `globals`/`_init_globals` shapes
  are preserved; the module tag slots in as an additional, already-reserved
  segment.
- **`CheckDeclName` / collision checking (historical PR #8) extends, not changes:** within a
  module the same reservations apply; across modules the module tag keeps names
  injective by construction, so the cross-declaration collision pass now runs
  per module and the global uniqueness is structural. This is an *implementation*
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

## 7. Testing strategy (when/if implemented)

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

- Aliased / renamed imports (`import { User as U }`), re-exports
  (`export { X } from "./y"`), and namespace imports (`import * as m`).
- A package system: bare specifiers (`"strings"`), a project manifest, versioned
  / third-party / remote dependencies, and a resolution algorithm beyond relative
  paths. (The specifier grammar already reserves bare/absolute forms by rejecting
  them, so adding this later is non-breaking.)
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

This is a **heavy, multi-package feature** that deliberately reopens several
currently-frozen packages (`parser`, `checker`, `types` mangling, `codegen`). It
is net-new scope the spec never defined — so it is a *feature design*, not a bug
fix. Nothing here is implemented; this document exists to be reviewed and
approved (or redirected) before any code is written.

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
