# TLang Technical Specification & Architecture Blueprint

Version: 1.2.1
Target domain: REST APIs with high throughput and low tail latency (target SLO: p99 ≤ 2.5 ms on the benchmark harness, see §12)
Compiler frontend: Go (Lexer, Pratt Parser, Type Checker, Lowering, C11 Codegen, CLI Driver)
Compilation target: ISO C11 (Clang / GCC / Tiny C Compiler, TCC)
Target architecture: Linux x86_64 / ARM64 (POSIX, epoll, libpq)

> Items marked [Proposed] are design decisions added in version 1.1.0 to close gaps in 1.0.0. None of them has been confirmed yet. The change history is in Appendix A and the open decisions are in Appendix B.
>
> Every performance figure in this document (p99, TCC startup time, buffer sizes) is a design target or default value. No measurements back them yet.

---

## 1. Overview and Vision

TLang is a statically typed, data-oriented programming language with TypeScript syntax. Its goal is to remove the sources of latency in the Node.js/V8 runtime for I/O-heavy workloads and REST APIs.

TLang is not a superset of JavaScript and does not use V8 or libuv. TLang code is compiled ahead of time (AOT) to C11, then to a native binary. It has four main components:

1. A fiber scheduler without function coloring: non-blocking I/O with no `async`/`await`.
2. A per-request arena: there is no garbage collector, and all request memory is released at once.
3. Strings as slices (fat pointers): protocol parsing without copying bytes.
4. JSON parsers and serializers generated per struct at compile time.

### 1.1 Non-Goals
* No compatibility with npm packages.
* No dynamic features (`any`, `eval`, `prototype` mutation).
* Linux only (x86_64 / ARM64). Windows and macOS are not supported in v1.
* PostgreSQL is the only integrated database.
* Work-stealing and fiber migration between threads are outside the scope of v1 (see §7.3).

### 1.2 Deployment Assumptions [Proposed]
* A reverse proxy or load balancer terminates TLS in front of the TLang server. The HTTP engine serves plaintext HTTP/1.1 only.
* One TLang process runs per host (or per group of cores), with one scheduler per core (§7.3).

---

## 2. Node.js Problems and the TLang Solutions

| Area | Node.js (V8 + libuv) | TLang |
| :--- | :--- | :--- |
| Type checking | Types are erased at runtime. `tsc` uses structural typing and runs as a build step separate from the runtime. | Nominal typing at compile time. Types map directly to C structs, and symbols are resolved by hash ID (average $O(1)$). |
| Garbage collector | The V8 GC is generational and partly concurrent/incremental, but it still causes pauses and churn when thousands of JSON objects and HTTP headers are allocated per request. | A per-fiber arena. Request memory is allocated with a bump pointer and released by a reset ($O(1)$ in the common case, no sweeping). |
| Concurrency and I/O | One event loop per process. Functions suffer from function coloring (`async`/`await`) and the Promise microtask queue. | A fiber scheduler on top of `epoll`. Functions look synchronous with no `await`. |
| Strings and URLs | `req.url`, `req.params`, and `req.query` produce new JavaScript strings or objects on the V8 heap. | Slices of `{ const char* data, size_t len }` that point directly into the request buffer (zero-copy, lifetime rules in §6.3). |
| JSON serialization | `JSON.parse` and `JSON.stringify` run synchronously on the event loop and build a generic object tree on the heap. | C11 parsers and serializers are generated per struct at compile time. |
| Middleware | Dynamic onion model: `next()` closures, Promise chaining, indirect function calls. | `@Use` guards are flattened into sequential C branches that can be inlined. |
| Database transactions | Releasing connections and rolling back depend on a `try/finally` the developer must write. Connections leak if it is missed. | Transactions are transpiled to C blocks with a `goto __tx_rollback` path that always returns the connection to the pool. |

---

## 3. Design Decisions and Language Paradigm

### 3.1 A Strict TypeScript Subset
* Supports `interface`, `type`, `let`, `const`, basic generics, and decorators.
* No `any`, `prototype` mutation, `eval`, or `undefined`.
* [Proposed] `null` is valid only on explicit optional types (`T | null`). Optional interface fields are written `name?: string`.

### 3.2 Data-Oriented with Static Method Receivers
* An `interface` describes a static data layout (a plain C struct).
* A method receiver (`fn (u: User) methodName()`) is dispatched statically to `User_methodName(User* u)`, with no vtable.

