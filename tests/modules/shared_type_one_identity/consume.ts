import { User } from "./shared";

export fn consume(u: User): int64 {
    return u.id;
}
