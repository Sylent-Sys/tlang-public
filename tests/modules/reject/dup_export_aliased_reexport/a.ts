// An aliased re-export (Y as X) and a direct export of X export X twice:
// E-IMPORT at the direct export, with a note at the alias.
export { Y as X } from "./b";

export fn X(): int64 {
    return 5;
}
