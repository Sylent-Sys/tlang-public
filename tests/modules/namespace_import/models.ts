// AC-11: a namespace import exposes exports as m.User (type position) and
// m.greet (value/call position).

export interface User {
    id: int64;
    name: string;
}

export fn greet(u: User): string {
    return u.name;
}
