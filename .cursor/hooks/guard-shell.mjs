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
    user_message: "Blocked by template shell guard (malformed-hook-input).",
  }, 2);
  denied = true;
}

if (!denied) {
  const command =
    input?.tool_input?.command ??
    input?.tool_input?.cmd ??
    input?.command ??
    input?.input?.command ??
    "";

  const denyPatterns = [
    ["root-delete", /\brm\s+-rf\s+(?:\/|\$HOME|~)(?:\s|$)/i],
    ["sudo", /\bsudo\b/i],
    ["prod-infra", /\b(?:kubectl|helm|terraform|pulumi)\b[^\n]*(?:prod|production|apply|destroy)/i],
    ["prod-secrets", /\b(?:stripe|doppler|vercel|aws|gcloud|az)\b[^\n]*(?:prod|production|live|secret|token|credential)/i],
    ["force-push", /\bgit\s+push\b[^\n]*--force/i],
    ["pipe-to-shell", /\b(?:curl|wget)\b[^\n|]*\|\s*(?:bash|sh|zsh)/i],
    ["package-publish", /\b(?:npm|pnpm|yarn|bun)\s+(?:publish|release)\b/i],
    ["docker-prune", /\bdocker\s+(?:system\s+prune|rm\s+-f)/i],
  ];

  const hit = denyPatterns.find(([, re]) => re.test(command));
  if (hit) {
    respond({
      permission: "deny",
      user_message: `Blocked by template shell guard (${hit[0]}). Review the command or adjust .cursor/hooks/guard-shell.mjs.`,
    }, 2);
  } else {
    respond({ permission: "allow" });
  }
}
