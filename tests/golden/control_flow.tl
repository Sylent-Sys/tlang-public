fn div(a: int64, b: int64): int64 {
    return a / b;
}

fn next(i: int64): int64 {
    return i + 1;
}

fn loops(n: int64, d: int64): int64 {
    let sum: int64 = 0;
    for (let i = 0; i < n; i++) {
        if (i == 3) {
            continue;
        }
        if (i > 10) {
            break;
        }
        sum += i;
    }
    let j: int64 = 0;
    while (j < n) {
        j++;
    }
    for (;;) {
        break;
    }
    for (let k = 0; k < div(n, d); k = next(k)) {
        if (k == 2) {
            continue;
        }
        sum = sum + k;
    }
    for (let m = 0; m < n; m + 1) {
        break;
    }
    if (n > 0) {
        sum = sum + 1;
    } else if (div(n, d) > 0) {
        sum = sum + 2;
    } else if (n < -5) {
        sum = sum + 3;
    } else {
        sum = sum - 1;
    }
    if (n > 100) let w = 1;
    while (j > n) let v = 2;
    for (let p = 0; p < 2; p++) for (let r = 0; r < 2; r++) sum++;
    return sum;
}
