#!/usr/bin/env node
let raw = "";
process.stdin.setEncoding("utf8");
for await (const chunk of process.stdin) raw += chunk;

const respond = (obj, code = 0) => {
  process.stdout.write(JSON.stringify(obj) + "\n");
  if (code) process.exitCode = code;
};

let input = {};
let denied = false;
try {
  input = raw.trim() ? JSON.parse(raw) : {};
} catch {
  respond({
    permission: "deny",
    user_message: "Blocked by template MCP guard (malformed-hook-input).",
  }, 2);
  denied = true;
}

if (!denied) {
  const toolName = String(
    input?.tool_name ??
    input?.tool ??
    input?.mcp_tool ??
    input?.name ??
    ""
  );

  const highRisk = /(?:delete|drop|destroy|admin|billing|payment|secret|credential|token|prod|production)/i.test(toolName);
  if (highRisk && process.env.CURSOR_HOOK_ALLOW_HIGH_RISK_MCP !== "1") {
    respond({
      permission: "deny",
      user_message: "Blocked by template MCP guard (high-risk-tool). Set CURSOR_HOOK_ALLOW_HIGH_RISK_MCP=1 only for a trusted lane.",
    }, 2);
  } else {
    respond({ permission: "allow" });
  }
}
