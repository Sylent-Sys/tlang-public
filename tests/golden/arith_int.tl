fn add(a: int64, b: int64): int64 {
    return a + b - 1;
}

fn mul32(a: int32, b: int32): int32 {
    return a * b;
}

fn neg(a: int64, b: int32): int64 {
    let c = -b;
    return -a + int64(c);
}

fn divVar(a: int64, b: int64): int64 {
    return a / b;
}

fn modVar(a: int32, b: int32): int32 {
    return a % b;
}

fn divConst(a: int64): int64 {
    return a / 7 + a % -1;
}

fn divZero(a: int64): int64 {
    return a / 0;
}

fn modZero(a: int32): int32 {
    return a % int32(0);
}

fn constants(): int64 {
    let maxv = 9223372036854775807;
    let minv = -9223372036854775808;
    let minv2 = -9223372036854775807 - 1;
    let wrapped = 9223372036854775807 + 1;
    let big = 9223372036854775808;
    let allOnes = 0xFFFFFFFFFFFFFFFF;
    let hex = 0xFF;
    let under = 1_000_000;
    let folded = 2 * 3 + 4;
    return maxv + minv + minv2 + wrapped + big + allOnes + hex + under + folded;
}

fn constants32(): int32 {
    let a: int32 = int32(-2147483648);
    let b: int32 = 2147483647;
    let c: int32 = int32(5) * int32(1000000000);
    return a + b + c;
}

fn compound(x: int64, d: int64, y: int32): int64 {
    x += 3;
    x -= 4;
    x *= 5;
    x /= d;
    x %= 7;
    x /= 0;
    x++;
    x--;
    y += 1;
    y *= 2;
    y %= 0;
    y--;
    return x + int64(y);
}
