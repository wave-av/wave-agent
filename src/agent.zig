const std = @import("std");
// wave-agent — the fx-style WASM-embeddable core. Native HTTP CLI is pending (Zig 0.17 std churn:
// std.process args/env APIs moved; the WASM surface below is the shipped deliverable).
pub export fn budgetOk(reasoning_tokens: i32, max_tokens: i32) bool {
    const min: i32 = 1024;
    const minHeadroom: i32 = 1024;
    if (max_tokens <= 0 or max_tokens < min) return false;
    return (max_tokens - reasoning_tokens) >= minHeadroom;
}
pub export fn isCanonical(ptr: [*]const u8, len: usize) bool {
    if (len < 3) return false;
    if (ptr[0] == 'w' and ptr[1] == 'a' and ptr[2] == '_') return true;
    var i: usize = 0;
    while (i < len) : (i += 1) {
        const c = ptr[i];
        if (c == '-') continue;
        if (!(c >= '0' and c <= '9') and !(c >= 'a' and c <= 'f')) return false;
    }
    return true;
}
pub export fn headroomOf(reasoning_tokens: i32, max_tokens: i32) i32 { return max_tokens - reasoning_tokens; }
