// AC-17 (namespace access): a namespace value access m.nope for a name the
// module does not declare is E-IMPORT "no exported member".
export fn make(): int64 {
    return 1;
}
