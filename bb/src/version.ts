/** The oldest ai-usage the panel reads in full: the first release with `cost`. */
export const MIN_AI_USAGE = "v0.10.0";
/** The first release whose status --json creates nothing before its first run. */
export const STATUS_SAFE = "v0.11.0";
/** The report schema the plugin reads, as the menu bar app does. */
export const SCHEMA_VERSION = 4;

/** A release as numbers, "v0.10.2" as [0, 10, 2]; null for a build from source. */
export function parseRelease(v: string | null | undefined): number[] | null {
  const m = /^v?(\d+)\.(\d+)\.(\d+)$/.exec((v ?? "").trim());
  return m === null ? null : [Number(m[1]), Number(m[2]), Number(m[3])];
}

/** At least `min`. A build from source, `devel` or `dev`, is newer than any release. */
export function atLeast(v: string | null | undefined, min: string): boolean {
  if (v === null || v === undefined || v === "") return false;
  const have = parseRelease(v);
  const want = parseRelease(min);
  if (have === null) return v === "devel" || v === "dev";
  if (want === null) return false;
  for (let i = 0; i < 3; i++) {
    if (have[i]! !== want[i]!) return have[i]! > want[i]!;
  }
  return true;
}
