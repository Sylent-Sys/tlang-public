import { console } from "tlang/system";


// F7 gap filler: script entry with globals, loops, console and a may-fail main.
interface Counter {
    hits: int64;
    names: string[];
}

let counter: Counter = new global Counter();
let limit: int64 = 3;

fn bump(n: int64): int64 {
    counter.hits = counter.hits + n;
    counter.names.push("n".clone_global());
    return counter.hits / limit;
}

fn main(): void {
    let i: int64 = 0;
    while (i < limit) {
        console.info("hit", JsonValue.number(float64(i)));
        i++;
    }
    for (const name of counter.names) {
        console.info(name, JsonValue.string("name"));
    }
}
