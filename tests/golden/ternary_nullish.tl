fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn tern(c: bool, a: int64, b: int64, o: int64 | null): int64 {
    let x = c ? a : b;
    let y = c ? div(a, b) : 0;
    let z: int64 | null = c ? a : null;
    let w = o ?? div(a, b);
    let v = c ? (a > b ? a : b) : -1;
    let s = c ? "yes" : "no";
    let t = c ? o : a;
    return x + y + w + v;
}
