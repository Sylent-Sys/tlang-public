import { User } from "./shared";

export fn make(id: int64): User {
    let u = new User();
    u.id = id;
    u.name = "shared";
    return u;
}
