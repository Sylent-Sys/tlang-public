interface Max {
    f0: int64;
    f1: string;
    f2: bool;
    f3: float64 | null;
    f4: int32;
    f5: int64;
    f6: string;
    f7: bool;
    f8: float64 | null;
    f9: int32;
    f10: int64;
    f11: string;
    f12: bool;
    f13: float64 | null;
    f14: int32;
    f15: int64;
    f16: string;
    f17: bool;
    f18: float64 | null;
    f19: int32;
    f20: int64;
    f21: string;
    f22: bool;
    f23: float64 | null;
    f24: int32;
    f25: int64;
    f26: string;
    f27: bool;
    f28: float64 | null;
    f29: int32;
    f30: int64;
    f31: string;
    f32: bool;
    f33: float64 | null;
    f34: int32;
    f35: int64;
    f36: string;
    f37: bool;
    f38: float64 | null;
    f39: int32;
    f40: int64;
    f41: string;
    f42: bool;
    f43: float64 | null;
    f44: int32;
    f45: int64;
    f46: string;
    f47: bool;
    f48: float64 | null;
    f49: int32;
    f50: int64;
    f51: string;
    f52: bool;
    f53: float64 | null;
    f54: int32;
    f55: int64;
    f56: string;
    f57: bool;
    f58: float64 | null;
    f59: int32;
    f60: int64;
    f61: string;
    f62: bool;
    f63: float64 | null;
}

fn route_dispatcher(ctx: Context): void {
    let m = new Max();
    if (ctx.bindJson(m)) {
        ctx.json(200, m);
    }
}
