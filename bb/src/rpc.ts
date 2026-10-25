// The RPC the plugin's windows call. app.tsx imports only its type.
import { defineRpcContract } from "@get-bb/plugin-sdk";
import { z } from "zod";

export const PHASES = [
  "loading",
  "no-host",
  "offline",
  "unsupported",
  "absent",
  "installing",
  "install-failed",
  "needs-first-run",
  "ready",
  "schema-mismatch",
  "error",
] as const;
export type Phase = (typeof PHASES)[number];

export const JOB_KINDS = ["install", "collect", "join", "update", "schedule-on", "schedule-off", "app", "uninstall"] as const;
export type JobKind = (typeof JOB_KINDS)[number];

export const jobSchema = z.object({
  id: z.string(),
  hostId: z.string(),
  kind: z.enum(JOB_KINDS),
  status: z.enum(["running", "done", "failed"]),
  startedAt: z.number(),
  endedAt: z.number().nullable(),
  message: z.string(),
  log: z.array(z.string()),
});
export type JobView = z.infer<typeof jobSchema>;

export const viewSchema = z.object({
  hostId: z.string().nullable(),
  hostName: z.string().nullable(),
  phase: z.enum(PHASES),
  platform: z.string().nullable(),
  macos: z.string().nullable(),
  root: z.boolean(),
  user: z.string().nullable(),
  version: z.string().nullable(),
  /** `self` or `homebrew`, as the binary says. */
  method: z.string().nullable(),
  /** The scheduler entry's state, as status --json says it. */
  schedule: z.string().nullable(),
  canUninstall: z.boolean(),
  app: z.boolean(),
  scheduleEntry: z.boolean(),
  /** The machine would install ai-usage by itself, or did. */
  auto: z.boolean(),
  error: z.string().nullable(),
});
export type HostView = z.infer<typeof viewSchema>;

const hostInput = z.object({ hostId: z.string().min(1).max(200).nullable() }).strict();

export const rpcContract = defineRpcContract({
  state: {
    input: z.object({ hostId: z.string().min(1).max(200).nullable(), hash: z.string().max(80).nullable() }).strict(),
    output: z.object({
      view: viewSchema,
      job: jobSchema.nullable(),
      /** The report, read as src/report.ts reads it; null when the window already has this hash. */
      report: z.unknown().nullable(),
      hash: z.string().nullable(),
      reportAt: z.number().nullable(),
      refreshing: z.boolean(),
    }),
  },
  machines: {
    input: z.null(),
    output: z.object({
      machines: z.array(
        z.object({
          id: z.string(),
          name: z.string(),
          primary: z.boolean(),
          connected: z.boolean(),
          phase: z.enum(PHASES),
          platform: z.string().nullable(),
          user: z.string().nullable(),
          root: z.boolean(),
          version: z.string().nullable(),
          /** A schedule of ai-usage whose binary the plugin cannot find: no Install. */
          scheduleEntry: z.boolean(),
          job: jobSchema.nullable(),
        }),
      ),
    }),
  },
  demo: { input: z.null(), output: z.object({ report: z.unknown() }) },
  job: { input: z.object({ id: z.string().max(64) }).strict(), output: z.object({ job: jobSchema.nullable() }) },
  collect: { input: hostInput, output: z.object({ jobId: z.string() }) },
  install: {
    input: z
      .object({
        hostId: z.string().min(1).max(200).nullable(),
        teamKey: z.string().max(4096).nullable(),
        relay: z.string().max(2048).nullable(),
        /** Join the team of a machine that already runs ai-usage, as the machine list does. */
        copyTeam: z.boolean(),
      })
      .strict(),
    output: z.object({ jobId: z.string() }),
  },
  join: {
    input: z
      .object({ hostId: z.string().min(1).max(200).nullable(), key: z.string().min(1).max(4096), relay: z.string().max(2048).nullable() })
      .strict(),
    output: z.object({ jobId: z.string() }),
  },
  invite: { input: hostInput, output: z.object({ message: z.string() }) },
  update: { input: hostInput, output: z.object({ jobId: z.string() }) },
  schedule: {
    input: z.object({ hostId: z.string().min(1).max(200).nullable(), on: z.boolean() }).strict(),
    output: z.object({ jobId: z.string() }),
  },
  appInstall: { input: hostInput, output: z.object({ jobId: z.string() }) },
  uninstall: {
    input: z.object({ hostId: z.string().min(1).max(200).nullable(), forget: z.boolean(), purge: z.boolean() }).strict(),
    output: z.object({ jobId: z.string() }),
  },
  cost: {
    input: z.object({ hostId: z.string().min(1).max(200).nullable(), args: z.array(z.string().max(400)).min(3).max(8) }).strict(),
    output: z.object({ ok: z.boolean(), message: z.string() }),
  },
});
