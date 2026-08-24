#!/usr/bin/env node
import { appendFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";

let raw = "";
process.stdin.setEncoding("utf8");
for await (const chunk of process.stdin) raw += chunk;

const input = raw.trim() ? JSON.parse(raw) : {};
if (process.env.CURSOR_HOOK_LANE_LOG === "1") {
  const root = Array.isArray(input?.workspace_roots) && input.workspace_roots[0] ? input.workspace_roots[0] : process.cwd();
  const logPath = join(root, ".cursor", "hooks", "lane.log");
  mkdirSync(dirname(logPath), { recursive: true });
  appendFileSync(logPath, JSON.stringify({
    ts: new Date().toISOString(),
    event: input?.hook_event_name ?? "unknown",
    conversation_id: input?.conversation_id ?? null,
    model_id: input?.model_id ?? null,
  }) + "\n");
}

process.stdout.write(JSON.stringify({}) + "\n");
