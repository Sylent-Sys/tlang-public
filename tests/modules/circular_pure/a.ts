// AC-13 (golden): a circular import of pure types and functions is legal (no
// cyclic global VALUE dependency). a and b import each other's functions.

import { fromB } from "./b";

export fn fromA(): int64 {
    return 1;
}

export fn callB(): int64 {
    return fromB();
}
