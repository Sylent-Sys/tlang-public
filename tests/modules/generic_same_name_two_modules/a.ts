// Two modules each declare a generic fn id<T> and both use it at int64. Each
// module's instance carries its own module tag, so the two id<int64>
// instances get distinct C names instead of colliding on tl_f_id__i64.

export fn id<T>(x: T): T {
    return x;
}

export fn fromA(): int64 {
    return id(1);
}
