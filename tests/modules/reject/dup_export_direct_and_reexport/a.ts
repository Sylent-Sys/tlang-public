// A re-exported name that the module also exports directly is exported
// twice: E-IMPORT at the direct export, with a note at the re-export.
export { X } from "./b";

export fn X(): int64 {
    return 5;
}
