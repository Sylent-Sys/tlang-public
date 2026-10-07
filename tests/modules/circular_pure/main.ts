import { callB } from "./a";
import { callA } from "./b";

fn main(): void {
    let x = callB();
    let y = callA();
}
