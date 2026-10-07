# Runs a shell command inside the tlang-dev Linux container with the repository
# mounted at /src. Example:
#   ./scripts/dev.ps1 "make -C runtime test CC=clang"
#   ./scripts/dev.ps1 "go test ./tests/..."
# Builds the image on first use. Go caches persist in named volumes.
param(
    [Parameter(Mandatory = $true, Position = 0)][string]$Command,
    [switch]$Rebuild
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot

$haveImage = docker image inspect tlang-dev:latest 2>$null
if ($Rebuild -or -not $haveImage) {
    docker build -t tlang-dev:latest (Join-Path $repo 'docker') | Out-Host
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

# SYS_PTRACE lets LeakSanitizer work; seccomp=unconfined allows perf-style syscalls in benchmarks.
docker run --rm -i `
    -v "${repo}:/src" `
    -v tlang-gocache:/root/.cache/go-build `
    -v tlang-gomod:/go/pkg/mod `
    -v "${repo}/.asan-diagnostics:/tmp/tlang-asan" `
    -e TLANG_KEEP_ASAN_BINARIES=1 `
    -e TLANG_ASAN_ARTIFACT_DIR=/src/.asan-diagnostics `
    -w /src `
    --cap-add SYS_PTRACE --security-opt seccomp=unconfined `
    --ulimit nofile=65536:65536 `
    tlang-dev:latest bash -c $Command
exit $LASTEXITCODE
