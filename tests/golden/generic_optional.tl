fn pick<T>(x: T | null, d: T): T {
    if (x != null) {
        return x;
    }
    return d;
}

fn force<T>(x: T | null): T {
    return x!;
}

fn orElse<T>(x: T | null, d: T): T {
    return x ?? d;
}

fn main(): void {
    let a: int64 | null = 5;
    let p1 = pick<int64>(a, 0);
    let s: string | null = "x";
    let e: string | null = null;
    let p2 = pick<string | null>(s, e);
    let f1 = force<int64>(a);
    let f2 = force<string | null>(s);
    let o1 = orElse<int64>(a, 1);
    let o2 = orElse<string | null>(s, e);
}
