// AC-19: a specifier that resolves to no file is E-IMPORT naming the
// specifier and the probed candidates (all inside the root).
import { a } from "./nope";

fn main(): void {}
