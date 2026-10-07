fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn tries(a: int64, b: int64): int64 {
    let r: int64 = 0;
    try {
        r = div(a, b);
    } catch (e) {
        r = int64(e.status);
    }
    try {
        r = div(r, b);
    } catch {
        r = -1;
    }
    try {
        r = div(r, a);
    } catch (unused) {
        r = 0;
    }
    try {
        r = div(a, 0);
    } catch (e) {
        try {
            r = div(r, b);
        } catch (e) {
            r = int64(e.status);
        }
        let msg = e.message;
        r = r + msg.len;
    }
    try {
        r = r + 1;
    } catch (e) {
        r = div(r, r);
    }
    try {
        throw "boom";
    } catch {
        r = 42;
    }
    try {
        r = div(a, b);
    } catch (e) {
        r = div(b, a);
    }
    return r;
}

fn main(): void {
    let x = tries(1, 0);
}