### 3.3 Reference Semantics for Objects
* A variable of an `interface` type is always a pointer (`User*`) and is passed as a pointer. Programmers do not write pointer syntax.
* An interface variable must be initialized with `new` (`let req = new CreateUserReq();`). Declaring one without initialization (`let req: CreateUserReq;`) is a compile error. [Proposed]
* Objects are already pointers, so `ctx.bindJson(req)` does not use `&`. The `&` token is reserved for primitive out-parameters in the future.

### 3.4 Fat Pointer Strings
```c
typedef struct { const char* data; size_t len; } tlang_string;
```
* Substrings and protocol parsing allocate no heap memory and do not depend on a `\0` terminator.
* Because it has no terminator, a `tlang_string` must not be passed directly to a C API that expects a C string (for libpq, see §11.2).
* [Proposed] Minimal stdlib for `string`: `s.eq("literal")` (lowered to a `len` check plus `memcmp`), `s.slice(a, b)`, `s.startsWith(...)`, and `s.clone()` (copies into the arena, see §6.3).

### 3.5 Basic Type System [Proposed]

| TLang | C11 | Notes |
| :--- | :--- | :--- |
| `int32`, `int64` | `int32_t`, `int64_t` | Arithmetic overflow wraps (compiled with `-fwrapv`) and is not undefined behavior. |
| `float64` | `double` | |
| `bool` | `bool` | |
| `string` | `tlang_string` | A slice that does not own its data. |
| `interface T` | `T*` | Allocated in the arena (§6). |
| `T[]` | `tlang_slice_T` (`items`, `len`, `cap`) | Element layout is not decided yet (Appendix B, item 2). |
| `void` | `void` | |

### 3.6 Error Model (`throw`, `try`, `catch`)
Errors do not use stack unwinding. The compiler lowers `throw` and runtime errors to a `fiber->err` flag, checks it after every call that can fail, and jumps with `goto` to the nearest `catch` label. [Proposed] An error that is still uncaught at the end of a handler produces a `500` response, and the arena is still reset.

---

## 4. Compilation Pipeline and CLI Driver

The compiler frontend is written in Go. The reasons: fast parsing, native per-file concurrency, and distribution of the CLI as a single binary.

```
[TLang Source (.ts / .tlang)]
              │
              ▼ (Lexer & Pratt Parser, Go)
            [AST]
              │
              ▼ (Type Checker)
         [Typed AST]
              │
              ▼ (Lowering: method receiver, @Use, try/catch, transaction)
       [C11 Code Generator]
              │
       ┌──────┴────────────────────────┐
       ▼ (Dev Mode)                    ▼ (Release Mode)
 [tlang run <file>]              [tlang build <file> -o <bin>]
       │                               │
       ▼ (TCC)                         ▼ (Clang / GCC -O3 -flto)
 In-memory execution               Optimized native binary
 (target < 15 ms)
```

### CLI Driver

`tlang run <file>` uses `tcc -run` to compile and run the program directly in RAM. The save-and-run cycle is close to running a Node.js script, but what runs is native machine code. The 15 ms figure is a target that still needs to be measured.

`tlang build <file> -o app` uses Clang with `-O3 -flto -s -DNDEBUG -fwrapv`. The final size depends on how the runtime and libpq are linked. LTO allows cross-unit inlining, and the optimizer may auto-vectorize.

### Constraints to maintain [Proposed]
* The generated C code must compile with both TCC and Clang/GCC. Check TCC's support for C11 features (for example `_Atomic` and `_Generic`) before codegen uses them.
* TCC does almost no optimization, and its behavior on undefined behavior can differ from Clang. CI runs the test suite in release mode (Clang) and in an ASan/UBSan build.

---

## 5. Compiler Frontend Components (Go)

### 5.1 Token System (`token/token.go`)
Tokens cover TypeScript lexemes, delimiters, operators, and decorators (`@`).

