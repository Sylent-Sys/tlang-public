import { wrap } from "./box";

export fn second(): int64 {
    return wrap<int64>(2);
}
