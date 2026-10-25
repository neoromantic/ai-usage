// The ai-usage commands the plugin runs, and nothing else. The host checks
// every argv here before it starts the binary.

const COST_TARGET = /^(claude|codex|grok):[^\s]{1,320}$/;
const DAY = /^(\d{4}-\d{2}-\d{2}|\d{1,2})$/;
const PRICE = /^\d{1,6}(\.\d{1,2})?$/;

function costSet(rest: string[]): boolean {
  let i = 0;
  while (i < rest.length) {
    const flag = rest[i];
    if (flag === "--monthly" || flag === "--yearly") {
      i += 1;
      continue;
    }
    const value = rest[i + 1];
    if (value === undefined) return false;
    if (flag === "--price" && PRICE.test(value)) i += 2;
    else if (flag === "--renews" && DAY.test(value)) i += 2;
    else return false;
  }
  return rest.length > 0;
}

export function allowed(args: readonly string[]): boolean {
  const [a, b, c, ...rest] = args;
  const exact = (...want: string[]) =>
    args.length === want.length && want.every((w, i) => args[i] === w);
  if (
    exact("version") ||
    exact("--help") ||
    exact("report", "--json") ||
    exact("collect", "--json") ||
    exact("status", "--json") ||
    exact("team", "key") ||
    exact("team", "join") ||
    exact("relay", "show") ||
    exact("update") ||
    exact("schedule", "install") ||
    exact("schedule", "remove") ||
    exact("app", "install")
  ) {
    return true;
  }
  if (a === "relay" && b === "set" && c !== undefined && rest.length === 0) {
    return /^https?:\/\/[^\s]{1,2000}$/.test(c);
  }
  if (a === "uninstall") {
    const flags = args.slice(1);
    return flags.length <= 2 && flags.every((f) => f === "--forget" || f === "--purge") && new Set(flags).size === flags.length;
  }
  if (a === "cost" && c !== undefined && COST_TARGET.test(c)) {
    if ((b === "confirm" || b === "cancel") && rest.length === 0) return true;
    if (b === "skip") return rest.length === 1 && ["renewal", "price", "still_paid"].includes(rest[0] ?? "");
    if (b === "set") return costSet(rest);
  }
  return false;
}
