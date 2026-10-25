// What the panel shows for a machine, from what the server knows of it.
import type { Probe } from "./contract.js";
import type { JobView, Phase } from "./rpc.js";

export interface HostFacts {
  /** BB's record of the machine; null when BB has no such machine. */
  host: { status: "connected" | "disconnected"; type: "persistent" | "ephemeral" } | null;
  /** The platform BB reports for its primary machine, which tells WSL apart. */
  bbPlatform: string | null;
  probe: Probe | null;
  job: JobView | null;
  report: {
    ok: boolean;
    schemaMismatch: boolean;
    nothingCollected: boolean;
    error: string | null;
  } | null;
}

export const NOTHING_COLLECTED = "nothing collected yet";

function collected(probe: Probe): boolean | null {
  const install = (probe.status as { install?: { collected?: unknown } } | null)?.install;
  return typeof install?.collected === "boolean" ? install.collected : null;
}

export function phaseOf(f: HostFacts): Phase {
  if (f.host === null) return "no-host";
  if (f.job?.kind === "install" && f.job.status === "running") return "installing";
  if (f.host.status === "disconnected") return "offline";
  if (f.probe === null) return "loading";
  if (f.probe.platform === "win32") return "unsupported";
  if (f.probe.bin === null) return f.job?.kind === "install" && f.job.status === "failed" ? "install-failed" : "absent";
  if (f.report?.schemaMismatch === true) return "schema-mismatch";
  if (f.report?.ok === true) return "ready";
  if (f.report?.nothingCollected === true || collected(f.probe) === false) return "needs-first-run";
  if (f.report?.error != null || f.probe.error !== null) return "error";
  return "loading";
}

/**
 * ai-usage installs itself on a machine when nobody has to answer anything
 * for it: a connected, persistent macOS or Linux machine where BB runs as a
 * person, not root, with no ai-usage and no schedule of one, that has not
 * been set up from this plugin before.
 */
export function autoInstallable(f: HostFacts, recorded: boolean): boolean {
  const p = f.probe;
  return (
    !recorded &&
    f.host !== null &&
    f.host.status === "connected" &&
    f.host.type === "persistent" &&
    f.bbPlatform !== "wsl" &&
    f.bbPlatform !== "win32" &&
    p !== null &&
    (p.platform === "darwin" || p.platform === "linux") &&
    p.uid !== 0 &&
    p.bin === null &&
    !p.scheduleEntry
  );
}
