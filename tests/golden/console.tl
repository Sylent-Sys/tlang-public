import { console } from "tlang/system";


fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn logs(n: int64, s: string, o: int64 | null, i: int32, f: float64, b: bool): void {
    console.info();
    console.info("one");
    console.info(s, n, i, f, b, 7, 2.5, true);
    console.info(div(n, 2), s + "!", div(n, n));
    console.error("err", n);
    console.error();
    if (o != null) {
        console.info(o);
    }
    console.info(o ?? 0);
    console.info(i > int32(0) ? "pos" : "neg");
}

fn main(): void {
    let o: int64 | null = 1;
    logs(1, "s", o, int32(2), 3.5, true);
}
