import { make } from "./make";
import { consume } from "./consume";

fn main(): void {
    let u = make(3);
    let n = consume(u);
}
