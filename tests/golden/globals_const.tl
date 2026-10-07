interface Cfg {
    port: int32;
}

let counter: int64;
let limit: int64 = 100;
let negative: int64 = -9;
let small: int32 = 7;
let smallNeg: int32 = int32(-5);
let ratio: float64 = 2;
let half: float64 = 0.5;
let label: string = "svc\t\"x\"\\";
let enabled: bool = true;
let maybe: int64 | null;
let cfgs: Cfg[] | null;
let name: string;

fn main(): void {
}
