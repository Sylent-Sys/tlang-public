import * as a from "./alpha";

export interface User {
    id: int64;
}

export fn take(u: User): int64 {
    return u.id;
}

export fn fromAlpha(): a.User {
    let u = new a.User();
    u.id = 1;
    return u;
}