```go
package token

type TokenType string

type Position struct {
	Line   int
	Column int
}

type Token struct {
	Type    TokenType
	Literal string
	Pos     Position
}

const (
	ILLEGAL TokenType = "ILLEGAL"
	EOF     TokenType = "EOF"

	IDENT  TokenType = "IDENT"
	INT    TokenType = "INT"
	FLOAT  TokenType = "FLOAT"
	STRING TokenType = "STRING"

	ASSIGN    TokenType = "="
	PLUS      TokenType = "+"
	MINUS     TokenType = "-"
	ASTERISK  TokenType = "*"
	SLASH     TokenType = "/"
	MOD       TokenType = "%"
	BANG      TokenType = "!"
	AMPERSAND TokenType = "&" // reserved, see §3.3
	AT        TokenType = "@"
	PIPE      TokenType = "|" // optional type: T | null
	QUESTION  TokenType = "?" // optional field: name?: string

	EQ     TokenType = "=="
	NOT_EQ TokenType = "!="
	LT     TokenType = "<"
	GT     TokenType = ">"
	LT_EQ  TokenType = "<="
	GT_EQ  TokenType = ">="
	AND    TokenType = "&&"
	OR     TokenType = "||"

	COMMA     TokenType = ","
	DOT       TokenType = "."
	COLON     TokenType = ":"
	SEMICOLON TokenType = ";"
	LPAREN    TokenType = "("
	RPAREN    TokenType = ")"
	LBRACE    TokenType = "{"
	RBRACE    TokenType = "}"
	LBRACKET  TokenType = "["
	RBRACKET  TokenType = "]"
	ARROW     TokenType = "=>"

	LET       TokenType = "LET"
	CONST     TokenType = "CONST"
	FN        TokenType = "FN"
	INTERFACE TokenType = "INTERFACE"
	TYPE      TokenType = "TYPE"
	NEW       TokenType = "NEW"
	RETURN    TokenType = "RETURN"
	IF        TokenType = "IF"
	ELSE      TokenType = "ELSE"
	FOR       TokenType = "FOR"
	WHILE     TokenType = "WHILE"
	BREAK     TokenType = "BREAK"
	CONTINUE  TokenType = "CONTINUE"
	TRY       TokenType = "TRY"
	CATCH     TokenType = "CATCH"
	THROW     TokenType = "THROW"
	TRUE      TokenType = "TRUE"
	FALSE     TokenType = "FALSE"
	NULL      TokenType = "NULL"
)

var keywords = map[string]TokenType{
	"let": LET, "const": CONST, "fn": FN, "interface": INTERFACE,
	"type": TYPE, "new": NEW, "return": RETURN, "if": IF, "else": ELSE,
	"for": FOR, "while": WHILE, "break": BREAK, "continue": CONTINUE,
	"try": TRY, "catch": CATCH, "throw": THROW,
	"true": TRUE, "false": FALSE, "null": NULL,
}

func LookupIdent(ident string) TokenType {
	if tok, ok := keywords[ident]; ok {
		return tok
	}
	return IDENT
}
```

### 5.2 Abstract Syntax Tree (`ast/ast.go`)

* `InterfaceStatement`: the interface name and a list of `FieldDefinition` (name, type, optional or required).
* `FunctionStatement`: `Decorators`, `Receiver` (`*Parameter`), `Parameters`, `ReturnType`, `Body`.
* `Decorator`: a static `@Use(...)` annotation that is resolved at compile time.
* `LetStatement`: a local variable with a static type. Initialization is required for interface types (§3.3).
* `NewExpression`: instantiation of an object or slice (`new User()`, `new User[]()`).
* `MemberExpression`: access to a property or method (`ctx.path`, `u.id`).
* `AssignmentExpression`: field mutation (`u.id = 10`).
* `TryCatchStatement`: error handling without stack unwinding (§3.6).
* `TransactionStatement`: a `db.transaction((tx) => { ... })` block. This block is a lexical scope. The compiler allocates no closure for it (§11.4).

### 5.3 Pratt Parser (`parser/parser.go`)
The parser uses recursive descent combined with Pratt parsing for binary operators, function calls, receiver binding, and decorators. Precedence from lowest to highest:

```go
const (
	_ int = iota
	LOWEST
	ASSIGN      // =
	OR          // ||
	AND         // &&
	EQUALS      // == !=
	LESSGREATER // < > <= >=
	SUM         // + -
	PRODUCT     // * / %
	PREFIX      // !x  -x
	CALL        // fn(x)
	MEMBER      // a.b  a[i]
)
```

[Proposed] The parser does error recovery (synchronizing on `;` or `}`) so that one syntax error does not hide the next. Every diagnostic carries a `Position` (line and column).

---

## 6. Memory Engine: Per-Fiber Arena

### 6.1 Lifecycle
In a REST API, memory lives as long as the request, so TLang allocates memory in a per-fiber arena instead of the global heap.

One fiber serves one connection (HTTP keep-alive). The arena is reset after each response is sent, without waiting for the fiber to finish.

