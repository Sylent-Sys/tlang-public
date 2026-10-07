import * as a from "./alpha";
import * as b from "./beta";

fn main(): void {
    let ua = new a.User();
    ua.id = 1;
    let ub = new b.User();
    ub.name = "x";
    let n = a.alphaId(ua);
    let s = b.betaName(ub);
}
