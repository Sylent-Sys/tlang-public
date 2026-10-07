// codegen-mandatory: 15.2.6 (float % lowers to tlang_mod_f64; link needs -lm)
fn fmod(a: float64, b: float64): float64 {
    return a % b;
}

fn fmodAssign(a: float64): float64 {
    let x = a;
    x %= 2.5;
    return x;
}

fn main(): void {
    let r = fmod(7.5, 2.0);
    let s = fmodAssign(r);
}
