import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const wasm = readFileSync(new URL("../agent.wasm", import.meta.url));

async function instantiate() {
  const mem = new WebAssembly.Memory({ initial: 2 });
  const env = {
    memory: mem,
    __indirect_function_table: new WebAssembly.Table({ initial: 0, element: "anyfunc" }),
    __memory_base: new WebAssembly.Global({ value: "i32", mutable: false }, 0),
    __table_base: new WebAssembly.Global({ value: "i32", mutable: false }, 0),
  };
  const { instance } = await WebAssembly.instantiate(wasm, { env });
  return { exports: instance.exports, mem };
}

function canon(x, s) {
  const arr = new Uint8Array(x.mem.buffer);
  for (let i = 0; i < s.length; i++) arr[i] = s.charCodeAt(i);
  return Boolean(x.exports.isCanonical(0, s.length));
}

test("budgetOk: reasoning within headroom passes", async () => { const x = await instantiate(); assert.equal(x.exports.budgetOk(100, 4000), 1); });
test("budgetOk: reasoning exhausting headroom fails", async () => { const x = await instantiate(); assert.equal(x.exports.budgetOk(14979, 4000), 0); });
test("budgetOk: below min cap fails", async () => { const x = await instantiate(); assert.equal(x.exports.budgetOk(0, 512), 0); });
test("isCanonical: wa_ id passes", async () => { const x = await instantiate(); assert.equal(canon(x, "wa_a1b2c3"), true); });
test("isCanonical: uuid passes", async () => { const x = await instantiate(); assert.equal(canon(x, "123e4567-e89b-12d3-a456-426614174000"), true); });
test("isCanonical: raw vendor slug fails", async () => { const x = await instantiate(); assert.equal(canon(x, "claude-haiku-4-5"), false); });
test("headroomOf: returns the delta", async () => { const x = await instantiate(); assert.equal(x.exports.headroomOf(100, 4000), 3900); });
