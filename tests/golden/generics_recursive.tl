interface Node<T> {
    next: Node<T> | null;
}

interface A<T> {
    b: B<T> | null;
}

interface B<T> {
    a: A<T> | null;
}

fn main(): void {
    let n: Node<int64> = new Node<int64>();
    let a: A<int64> = new A<int64>();
    let b: B<int64> = new B<int64>();
}
