function quote(s: string) {
  return `'${s.replaceAll("'", `'\\''`)}'`;
}

export function inviteMessage(
  key: string,
  relay: { configured: string | null; builtIn: string | null },
) {
  const custom =
    relay.configured !== null && relay.configured !== relay.builtIn ? relay.configured : null;
  const env = [
    custom === null ? null : `AI_USAGE_RELAY=${quote(custom)}`,
    `AI_USAGE_TEAM_KEY=${quote(key)}`,
  ]
    .filter((x) => x !== null)
    .join(" ");
  return [
    "Join my AI Usage team: it shows what is left of our Claude Code, Codex and Grok subscriptions, and who uses them.",
    "",
    "In BB: install the AI Usage plugin, open AI Usage, and paste this key under Join a team" +
      (custom === null ? "." : `, with this relay: ${custom}`),
    "In a terminal on macOS or Linux:",
    `  curl -fsSL https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh | ${env} sh`,
    "",
    "The key works like a password and cannot be revoked:",
    key,
  ].join("\n");
}