```
[ Request arrives ] ──────────────────────────────────────┐
       │                                                  │
       ▼                                                  │
 [ Arena ] ──► [ Request Header ]                         │ (Request cycle)
               [ Query Params ]                           │
               [ DTO / Struct User ]                      │
               [ JSON Output String ]                     │
       │                                                  │
       ▼                                                  │
[ Response sent ] ────────────────────────────────────────┘
       │
       ▼
[ arena_reset() ] ──► offset = 0 (O(1) in the common case)
```

### 6.2 Runtime Implementation (tiered arena)
The 1.0.0 arena fell back to `malloc` when full and never freed it, so every large request leaked memory. This version links extra chunks into a single chain and frees them on reset.

```c
#define ARENA_CHUNK_SIZE (128 * 1024)   // first chunk, kept across requests
#define ARENA_ALIGNMENT  8

typedef struct ArenaChunk {
    struct ArenaChunk* next;
    size_t capacity;
    size_t offset;
    uint8_t data[];                     // flexible array member (C11)
} ArenaChunk;

typedef struct {
    ArenaChunk* first;                  // owned by the fiber, never freed
    ArenaChunk* current;
} MemoryArena;

// Creates a chunk of capacity max(ARENA_CHUNK_SIZE, n) and links it. NULL on OOM.
ArenaChunk* arena_grow(MemoryArena* a, size_t n);

static inline void* arena_alloc(MemoryArena* a, size_t size) {
    if (size > SIZE_MAX - (ARENA_ALIGNMENT - 1)) return NULL;
    size_t n = (size + (ARENA_ALIGNMENT - 1)) & ~(size_t)(ARENA_ALIGNMENT - 1);
    ArenaChunk* c = a->current;
    if (c->offset + n > c->capacity) {
        c = arena_grow(a, n);
        if (!c) return NULL;
    }
    void* p = c->data + c->offset;
    c->offset += n;
    return p;
}

static inline void arena_reset(MemoryArena* a) {
    ArenaChunk* c = a->first->next;
    while (c) { ArenaChunk* nx = c->next; free(c); c = nx; }
    a->first->next = NULL;
    a->first->offset = 0;
    a->current = a->first;
}
```

A reset costs $O(1)$ as long as the request fits in the first chunk, and $O(k)$ for $k$ extra chunks.

`new User()` is lowered to a zero-initialized allocation:

```c
User* u = (User*)tlang_alloc_zeroed(sizeof(User));
```

`tlang_alloc_zeroed` never returns `NULL` to user code. On OOM it aborts the request with a `503` response, so the process does not go down. There is no `free(u)`: the memory is released when the arena is reset.

### 6.3 Lifetime and Escape Rules [Proposed]
Zero-copy plus an arena opens the risk of dangling references. The compiler or runtime enforces these rules:

1. A `tlang_string` that comes from the request buffer (path, query, header, body, JSON value) is valid only until the response is sent. The buffer is reused for the next keep-alive request.
2. Data that must outlive a single request (caches, configuration, global state) must not be allocated with a plain `new`. Use an explicit global-heap allocation (`new global T()` or `s.clone_global()`), and store copies, not slices that borrow the request buffer.
3. The compiler rejects storing an arena value or request slice in a global variable or in a field of a global object. A simple escape analysis is enough: assigning a request-scoped value to a global symbol is a compile error.
4. `s.clone()` copies the string into the fiber arena (same lifetime as the request). `s.clone_global()` copies it to the global heap.

---

## 7. Concurrency Model: Fiber Scheduler and Netpoller

TLang does not use `async`/`await`. I/O operations (HTTP sockets, database) look synchronous at the language level, and the runtime runs them non-blocking through Linux `epoll`.

### 7.1 Fiber Structure
```c
#ifndef TLANG_STACK_SIZE
#define TLANG_STACK_SIZE (256 * 1024)
#endif
#ifndef TLANG_MAX_FIBERS
#define TLANG_MAX_FIBERS 10000          // per scheduler
#endif

typedef enum {
    FIBER_DEAD,
    FIBER_READY,
    FIBER_RUNNING,
    FIBER_WAITING_IO
} FiberState;

typedef struct Fiber {
    int id;
    FiberState state;
    FiberContext ctx;          // see §7.4
    void* stack_base;          // mmap: [guard page][stack]
    size_t stack_size;
    MemoryArena arena;
    void (*fn)(void*);
    void* arg;
    int waiting_fd;
    uint64_t deadline_ns;      // 0 = no timeout
    int err;                   // error flag, §3.6
    struct Fiber* next_free;   // fiber pool freelist
} Fiber;
```

Stacks are allocated with `mmap` and given a guard page at the low end, so a stack overflow ends as a detectable `SIGSEGV` and does not silently corrupt memory. New stack pages are committed the first time they are touched.

