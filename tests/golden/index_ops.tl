fn pos(i: int64): int64 {
    return i;
}

fn arr(xs: int64[]): int64[] {
    return xs;
}

fn reads(xs: int64[], ys: string[], i: int32, grid: int64[][]): int64 {
    let a = xs[0];
    let b = xs[pos(1)];
    let c = arr(xs)[2];
    let d = xs[i];
    let s = ys[0] + ys[1];
    let g = grid[1][2];
    if (xs[0] > xs[1]) {
        return a;
    }
    return a + b + c + d + g;
}
