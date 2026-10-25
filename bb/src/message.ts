// What ai-usage said went wrong, read the way the menu bar app reads it
// (macos/Sources/AIUsageKit/CLI.swift): the last "ai-usage: " line of
// standard error and the paragraph it starts, without the lines that say it
// waited for another run, else standard output when it said why there.

const WAITING = ["ai-usage: waiting for another run", "ai-usage: another run"];

export function errorMessage(stderr: string, stdout = ""): string {
  let text = stderr.trim();
  const lines = text.split("\n");
  let own = -1;
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = lines[i]!;
    if (line.startsWith("ai-usage: ") && !WAITING.some((w) => line.startsWith(w))) {
      own = i;
      break;
    }
  }
  if (own >= 0) {
    text = lines.slice(own).join("\n");
  } else if (stdout.trim() !== "") {
    return stdout.trim();
  }
  const paragraph = text.split("\n\n")[0] ?? "";
  return paragraph
    .split("\n")
    .filter((line) => !WAITING.some((w) => line.startsWith(w)))
    .map((line) => (line.startsWith("ai-usage: ") ? line.slice("ai-usage: ".length) : line))
    .join("\n")
    .trim();
}

const KEY = /aiu-team-\d+:[A-Za-z0-9+/=_-]+/g;

/** Text with any team key in it replaced, for logs and installer lines. */
export function redact(text: string): string {
  return text.replace(KEY, "aiu-team-…");
}
