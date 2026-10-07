# runtime/fuzz — opt-in libFuzzer harnesses

Byte-level [libFuzzer](https://llvm.org/docs/LibFuzzer.html) harnesses for two
locked C translation units:

- `fuzz_json.c` — the JSON scanner `runtime/src/json.c` (`json_skip_value`,
  `json_scan_string`, `json_scan_int64`, `json_scan_float64`, …).
- `fuzz_http.c` — the static HTTP request parser `parse_request` in
  `runtime/src/http.c`.

These are **developer opt-in**. The Go test suite never builds or runs them, so
`go build ./...` / `go test ./...` stay green with no C toolchain. They are
compiled and run by hand with `clang -fsanitize=fuzzer,address,undefined`
(libFuzzer + ASan + UBSan in one binary), inside the `tlang-dev` container,
which ships the clang sanitizer/fuzzer runtimes.

The invariant each harness enforces: **no crash, no out-of-bounds access, and
bounded time** over arbitrary input. A crash reported against `runtime/src/json.c`
or `runtime/src/http.c` is a bug in the locked runtime — **stop and raise it**;
never edit the runtime or add a suppression to make the harness pass.

## Build and run (inside the `tlang-dev` container)

Run via the host wrapper `scripts/dev.sh '<cmd>'` (mounts the repo at `/src`),
or directly if you already have a sanitizer-capable clang.

### JSON scanner harness

`fuzz_json.c` **links** the real `json.c` on the command line (it does not
`#include` it), together with its minimal transitive dependencies. The two
out-of-memory-path symbols `arena.c` pulls in (`tlang_log_error`,
`tlang_request_abort`) are provided as never-reached stand-ins inside
`fuzz_json.c`, so the link set stays minimal.

```sh
clang -std=c11 -D_GNU_SOURCE -g -O1 \
  -fsanitize=fuzzer,address,undefined -fno-sanitize-recover=all \
  -I runtime/include -I runtime/src \
  runtime/fuzz/fuzz_json.c runtime/src/json.c runtime/src/strings.c \
  runtime/src/arena.c runtime/src/errors.c \
  -o /tmp/fuzz_json -lpthread -lm
/tmp/fuzz_json -max_total_time=60 runtime/fuzz/corpus/json
```

### HTTP request-parser harness

`fuzz_http.c` reaches the **static** `parse_request` by
`#include "../src/http.c"` — it compiles the real locked translation unit in
place. **`http.c` is therefore NOT listed again on the command line.** Only
`http.c`'s transitive dependencies are linked; the net/scheduler/fiber/console
entry points its connection loop references (never reached by `parse_request`)
are satisfied by never-called stand-ins inside `fuzz_http.c`.

```sh
clang -std=c11 -D_GNU_SOURCE -g -O1 \
  -fsanitize=fuzzer,address,undefined -fno-sanitize-recover=all \
  -I runtime/include -I runtime/src \
  runtime/fuzz/fuzz_http.c runtime/src/strings.c runtime/src/slices.c \
  runtime/src/arena.c runtime/src/errors.c runtime/src/router.c \
  runtime/src/query.c \
  -o /tmp/fuzz_http -lpthread -lm
/tmp/fuzz_http -max_total_time=60 runtime/fuzz/corpus/http
```

## Seed corpora

`corpus/json/` and `corpus/http/` hold small versioned seed inputs that steer
coverage from the first run. libFuzzer treats the directory as both the seed
set and the place it writes newly-discovered interesting inputs.
