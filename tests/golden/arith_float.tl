fn favg(a: float64, b: float64): float64 {
    return (a + b) / 2.0;
}

fn fops(a: float64, b: float64): bool {
    let c = a * b - 1.5;
    let d = -c;
    let e: float64 = 2;
    let f = 1e308 * 10.0;
    let g = 0.1 + 0.2;
    let h = a / 0.0;
    return c < d && e >= 2.0 || a == b;
}

fn conv(i: int64, j: int32, f: float64): int64 {
    let a = float64(i);
    let b = int64(j);
    let c = int32(i);
    let d = int32(f);
    let e = int64(f);
    let g = float64(j);
    let h = int64(3.9);
    let k = int32(5000000000);
    let m = float64(3);
    return b + e + h + int64(c) + int64(d) + int64(k) + int64(a) + int64(g) + int64(m);
}
