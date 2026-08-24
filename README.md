# wave-agent — the fx-style WASM-embeddable core

Tiny Zig core over the WAVE pool. Compiles to WebAssembly exposing the law-gates:
- `budgetOk(reasoning_tokens, max_tokens)` — token-budget-conservation
- `isCanonical(ptr, len)` — gauge-invariance (registry-id check)
- `headroomOf(reasoning_tokens, max_tokens)`

Build (Zig 0.16+): `zig build-obj src/agent.zig -target wasm32-freestanding -fno-entry -femit-bin=agent.wasm`
The native HTTP CLI (POST to the pool) is the next step (Zig 0.17 std churn pending).
