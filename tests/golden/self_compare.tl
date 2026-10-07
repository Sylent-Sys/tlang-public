interface User {
    id: int64;
}

fn selfOps(x: int64, f: float64, b: bool, u: User): bool {
    let same = x == x;
    let fsame = f == f;
    let bsame = b != b;
    let usame = u == u;
    x = x;
    f = f;
    b = b;
    let overlap = (x > 5) || (x < 10);
    let never = (x < 5) && (x > 10);
    return same && fsame && bsame && usame && overlap && never;
}
