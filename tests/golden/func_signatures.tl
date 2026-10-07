// Signatures, methods, and the missing-return zero-value fallback (bodies are empty).
interface User {
    id: int64;
}

fn a(): int64 {
}

fn b(): string {
}

fn c(): float64 | null {
}

fn d(): User {
}

fn e(): Error {
}

fn f(): bool {
}

fn g(): int32 | null {
}

fn h(): int64[] {
}

fn i(): string | null {
}

fn j(): User | null {
}

fn v(): void {
    return;
}

fn params(n: int32, m: int64 | null, s: string, u: User | null, xs: int64[], ctx: Context, flag: bool | null): void {
}

fn (u: User) twice(): int64 {
}

fn (c: Context) handle(): void {
}

fn main(): void {
}
