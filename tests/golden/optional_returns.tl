interface User {
    id: int64;
}

fn f64opt(x: float64): float64 | null {
    if (x > 0.0) {
        return x;
    }
    return null;
}

fn i32opt(x: int32, y: int32): int32 | null {
    if (x > y) {
        return x;
    }
    return null;
}

fn i64opt(x: int64): int64 | null {
    if (x == 0) {
        return null;
    }
    return x;
}

fn boolopt(b: bool): bool | null {
    if (b) {
        return true;
    }
    return null;
}

fn stropt(s: string, empty: bool): string | null {
    if (empty) {
        return null;
    }
    return s;
}

fn useropt(u: User, none: bool): User | null {
    if (none) {
        return null;
    }
    return u;
}

fn widen(n: int64): int64 | null {
    let o: int64 | null = n;
    let p: int64 | null = null;
    let q: int64 | null;
    return o;
}
