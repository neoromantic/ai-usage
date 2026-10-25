// The team a machine joins when it is installed from another machine's
// Settings: the key and relay of the first machine that runs ai-usage and
// has collected, read into memory only.

export interface TeamSource {
  key: string;
  relay: string | null;
}

export interface SourceMachine {
  /** The machine has an ai-usage binary; false when it has none or did not answer. */
  hasAiUsage(id: string): Promise<boolean>;
  /** Its report reads, after reading it again when the cache is cold. */
  collected(id: string): Promise<boolean>;
  key(id: string): Promise<string>;
  relay(id: string): Promise<{ configured: string | null; builtIn: string | null }>;
}

export class NoTeamError extends Error {}

/**
 * The first machine in order that gives its key, or null when no machine
 * runs ai-usage, so the install starts a team of its own. A machine that
 * runs ai-usage but gives no key is an error, never a new team in silence.
 */
export async function teamSource(order: string[], m: SourceMachine): Promise<TeamSource | null> {
  let found = false;
  let why = "";
  for (const id of order) {
    if (!(await m.hasAiUsage(id))) continue;
    found = true;
    try {
      if (!(await m.collected(id))) {
        why = "it has not collected yet";
        continue;
      }
      const key = (await m.key(id)).trim();
      if (!key.startsWith("aiu-team-")) {
        why = "it gave no team key";
        continue;
      }
      const relay = await m.relay(id);
      return { key, relay: relay.configured !== null && relay.configured !== relay.builtIn ? relay.configured : null };
    } catch (error) {
      why = error instanceof Error ? error.message : String(error);
    }
  }
  if (found) throw new NoTeamError(`Could not read the team of a machine that runs ai-usage (${why}); install with its team key from the AI Usage page instead.`);
  return null;
}
