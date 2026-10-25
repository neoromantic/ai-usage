// The environment every ai-usage run from BB gets. A launch agent or a
// crontab line keeps the PATH of the run that registered it, for good, so
// no run may carry BB's own folders: they would outlive BB in the schedule.
import path from "node:path";

export type Env = Record<string, string>;
type Sanitize = (args: { env: NodeJS.ProcessEnv }) => NodeJS.ProcessEnv;

/** The folders the installer and package managers put ai-usage in. */
export function standardDirs(home: string): string[] {
  return [
    path.join(home, ".local/bin"),
    path.join(home, "bin"),
    "/opt/homebrew/bin",
    "/usr/local/bin",
    "/home/linuxbrew/.linuxbrew/bin",
  ];
}

/** A PATH entry that is BB's own: the folder of BB's CLI, or its host daemon's. */
export function isBbDir(dir: string, bbCli: string | undefined): boolean {
  if (bbCli !== undefined && bbCli !== "" && dir === path.dirname(bbCli)) return true;
  return dir.replaceAll("\\", "/").includes("bb-app/host-daemon");
}

/**
 * BB's sanitizer first, which drops BB_* and NODE_ENV, then BB's folders off
 * PATH. Pool variables stay: ai-usage drops them from its quota probes
 * itself and reads them to mark the device as pooled.
 */
export function cleanEnv(env: NodeJS.ProcessEnv, sanitize: Sanitize): Env {
  const bbCli = env.BB_CLI;
  const out: Env = {};
  for (const [key, value] of Object.entries(sanitize({ env }))) {
    if (value !== undefined) out[key] = value;
  }
  const seen = new Set<string>();
  const dirs: string[] = [];
  for (const dir of (out.PATH ?? "").split(path.delimiter)) {
    if (dir === "" || seen.has(dir) || isBbDir(dir, bbCli)) continue;
    seen.add(dir);
    dirs.push(dir);
  }
  out.PATH = (dirs.length > 0 ? dirs : ["/usr/bin", "/bin", "/usr/sbin", "/sbin"]).join(path.delimiter);
  return out;
}

/** Where to look for ai-usage, in order: PATH, then the standard folders. */
export function candidates(env: Env, dirs: string[], exe = "ai-usage"): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const dir of [...(env.PATH ?? "").split(path.delimiter), ...dirs]) {
    if (dir === "" || !path.isAbsolute(dir)) continue;
    const file = path.join(dir, exe);
    if (seen.has(file)) continue;
    seen.add(file);
    out.push(file);
  }
  return out;
}
