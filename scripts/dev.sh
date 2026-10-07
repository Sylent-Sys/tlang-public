#!/usr/bin/env bash
# Runs a shell command inside the tlang-dev Linux container with the repository
# mounted at /src. Example: scripts/dev.sh "make -C runtime test CC=clang"
set -euo pipefail
repo="$(cd "$(dirname "$0")/.." && pwd)"

if [[ "${1:-}" == "--rebuild" ]] || ! docker image inspect tlang-dev:latest >/dev/null 2>&1; then
    [[ "${1:-}" == "--rebuild" ]] && shift
    docker build -t tlang-dev:latest "$repo/docker"
fi

exec docker run --rm -i \
    -v "$repo:/src" \
    -v tlang-gocache:/root/.cache/go-build \
    -v tlang-gomod:/go/pkg/mod \
    -v "${repo}/.asan-diagnostics:/tmp/tlang-asan" \
    -e TLANG_KEEP_ASAN_BINARIES=1 \
    -e TLANG_ASAN_ARTIFACT_DIR=/src/.asan-diagnostics \
    -w /src \
    --cap-add SYS_PTRACE --security-opt seccomp=unconfined \
    --ulimit nofile=65536:65536 \
    tlang-dev:latest bash -c "$*"
