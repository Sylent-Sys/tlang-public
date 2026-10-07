// AC-24 / AC-25: two modules each declare a global named "counter" with an
// initializer. Distinct module tags give the two globals distinct, injective
// C names, and globals initialize in dependency order (base before derived
// before main).

export let counter: int64 = 1;

export fn baseValue(): int64 {
    return counter;
}