Fibers are pooled through a freelist, so `mmap` and `munmap` do not happen on the request path.

Memory budget per scheduler: `MAX_FIBERS × (used STACK_SIZE + ARENA_CHUNK_SIZE)`. Measure real stack usage before lowering `TLANG_STACK_SIZE`: libpq calls and `snprintf` can use more than 64 KB on some paths.

When the fiber pool is exhausted, `accept` is deferred or new connections get a `503`. Without this limit, the number of allocations grows without control.

### 7.2 Non-Blocking I/O Hooks
When a socket is not ready (`EAGAIN` / `EWOULDBLOCK`):
1. The fiber records the target fd and sets its state to `FIBER_WAITING_IO`.
2. The socket is registered with `epoll` using `EPOLLONESHOT`: `EPOLL_CTL_ADD` the first time, then `EPOLL_CTL_MOD` to re-arm it.
3. The fiber switches context back to the scheduler loop.
4. The OS thread runs other fibers. When the kernel marks the fd ready, the fiber returns to `FIBER_READY` and continues from where it stopped.
5. [Proposed] `EPOLLERR`, `EPOLLHUP`, and `EPOLLRDHUP` wake the fiber with an error (client disconnected). A passed `deadline_ns` wakes the fiber with a timeout error.

### 7.3 Thread Topology [Proposed]
Version 1.0.0 says M:N, but the mechanism it describes (one OS thread, one epoll) is M:1. The model in this version:

* N scheduler threads, usually one per core. Each has its own epoll and its own `SO_REUSEPORT` listener.
* No state is shared between schedulers. A fiber stays on the scheduler where it was created, so the request path takes no locks.
* The database connection pool is also per scheduler (§11.3), so no cross-thread synchronization is needed.
* Work-stealing between schedulers is deferred to a later version.

### 7.4 Context Switch [Proposed]
On glibc Linux, `swapcontext` calls `rt_sigprocmask` on every switch, which is one syscall on the hot path. The dev implementation may use `ucontext`. The production implementation uses an assembly context switch (x86_64 and ARM64) that saves and restores only the callee-saved registers and the stack pointer.

---

## 8. HTTP Engine and Zero-Copy Routing

### 8.1 Request Parser
The request is read into a local buffer. The parser takes the method, path, query, and headers by storing pointers into the original buffer in a `tlang_string`, with no `malloc`. A body larger than the local buffer is read into a buffer in the fiber arena.

```c
typedef struct {
    int client_fd;
    Fiber* fiber;
    MemoryArena* arena;
    tlang_string method;
    tlang_string path;
    tlang_string query;
    tlang_string body;
    tlang_route_param params[8];
    size_t param_count;
    tlang_http_header headers[32];
    size_t num_headers;
    bool response_sent;
} Context;
```

Limits and rejections [Proposed]. All values are defaults and can be configured:

| Condition | Default | Response |
| :--- | :--- | :--- |
| Total header size | 8 KB | `431` |
| Number of headers | 32 | `431` |
| Path plus query length | 2 KB | `414` |
| Body size | 1 MB | `413` |
| Number of dynamic segments | 8 | `404` |
| Header read timeout | 5 seconds | connection closed (prevents Slowloris) |
| Keep-alive idle time | 60 seconds | connection closed |

v1 scope: HTTP/1.1 with `Content-Length`. Chunked request bodies are not supported yet and get a `411` or `501`. Pipelined requests are processed in order. `ctx.header(name)` matches header names case-insensitively.

### 8.2 Dynamic Segment Route Matcher (`/users/:id`)
Matching proceeds segment by segment:
* Static segments are matched with `memcmp`.
* A dynamic segment (`:id`) records a slice of its value from the URL buffer into `ctx->params`.
* `ctx.paramInt("id")` converts ASCII to `int64_t` in place, with no temporary string. [Proposed] If the value is not a valid number or overflows, the runtime throws `BadRequest` (a `400` response if not caught).

### 8.3 On-Demand Query Scanner
`ctx.query("page")` scans `ctx->query` linearly without building a hash map. For short query strings the data is still in the L1/L2 cache, so the scan is cheap. [Proposed] Values are percent-decoded lazily into the arena, only if they contain `%` or `+`. Otherwise the value is returned as a zero-copy slice.

---

## 9. JSON Serialization and Deserialization

