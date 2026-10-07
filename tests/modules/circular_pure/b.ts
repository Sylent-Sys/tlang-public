import { fromA } from "./a";

export fn fromB(): int64 {
    return 2;
}

export fn callA(): int64 {
    return fromA();
}
