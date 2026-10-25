// Long operations run as jobs on the server: an RPC starts one and returns
// its id at once, and windows follow it over realtime. One job runs on a
// machine at a time, since each of them writes its ai-usage state.
import { randomUUID } from "node:crypto";
import type { JobKind, JobView } from "./rpc.js";

const MAX_LOG = 400;
const KEEP = 50;

export class BusyError extends Error {
  constructor(readonly job: JobView) {
    super(`ai-usage is busy on this machine: ${job.kind} is running`);
  }
}

export interface JobEvents {
  changed(job: JobView): void;
  line(job: JobView, line: string): void;
}

export class Jobs {
  private readonly jobs = new Map<string, JobView>();

  constructor(private readonly events: JobEvents, private readonly now: () => number = Date.now) {}

  get(id: string): JobView | null {
    return this.jobs.get(id) ?? null;
  }

  /** The machine's running job, else its newest. */
  latest(hostId: string): JobView | null {
    let newest: JobView | null = null;
    for (const job of this.jobs.values()) {
      if (job.hostId !== hostId) continue;
      if (job.status === "running") return job;
      if (newest === null || job.startedAt >= newest.startedAt) newest = job;
    }
    return newest;
  }

  running(hostId: string): JobView | null {
    for (const job of this.jobs.values()) if (job.hostId === hostId && job.status === "running") return job;
    return null;
  }

  /**
   * Starts work as a job on a machine. A request for the job that already
   * runs there joins it; another kind of job waits for its end.
   */
  start(hostId: string, kind: JobKind, work: (job: JobView, log: (line: string) => void) => Promise<string>): JobView {
    const busy = this.running(hostId);
    if (busy !== null) {
      if (busy.kind === kind) return busy;
      throw new BusyError(busy);
    }
    const job: JobView = { id: randomUUID(), hostId, kind, status: "running", startedAt: this.now(), endedAt: null, message: "", log: [] };
    this.jobs.set(job.id, job);
    this.prune();
    this.events.changed(job);
    const log = (line: string) => this.append(job.id, line);
    void work(job, log).then(
      (message) => this.finish(job.id, "done", message),
      (error: unknown) => this.finish(job.id, "failed", error instanceof Error ? error.message : String(error)),
    );
    return job;
  }

  append(id: string, line: string): void {
    const job = this.jobs.get(id);
    if (job === undefined || job.status !== "running") return;
    job.log.push(line);
    if (job.log.length > MAX_LOG) job.log.splice(0, job.log.length - MAX_LOG);
    this.events.line(job, line);
  }

  private finish(id: string, status: "done" | "failed", message: string): void {
    const job = this.jobs.get(id);
    if (job === undefined) return;
    job.status = status;
    job.message = message;
    job.endedAt = this.now();
    this.events.changed(job);
  }

  private prune(): void {
    if (this.jobs.size <= KEEP) return;
    const done = [...this.jobs.values()].filter((j) => j.status !== "running").sort((a, b) => a.startedAt - b.startedAt);
    for (const job of done.slice(0, this.jobs.size - KEEP)) this.jobs.delete(job.id);
  }
}
