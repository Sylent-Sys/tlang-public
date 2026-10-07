interface a { v: int64; }
interface User { id: int64; }
interface m_User<T> { w: T; }

fn (u: User) a(): void {}

fn main(): void {
    let x = new m_User<a>();
}
