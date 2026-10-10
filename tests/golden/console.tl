import { console } from "tlang/system";


fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn logs(n: int64, s: string, o: int64 | null, i: int32, f: float64, b: bool): void {
    console.debug("debug", JsonValue.nullValue());
    console.info("one", JsonValue.nullValue());
    console.info("values", JsonValue.object(["s", "n", "i", "f", "b"], [JsonValue.string(s), JsonValue.number(float64(n)), JsonValue.number(float64(i)), JsonValue.number(f), JsonValue.bool(b)]));
    console.info("computed", JsonValue.array([JsonValue.number(float64(div(n, 2))), JsonValue.string(s + "!"), JsonValue.number(float64(div(n, n)))]));
    console.error("err", JsonValue.object(["n"], [JsonValue.number(float64(n))]));
    console.error("empty", JsonValue.nullValue());
    if (o != null) {
        console.info("optional", JsonValue.number(float64(o)));
    }
    console.info("coalesced", JsonValue.number(float64(o ?? 0)));
    console.warn(i > int32(0) ? "pos" : "neg", JsonValue.nullValue());
}

fn main(): void {
    let o: int64 | null = 1;
    logs(1, "s", o, int32(2), 3.5, true);
}
