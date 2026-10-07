fn shadow(x: int64, c: bool): int64 {
    let total = x;
    {
        let x = 5;
        total = total + x;
    }
    if (c) {
        let x = 7;
        total = total + x;
    } else {
        let x = 9;
        total = total + x;
    }
    {
        let y = total;
        {
            let y = y + 1;
            total = y;
        }
    }
    let unused = 5;
    let setOnly = 1;
    setOnly = 2;
    let z: int64;
    let s: string;
    let o: float64 | null;
    return total + z;
}

fn paramSetOnly(p: int64, q: string): void {
    p = 5;
    q = "x";
}
