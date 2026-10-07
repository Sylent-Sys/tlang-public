// AC-21 (reject): an imported name that collides with a local declaration is
// E-NAME.
export fn helper(): int64 {
    return 1;
}
