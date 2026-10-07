// AC-14: a specifier that lexically escapes the project root is E-IMPORT and
// never echoes a path outside the root.
import { x } from "../outside";

fn main(): void {}