### 9.1 Zero-Copy Deserializer (`ctx.bindJson(req)`)
The Go compiler reads each `ast.InterfaceStatement` and generates a C11 parser for it. The 1.0.0 parser did not handle unknown keys (the loop never ended), commas between fields, data after `}`, or required fields. This version handles them:

```c
// Generated for 'interface CreateUserReq'
bool CreateUserReq_parse_json(tlang_string raw, CreateUserReq* out, MemoryArena* arena) {
    const char* p   = raw.data;
    const char* end = raw.data + raw.len;
    uint32_t seen = 0;                       // bitmask of fields already filled

    p = json_skip_ws(p, end);
    if (p >= end || *p != '{') return false;
    p++;
    p = json_skip_ws(p, end);
    if (p < end && *p == '}') { p++; goto done; }

    for (;;) {
        tlang_string key;
        p = json_skip_ws(p, end);
        if (!json_scan_key(&p, end, &key)) return false;
        p = json_skip_ws(p, end);
        if (p >= end || *p != ':') return false;
        p++;
        p = json_skip_ws(p, end);

        if (key.len == 2 && memcmp(key.data, "id", 2) == 0) {
            if (!json_scan_int64(&p, end, &out->id)) return false;
            seen |= 1u << 0;
        } else if (key.len == 4 && memcmp(key.data, "name", 4) == 0) {
            // No escapes: slice into the request buffer. With escapes (\" \\ \uXXXX): decoded into the arena.
            if (!json_scan_string(&p, end, arena, &out->name)) return false;
            seen |= 1u << 1;
        } else {
            if (!json_skip_value(&p, end, /*depth=*/0)) return false;
        }

        p = json_skip_ws(p, end);
        if (p >= end) return false;
        if (*p == ',') { p++; continue; }
        if (*p == '}') { p++; break; }
        return false;
    }

done:
    p = json_skip_ws(p, end);
    if (p != end) return false;
    return (seen & 0x3u) == 0x3u;            // all required fields must be present
}
```

Parser rules [Proposed]:
* Required or optional fields follow the interface declaration (`name?: string` means optional).
* Nesting depth is limited (default 32) so the fiber stack does not run out.
* If a key appears twice, the last value is used.
* String slices produced by parsing follow the lifetime rules in §6.3.

