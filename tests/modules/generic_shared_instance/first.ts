import { wrap } from "./box";

export fn first(): int64 {
    return wrap<int64>(1);
}
