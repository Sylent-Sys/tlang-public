// Multi-file e2e fixture (FEAT-004 / AC-31, AC-33): the data model module. Its
// types are consumed through a namespace import in the handler module and the
// root dispatcher.

export interface EchoReq {
    id: int64;
    name: string;
}

export interface EchoResp {
    id: int64;
    name: string;
}

export interface BenchResp {
    id: int64;
    name: string;
}
