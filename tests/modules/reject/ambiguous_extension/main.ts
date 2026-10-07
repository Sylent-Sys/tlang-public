// AC-18: an extensionless specifier that matches both ./util.ts and
// ./util.tlang is an ambiguous E-IMPORT.
import { a } from "./util";

fn main(): void {}
