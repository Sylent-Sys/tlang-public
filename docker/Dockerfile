# TLang Linux development image: Go toolchain, C compilers (GCC, Clang, and a
# LINK-capable TCC built from source), libpq/PostgreSQL 17, and a PostgreSQL
# server for the E2E harness (spec §12).
#
# DESIGN TENSION (see docker/README.md): one image must simultaneously provide
#   (a) Go 1.26,
#   (b) a tcc that can LINK executables on modern glibc, and
#   (c) libpq >= 17 (runtime/src/pg.c uses PGcancelConn, a PostgreSQL 17+ API).
# golang:1.26-trixie gives (a) Go 1.26 and (c) libpq 17, but the DISTRO tcc
# (apt-get install tcc) is the 0.9.27 release, which SEGFAULTS when LINKING on
# glibc >= 2.34 (as trixie ships). We therefore build tcc from the upstream mob
# branch (0.9.28rc), which links cleanly on modern glibc, in a build stage and
# copy it into the final image. The distro tcc is intentionally NOT installed.

# ---------------------------------------------------------------------------
# Stage 1: build a link-capable tcc (0.9.28rc) from pinned upstream source.
# ---------------------------------------------------------------------------
FROM golang:1.26-trixie@sha256:af00f232205a01baf323bddc5bb4a4ea2e4cbf49e8ccc988beb2558d0f9c882a AS tcc-builder

# Pinned mob-branch revision (TCC version 0.9.28rc) for reproducibility.
# repo.or.cz is the canonical upstream for the tinycc mob branch.
ARG TCC_REPO=https://repo.or.cz/tinycc.git
ARG TCC_REV=43c7708b85681a2fd4451c8a541af4494a8919b2

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      gcc make binutils libc6-dev git ca-certificates \
 && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    git clone "${TCC_REPO}" /tmp/tcc; \
    cd /tmp/tcc; \
    git checkout "${TCC_REV}"; \
    test "$(git rev-parse HEAD)" = "${TCC_REV}"; \
    ./configure --prefix=/opt/tcc; \
    make; \
    make install

# Prove the freshly built tcc can LINK and RUN a trivial executable on this
# glibc. This is the whole point of building from source; fail the build loudly
# if it cannot (that is the 0.9.27-on-glibc-2.34 segfault we are avoiding).
RUN set -eux; \
    printf 'int main(void){return 0;}\n' > /tmp/linkcheck.c; \
    /opt/tcc/bin/tcc -o /tmp/linkcheck /tmp/linkcheck.c; \
    /tmp/linkcheck; \
    echo "tcc link-check OK:"; \
    /opt/tcc/bin/tcc -v

# ---------------------------------------------------------------------------
# Stage 2: the final development image.
# ---------------------------------------------------------------------------
FROM golang:1.26-trixie@sha256:af00f232205a01baf323bddc5bb4a4ea2e4cbf49e8ccc988beb2558d0f9c882a

# The toolchain that already works on trixie. NOTE: the distro `tcc` is
# deliberately absent here; the link-capable tcc comes from the build stage.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      gcc clang lld llvm libclang-rt-19-dev make gdb \
      libpq-dev postgresql postgresql-client \
      curl ca-certificates procps util-linux psmisc \
 && rm -rf /var/lib/apt/lists/*

# The link-capable tcc (0.9.28rc) built in stage 1. Symlink it onto PATH so
# `CC=tcc` and bare `tcc` resolve to it.
COPY --from=tcc-builder /opt/tcc /opt/tcc
RUN ln -sf /opt/tcc/bin/tcc /usr/local/bin/tcc

# initdb refuses to run as root; the E2E harness runs PostgreSQL as this user.
RUN useradd -m -u 1000 -s /bin/bash tlang || true

ENV PATH="/usr/lib/postgresql/17/bin:${PATH}" \
    GOFLAGS="-buildvcs=false" \
    CGO_ENABLED=0

WORKDIR /src
