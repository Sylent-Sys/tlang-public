// AC-22 (golden): one User type is exported from shared and imported by two
// modules; it is a single nominal type with one C struct, so a value produced
// in one module flows into the other without conversion.

export interface User {
    id: int64;
    name: string;
}
