export interface User {
    name: string;
}

export fn betaName(u: User): string {
    return u.name;
}
