// The contract between the server entry and the host entry, which runs on
// each BB machine. The host runs ai-usage and its installer; it has no
// generic exec: every argv it accepts is checked against src/args.ts.
import {
  defineRpcContract,
  type ExperimentalHostSignals,
} from "@get-bb/plugin-sdk";
import { z } from "zod";

export const probeOutput = z
  .object({
    platform: z.string(),
    arch: z.string(),
    uid: z.number().nullable(),
    user: z.string(),
    /** macOS's product version, as "15.1"; null elsewhere. */
    macos: z.string().nullable(),
    /** The ai-usage binary to run: the one the scheduler runs when status --json names it. */
    bin: z.string().nullable(),
    /** The state folder the scheduler's entry names, for AI_USAGE_HOME. */
    aiHome: z.string().nullable(),
    version: z.string().nullable(),
    /** What status --json printed, from a release that creates nothing before its first run. */
    status: z.unknown().nullable(),
    /** The binary knows `ai-usage uninstall`. */
    canUninstall: z.boolean(),
    /** A launch agent or crontab line of ai-usage exists although no binary was found. */
    scheduleEntry: z.boolean(),
    /** The menu bar app is in ~/Applications or /Applications. */
    app: z.boolean(),
    error: z.string().nullable(),
  })
  .strict();
export type Probe = z.infer<typeof probeOutput>;

export const runInput = z
  .object({
    bin: z.string().min(1),
    aiHome: z.string().nullable(),
    args: z.array(z.string().max(512)).min(1).max(12),
    /** Written to standard input, as the team key for `team join`. */
    stdin: z.string().max(4096).nullable(),
    /** Parse standard output as JSON and return it as `json`. */
    json: z.boolean(),
    timeoutMs: z.number().int().min(1000).max(30 * 60 * 1000),
  })
  .strict();
export type RunInput = z.infer<typeof runInput>;

export const runOutput = z
  .object({
    code: z.number().nullable(),
    timedOut: z.boolean(),
    /** Standard output, when it is not JSON. */
    stdout: z.string(),
    json: z.unknown().nullable(),
    /** The error ai-usage gave, as the menu bar app reads it. */
    message: z.string(),
  })
  .strict();
export type RunOutput = z.infer<typeof runOutput>;

export const installInput = z
  .object({
    jobId: z.string().min(1).max(64),
    teamKey: z.string().max(4096).nullable(),
    relay: z.string().max(2048).nullable(),
  })
  .strict();

export const installOutput = z
  .object({
    ok: z.boolean(),
    code: z.number().nullable(),
    tag: z.string().nullable(),
    message: z.string(),
  })
  .strict();
export type InstallOutput = z.infer<typeof installOutput>;

export const relayInput = z.object({ bin: z.string().min(1), aiHome: z.string().nullable() }).strict();
export const relayOutput = z
  .object({ configured: z.string().nullable(), builtIn: z.string().nullable() })
  .strict();

export const hostContract = defineRpcContract({
  probe: { input: z.object({}).strict(), output: probeOutput },
  run: { input: runInput, output: runOutput },
  relay: { input: relayInput, output: relayOutput },
  install: { input: installInput, output: installOutput },
});

export const hostSignals = {
  installLog: {
    payload: z
      .object({ jobId: z.string(), line: z.string().max(4096) })
      .strict(),
  },
} satisfies ExperimentalHostSignals;
