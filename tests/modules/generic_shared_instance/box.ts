// AC-26: a generic function exported from one module and instantiated at the
// same type argument in two importing modules is monomorphized once — a single
// C function for wrap<int64> across the whole program.

export fn wrap<T>(x: T): T {
    return x;
}
