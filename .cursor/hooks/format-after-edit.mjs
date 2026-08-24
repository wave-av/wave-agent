#!/usr/bin/env node
let raw = "";
process.stdin.setEncoding("utf8");
for await (const chunk of process.stdin) raw += chunk;

// Template intentionally no-ops by default. Wire a formatter here only if it is
// fast, local, deterministic, and safe to run after every edit.
process.stdout.write(JSON.stringify({}) + "\n");