### 9.2 Struct and Slice Serializer (`ctx.json(200, u)`)
* `ctx.json(200, u)` is lowered to `Context_json(ctx, 200, User_to_json(u))`.
* `ctx.json(200, users)` is lowered to `Context_json(ctx, 200, User_slice_to_json(users))`.
* The output JSON buffer is allocated in the fiber arena and released when the arena is reset.
* [Proposed] The serializer escapes strings correctly (`"`, `\`, and control characters below `0x20`) and formats `int64` and `float64` without temporary allocations.

---

## 10. Middleware and Decorators (`@Use`)

In Node.js, middleware adds overhead through `next()` closure allocation and Promise microtasks. TLang flattens the guard chain at compile time.

```typescript
@Use(logger, authGuard)
fn (ctx: Context) handleDashboard(): void {
    ctx.text(200, "Dashboard Content");
}
```

The compiler inserts the guards at the start of the C function:

```c
void Context_handleDashboard(Context* ctx) {
    /* inserted by @Use */
    if (!logger(ctx)) goto __guard_denied;
    if (!authGuard(ctx)) goto __guard_denied;

    Context_text(ctx, 200, TLANG_STR("Dashboard Content"));
    return;

__guard_denied:
    if (!ctx->response_sent) Context_text(ctx, 500, TLANG_STR("Guard produced no response"));
}
```

Guard calls allocate no memory and do not go through function pointers, so the optimizer can inline them with `-O3`.

Semantics [Proposed]:
* Guards run from left to right. A guard that returns `false` stops the chain.
* A guard that rejects a request must already have written a response. If it has not, the runtime sends a `500` (see the code above) so the connection does not hang.
* `@Use` supports only logic that runs before the handler. Logic that runs after the handler (for example recording duration or response status) cannot be expressed yet. Appendix B, item 4, proposes `@After(...)`.

---

## 11. Database Integration and Transactions

TLang integrates PostgreSQL (`libpq`) into the epoll netpoller.

### 11.1 Non-Blocking Query Execution
`PQsetnonblocking(conn, 1)` is not enough to stop `PQexec` from blocking. The runtime uses the asynchronous libpq API:

1. The connection is created with `PQconnectStart` and `PQconnectPoll`. The socket is obtained through `PQsocket`.
2. The query is sent with `PQsendQueryParams` (or `PQsendQueryPrepared`), then `PQflush` is called until it finishes. If the socket is not ready for writing, the fiber waits for `EPOLLOUT`.
3. Results are read in a loop of `PQconsumeInput` and `PQisBusy`. If the connection is still busy, the fiber waits for `EPOLLIN`.
4. `PQgetResult` is called until it returns `NULL`.

[Proposed] Prepared statements are cached per connection (key: hash of the SQL text) so parse and plan are not repeated on every request.

### 11.2 Parameters and Strings
A `tlang_string` has no `\0` terminator, while libpq text-format parameters require one. There are two ways around this: send string parameters in binary format (pointer and length, for `text` or `varchar` columns, with `paramTypes` filled in), or copy them into the arena with a `\0` terminator. `int64` is sent in big-endian binary format.

### 11.3 Connection Pool [Proposed]
* The pool has a bounded size and is owned per scheduler (size is configurable, for example 8 to 32).
* When the pool is empty, the fiber waits in a queue with a timeout. A timeout produces an error (default `503`).
* `statement_timeout` is set on the connection. If the client disconnects in the middle of a query, the runtime calls `PQcancel`, then returns or closes the connection.

### 11.4 Compile-Time Structured Transactions
A `db.transaction((tx) => { ... })` block is not a closure. The compiler unrolls it into a `goto` structure. Version 1.0.0 did not check for `COMMIT` and `BEGIN` failures. This version does:

```c
{
    TxContext __tx_1;
    if (!tlang_tx_begin(&__tx_1)) goto __tx_fail_1;
    TxContext* tx = &__tx_1;

    tlang_tx_exec(tx, "UPDATE accounts SET balance = balance - 100 WHERE id = 1");
    if (fiber->err) goto __tx_rollback_1;

    tlang_tx_exec(tx, "UPDATE accounts SET balance = balance + 100 WHERE id = 2");
    if (fiber->err) goto __tx_rollback_1;

    if (!tlang_tx_commit(&__tx_1)) goto __tx_rollback_1;
    goto __tx_end_1;

__tx_rollback_1:
    tlang_tx_rollback(&__tx_1);   // idempotent; always returns the connection to the pool
__tx_fail_1:
    goto __tlang_catch_1;         // without try/catch: the error propagates to the caller

__tx_end_1:
    (void)0;
}
```

Semantics [Proposed]:
* A block that finishes normally runs `COMMIT`. A `throw` or an error from `tx.execute` runs `ROLLBACK`, and then the error propagates to the nearest `catch`.
* A `return`, `break`, or `continue` that leaves the transaction block is a compile error in v1. The reason: it is unclear whether the transaction should be committed or rolled back.
* Variables outside the block can be read and written directly. There is no closure capture.

---

## 12. Test Suite and Regression Benchmark

The test suite lives in `tests/` and is written in Go.

1. Unit and golden tests [Proposed]: lexer, parser (including error recovery), type checker, and golden files for the C codegen output of each language feature.
2. Fuzzing [Proposed]: `go test -fuzz` for the lexer and parser. libFuzzer or AFL for the HTTP parser and the generated JSON parsers. The goal: no crashes, hangs, or out-of-bounds access.
3. E2E harness (`e2e_tx_test.go`): compiles a TLang program to C11, builds the binary with Clang (release and ASan/UBSan), runs it as a child process, and tests HTTP endpoints against PostgreSQL in Docker. Scenarios include rollback, an empty pool, and a client disconnect.
4. Load generator (`benchmark_test.go`): parallel persistent HTTP Keep-Alive connections to measure throughput and latency percentiles (p50, p90, p99, p99.9, Max).
5. Performance regression gate: the test fails if any request fails (more than 0) or if p99 exceeds 2.5 ms.

Benchmark methodology [Proposed], so the numbers can be trusted:
* Use an open-loop generator (fixed request rate, like wrk2) or correct for coordinated omission. A closed-loop generator hides tail latency.
* Run the generator on a separate process and core from the server, with CPU pinning, warm-up, and HDR histograms.
* Record the hardware specification, connection count, request rate, and payload size along with the p99 threshold, so the gate can be reproduced.

---

## 13. Example REST API Program (`app.ts`)

```typescript
interface CreateUserReq {
    id: int64;
    name: string;
}

interface UserResponse {
    id: int64;
    name: string;
    status: string;
}

fn logger(ctx: Context): bool {
    return true;
}

fn authGuard(ctx: Context): bool {
    let auth = ctx.header("Authorization");
    if (auth.len == 0) {
        ctx.text(401, "Unauthorized");
        return false;
    }
    return true;
}

@Use(logger, authGuard)
fn (ctx: Context) handleCreateUser(): void {
    let req = new CreateUserReq();
    if (!ctx.bindJson(req)) {
        ctx.text(400, "Invalid JSON Payload");
        return;
    }

    try {
        db.transaction((tx) => {
            tx.execute("INSERT INTO users(id, name) VALUES ($1, $2)", req.id, req.name);
        });

        let res = new UserResponse();
        res.id = req.id;
        res.name = req.name;
        res.status = "SUCCESS";

        ctx.json(201, res);
    } catch (err) {
        ctx.text(500, "Database Transaction Failed");
    }
}

fn route_dispatcher(ctx: Context): void {
    if (ctx.method.eq("POST") && ctx.path.eq("/api/users")) {
        ctx.handleCreateUser();
        return;
    }
    ctx.text(404, "Endpoint Not Found");
}
```

---

## Appendix A. Change History

### 1.2.0 to 1.2.1 (translation)

The whole document was translated from Indonesian to English. The technical content, section numbers, and code are unchanged. The `[Usulan]` tag became `[Proposed]`, and code comments were translated.

### 1.1.0 to 1.2.0 (editing with anti-slop rules)

The technical content did not change, except for one correction noted below. The other changes are to the writing only.

* Performance claims (p99, TCC startup under 15 ms, HTTP limit defaults) are labeled explicitly as targets or defaults, both in the opening note and where the figures appear. No measurements stand behind those figures.
* The claim that TCC does not support `_Atomic` was removed because it was not verified. In its place: check TCC's C11 feature support before using a feature (§4).
* The overview in §1 was rewritten in plain sentences. The sentences about "fundamental weaknesses" and "ergonomics" from 1.0.0 and 1.1.0 were replaced with statements that can be checked.
* Bold was removed from terms in running text, tables, and lists.
* Em dashes, en dashes, and arrows in prose were removed. Number ranges are written with words ("8 to 32").
* Code comments that only repeated the code were removed (for example the labels "TLang code", "Transpiled C code", and "Router Dispatcher"). Comments that explain a reason or behavior were kept.
* Signposting phrases such as "the following version fixes this" were shortened, and quotation marks that served only as emphasis were dropped.

### 1.0.0 to 1.1.0

Inconsistencies fixed:
* The title said sub-millisecond, while the p99 gate is 2.5 ms. The title now states a measurable SLO target.
* The scheduler was called M:N, but the mechanism described is M:1. The topology is now one scheduler per core with no shared state (§7.3).
* The example used `let req: CreateUserReq;` and `bindJson(&req)`, which contradicted reference semantics. The example now uses `new` and `bindJson(req)`.
* The example called `memcmp` directly, although it is not part of the language. It is now `s.eq("...")`.
* `Context` did not contain the arena and fiber that other parts use. Both were added.
* The parser precedence table had only five levels and no binary operators. The table was completed.

Bugs in the sample code:
* Arena: the `malloc` fallback was never freed, and there was no check for `size_t` overflow or `NULL`.
* JSON parser: an unknown key did not advance the pointer (the loop never ended). Commas, trailing data, required fields, and escapes were not handled.
* Transactions: failures of `BEGIN` and `COMMIT` were not checked.
* Tokens: the constants were not typed as `TokenType`, and there was no keyword table.

Claims softened:
* The type-checking comparison (`tsc` uses structural typing, not $O(N)$ duck typing), the V8 GC, binary size, and SIMD.
* "Zero GC-pause" became "no garbage collector".

New sections, all [Proposed]: non-goals and deployment assumptions, the basic type system and error model, lifetime rules, HTTP limits, the asynchronous libpq flow, the connection pool, a production context switch, benchmark methodology, fuzzing, and sanitizers.

## Appendix B. Open Decisions

1. The role of `&`: reserved for primitive out-parameters, or removed from the language?
2. Layout of `T[]`: are interface elements stored as pointers (`T**`, consistent with reference semantics) or contiguously as values (`T*`, friendlier to the cache and closer to the data-oriented claim)?
3. Long-lived objects: the final syntax for the global heap (`new global T()` or something else) and the escape analysis rules (§6.3).
4. Post-handler hooks: is `@After(...)` or a similar mechanism needed for logging and metrics?
5. HTTP v1 scope: are chunked request bodies and HTTP/2 really out of scope?
6. Fiber migration (work-stealing): which version is it deferred to, and do the per-scheduler arenas and pools need to be designed to stay compatible with it?
