# TLang development image

One reproducible container image that bundles **every** tool needed to run the
entire TLang workflow regardless of host:

- **Go 1.26** front-end: `go build`, `go vet`, `go test ./...`.
- The codegen golden + **gcc/clang/tcc** ccompile matrix:
  `go test -tags ccompile ./tests/...`.
- The **C-runtime matrix** across gcc, clang, and a link-capable tcc, with
  **libpq / PostgreSQL 17**: `make -C runtime test CC=<gcc|clang|tcc> [SAN=1] [PG=0]`.

The image is defined by [`Dockerfile`](./Dockerfile). The thin host wrappers
[`scripts/dev.sh`](../scripts/dev.sh) (bash) and
[`scripts/dev.ps1`](../scripts/dev.ps1) (PowerShell) build it on first use and
run a command inside it with the repository mounted at `/src`.

## Build the image

With podman (on this project's host `docker` is a symlink to podman, so the
`docker` CLI works too):

```sh
podman build -t tlang-dev:latest docker/
```

or via the wrapper (which also wires the Go cache volumes and the ptrace /
seccomp options the sanitizers need):

```sh
scripts/dev.sh --rebuild "true"      # bash
./scripts/dev.ps1 -Rebuild "true"    # PowerShell
```

The build runs a self-check in the tcc build stage: it compiles **and links and
runs** a trivial `int main(void){return 0;}` with the freshly built tcc. If that
fails the whole image build fails, so a successful build guarantees a
link-capable tcc.

## Run the workflow inside the image

Mount the repo at `/src` (use the `:Z` SELinux relabel flag on podman):

```sh
# Go front-end + the ccompile golden matrix
podman run --rm -v "$PWD:/src:Z" -w /src tlang-dev:latest bash -c \
  'go test ./... -count=1 && go test -tags ccompile ./tests/...'

# C-runtime matrix across every compiler
podman run --rm -v "$PWD:/src:Z" -w /src tlang-dev:latest bash -c \
  'for c in gcc clang tcc; do make -C runtime clean && make -C runtime test CC=$c; done'

# Sanitizer leg (gcc or clang only) and the libpq-less leg
podman run --rm -v "$PWD:/src:Z" -w /src tlang-dev:latest bash -c \
  'make -C runtime clean && make -C runtime test CC=gcc SAN=1'
podman run --rm -v "$PWD:/src:Z" -w /src tlang-dev:latest bash -c \
  'make -C runtime clean && make -C runtime test CC=clang PG=0'
```

Always `make -C runtime clean` between legs: each `CC`/`SAN`/`PG` combination
uses a distinct `build/<variant>/` tree, so cleaning only guards against a stale
tree, but it keeps the mounted `/src` tidy.

The `tests/test_pg_*.c` cases are DB-backed and are skipped unless
`TLANG_TEST_DATABASE_URL` is set to a disposable PostgreSQL connection string
(the image ships a PostgreSQL 17 server and client for that harness).

## The resolved design tension

A single image must simultaneously satisfy three constraints that pull against
each other:

1. **Go 1.26** for the compiler front-end.
2. **A tcc that can LINK executables on modern glibc.** The runtime matrix
   builds and runs test binaries with `CC=tcc`, so an object-only tcc is not
   enough.
3. **libpq >= 17.** `runtime/src/pg.c` calls `PGcancelConn`, a PostgreSQL 17+
   libpq API, so the `PG=1` build does not even compile against an older libpq.

`golang:1.26-trixie` gives us (1) Go 1.26 and (3) libpq 17 (Debian trixie ships
PostgreSQL 17 — verified in-image: `pg_config --version` reports 17.11 and
`libpq-dev` is `17.11`). The trap is (2): `apt-get install tcc` on trixie
installs the **0.9.27 release**, which **segfaults when LINKING** on glibc >=
2.34 (trixie's glibc is 2.41). That is a glibc-2.34 regression against the old
tcc, not a defect in the runtime code.

**Resolution:** a multi-stage build.

- **Stage 1 (`tcc-builder`)** clones the upstream tinycc *mob* branch at a
  **pinned commit** and builds it from source with
  `./configure --prefix=/opt/tcc && make && make install`. That revision is TCC
  **0.9.28rc**, which links cleanly on modern glibc. The stage then proves the
  built tcc can link and run a trivial program before the build is allowed to
  proceed.
- **Stage 2 (final)** is `FROM golang:1.26-trixie` pinned by digest, apt-installs
  the toolchain that already works on trixie (gcc, clang, lld, llvm,
  libclang-rt, make, gdb, libpq-dev, postgresql, postgresql-client, plus
  curl/ca-certificates/procps/util-linux/psmisc), copies `/opt/tcc` from stage 1,
  and symlinks `/opt/tcc/bin/tcc` to `/usr/local/bin/tcc` so `CC=tcc` resolves.
  The distro tcc is intentionally **not** installed.

The 0.9.28rc tcc still has the same string-literal-pooling codegen quirk as
0.9.27, but the runtime test harness was made quirk-agnostic (see
`runtime/tests/test_json.c`), so `test_json` is **green on tcc** too.

### Reproducibility pins

- Base image: `golang:1.26-trixie@sha256:af00f232205a01baf323bddc5bb4a4ea2e4cbf49e8ccc988beb2558d0f9c882a`.
- tcc source: `https://repo.or.cz/tinycc.git` at commit
  `43c7708b85681a2fd4451c8a541af4494a8919b2` (version 0.9.28rc). Both are
  exposed as `ARG`s (`TCC_REPO`, `TCC_REV`) at the top of the build stage.

### PostgreSQL 17 note

Because the image ships libpq 17, **all** matrix legs — including the tcc leg —
build and run with `PG=1`. No leg has to drop to `PG=0` to work; `PG=0` remains
available purely to exercise the libpq-less build path. (If a future base ever
regressed below libpq 17 and the tcc `PG=1` compile broke, the fallback would be
to run the tcc leg at `PG=0`, which still exercises the full non-DB runtime
including `test_json`, while gcc/clang stay at `PG=1` — but that is not needed
today.)
