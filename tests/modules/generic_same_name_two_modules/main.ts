import { console } from "tlang/system";


import { fromA } from "./a";

fn id<T>(x: T): T {
    return x;
}

fn main(): void {
    let a = fromA();
    let b = id(2);
    console.info("sum", JsonValue.number(float64(a + b)));
}
