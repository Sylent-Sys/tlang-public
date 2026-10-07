fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn safe(a: int64): int64 {
    return a + 1;
}

fn chain(a: int64, b: int64): int64 {
    let q = div(a, b);
    let r = div(div(a, b), safe(q));
    return q + r;
}

fn andOr(ok: bool, a: int64, b: int64): bool {
    let x = ok && div(a, b) > 0;
    let y = ok || div(a, b) > 0;
    let z = div(a, b) > 0 && ok;
    return x && y || z;
}

fn loopCond(n: int64, d: int64): int64 {
    let count: int64 = 0;
    let i = n;
    while (div(i, d) > 0) {
        i = i - d;
        count++;
    }
    return count;
}

fn voidCalls(a: int64): void {
    div(a, a);
    safe(a);
}

fn main(): void {
    let x = chain(10, 2);
    voidCalls(x);
}
