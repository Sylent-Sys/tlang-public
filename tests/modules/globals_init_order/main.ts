import { baseValue } from "./base";
import { derivedValue } from "./derived";

let total: int64 = baseValue() + derivedValue();

fn main(): void {
    total = total + 1;
}
