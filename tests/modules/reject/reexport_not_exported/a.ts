// AC-16 (re-export): re-exporting a name the source declares but does not
// export is E-IMPORT "not exported".
fn hidden(): int64 {
    return 1;
}
