// AC-17: a duplicate local binding within one import statement is E-IMPORT.
import { a, b as a } from "./util";

fn main(): void {}
