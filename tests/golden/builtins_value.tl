fn strings(s: string, t: string): int64 {
    let n = s.len;
    let e = s.eq(t);
    let sl = s.slice(1, 3);
    let st = s.startsWith("ab");
    let en = s.endsWith(t);
    let ix = s.indexOf("x");
    let cl = s.clone();
    let cg = s.clone_global();
    let parsed = s.toInt();
    let lit = "42".toInt();
    s.eq("unused");
    return n + ix + parsed + lit;
}

fn numbers(a: int32, b: int64, c: float64, d: bool): string {
    return a.toString() + b.toString() + c.toString() + d.toString() + (5).toString();
}

fn idx(i: int64): int64 {
    return i;
}

fn arrays(xs: int64[]): int64 {
    xs.push(1);
    xs.push(xs.len);
    xs.push(idx(xs.len));
    return xs.len;
}

fn errors(): int32 {
    let e = new Error("boom", 404);
    let m = e.message;
    let st = e.status;
    return st;
}
