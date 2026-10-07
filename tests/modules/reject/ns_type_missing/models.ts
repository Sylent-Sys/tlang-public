// AC-16/17 (namespace type access): a qualified type m.Nope for a name the
// module does not export is E-IMPORT "no exported member".
export interface User {
    id: int64;
}
