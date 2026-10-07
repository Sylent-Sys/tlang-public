// AC-17 (re-export): re-exporting a name the source has no declaration for is
// E-IMPORT.
export fn present(): int64 {
    return 1;
}
