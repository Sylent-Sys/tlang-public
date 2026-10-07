interface User {
    id: int64;
}

fn opts(a: int64 | null, b: int32 | null, c: float64 | null, d: bool | null, s: string | null, u: User | null, xs: int64[] | null): int64 {
    let total: int64 = 0;
    if (a != null) {
        total = total + a;
    }
    if (b != null) {
        total = total + int64(b);
    }
    if (c != null && c > 1.0) {
        total = total + int64(c);
    }
    if (d != null) {
        if (d) {
            total = total + 1;
        }
    }
    if (s == null) {
        return total;
    }
    total = total + s.len;
    if (null != u) {
        total = total + u.id;
    }
    if (xs != null) {
        total = total + xs.len;
    }
    let forced = a!;
    let forced32 = b!;
    let forcedF = c!;
    let forcedB = d!;
    let forcedS = s!;
    let forcedU = u!;
    let forcedXs = xs!;
    let ra = a ?? 0;
    let rb = b ?? 7;
    let rc = c ?? 1.5;
    let rdd = d ?? false;
    let rs = s ?? "none";
    let ru = u ?? new User();
    let isNull = a == null;
    let strNull = s != null;
    return total + forced + ra + int64(rb);
}

fn widenArgs(n: int64, s: string, u: User): void {
    takeOpt(n, s, u);
}

fn takeOpt(a: int64 | null, s: string | null, u: User | null): void {
}
