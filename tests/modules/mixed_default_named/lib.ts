// AC-10: a module with both a default export and named exports, consumed by a
// mixed "default, { named }" import.

export default fn run(): int64 {
    return 1;
}

export fn step(n: int64): int64 {
    return n + 1;
}

export let base: int64 = 10;
