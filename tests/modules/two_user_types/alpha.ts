// AC-21 (golden): two modules each declare their own User. Per-module tags
// keep the two structs distinct in C, so both coexist in one TU.

export interface User {
    id: int64;
}

export fn alphaId(u: User): int64 {
    return u.id;
}
