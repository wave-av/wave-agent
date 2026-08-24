const std = @import("std");
// wave-agent — the fx-style WASM-embeddable core. Native HTTP CLI is pending (Zig 0.17 std churn:
// std.process args/env APIs moved; the WASM surface below is the shipped deliverable).
export fn budgetOk(reasoning_tokens: i32, max_tokens: i32) bool {
    const min: i64 = 1024;
    const minHeadroom: i64 = 1024;
    const max: i64 = max_tokens;
    const reasoning: i64 = if (reasoning_tokens < 0) 0 else reasoning_tokens;
    if (max <= 0 or max < min or reasoning > max) return false;
    return (max - reasoning) >= minHeadroom;
}
export fn isCanonical(ptr: [*]const u8, len: usize) bool {
    if (len < 3) return false;
    if (ptr[0] == 'w' and ptr[1] == 'a' and ptr[2] == '_') return true;
    var i: usize = 0;
    var hex_count: usize = 0;
    while (i < len) : (i += 1) {
        const c = ptr[i];
        if (c == '-') continue;
        if (!(c >= '0' and c <= '9') and !(c >= 'a' and c <= 'f')) return false;
        hex_count += 1;
    }
    return hex_count == 32;
}
export fn headroomOf(reasoning_tokens: i32, max_tokens: i32) i32 {
    const max: i64 = max_tokens;
    const reasoning: i64 = if (reasoning_tokens < 0) 0 else reasoning_tokens;
    if (max <= 0 or reasoning > max) return 0;
    const headroom = max - reasoning;
    return if (headroom > std.math.maxInt(i32)) std.math.maxInt(i32) else @intCast(headroom);
}
