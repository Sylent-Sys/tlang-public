// AC-13 (reject): a cross-module cyclic global VALUE dependency is E-INIT.
import { b } from "./b";

export let a: int64 = b;
