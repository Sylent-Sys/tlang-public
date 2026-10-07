// A name re-exported by two statements is exported twice: E-IMPORT at the
// second, with a note at the first.
export { X } from "./b";
export { X } from "./c";
