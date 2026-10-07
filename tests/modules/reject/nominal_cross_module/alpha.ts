// AC-22 (reject): two modules each declare their own nominal User. Passing
// alpha's User where beta's User is expected is an E-TYPE mismatch — the two
// are distinct nominal types even though structurally alike.
export interface User {
    id: int64;
}
