// AC-12: a transitive re-export chain. deep -> mid -> near -> main, with an
// alias introduced at the near hop.

export fn deep(): int64 {
    return 99;
}
