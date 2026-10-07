import { baseValue } from "./base";

export let counter: int64 = baseValue() + 10;

export fn derivedValue(): int64 {
    return counter;
}
