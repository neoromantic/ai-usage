// Settings → AI Usage: this machine's ai-usage and what to do with it, and
// the other machines BB has, each with its own Install.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { toast } from "sonner";
import { useRealtime, useRpc } from "@get-bb/plugin-sdk/app";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import type { HostView, JobView, Phase, rpcContract } from "../src/rpc";
import { Banner, Spinner } from "./bits";
import { useAiUsage, useExample } from "./use-ai-usage";

function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause);
}

const PHASE_WORDS: Record<Phase, string> = {
  loading: "checking…",
  "no-host": "not in BB",
  offline: "offline",
  unsupported: "not supported",
  absent: "no ai-usage",
  installing: "installing…",
  "install-failed": "install failed",
  "needs-first-run": "not collected yet",
  ready: "collecting",
  "schema-mismatch": "newer ai-usage",
  error: "error",
};

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 py-1.5 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="min-w-0 truncate text-right">{children}</span>
    </div>
  );
}

function macosMajor(v: string | null): number {
  return Number((v ?? "0").split(".")[0]);
}

export function SettingsSection() {
  const { rpc, state } = useAiUsage();
  const example = useExample();
  const [removing, setRemoving] = useState(false);
  const view = state?.view;
  const report = state?.report ?? null;
  const job = state?.job ?? null;
  if (view === undefined) {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Spinner /> Looking for ai-usage…
      </div>
    );
  }
  const has = view.version !== null;
  const running = job?.status === "running";
  const paused = report?.collector.health.find((h) => h.item === "collection")?.status === "unscheduled" || view.schedule === "absent" || view.schedule === "disabled";
  const start = (method: "update" | "appInstall", label: string) =>
    rpc
      .call(method, { hostId: view.hostId })
      .then(() => toast(label))
      .catch((e: unknown) => toast.error(message(e)));
  const canApp = view.platform === "darwin" && !view.root && !view.app && macosMajor(view.macos) >= 14;
  return (
    <div className="space-y-6">
      <div>
        <div className="divide-y divide-border">
          <Row label="Machine">
            {view.hostName ?? "–"}
            {view.user !== null ? <span className="text-muted-foreground"> · {view.root ? "root" : view.user}</span> : null}
          </Row>
          <Row label="ai-usage">{has ? `${view.version} · ${view.method === "homebrew" ? "Homebrew" : "updates itself"}` : PHASE_WORDS[view.phase]}</Row>
          {has ? <Row label="Collecting">{paused ? "paused" : view.phase === "ready" ? "every 15 minutes" : PHASE_WORDS[view.phase]}</Row> : null}
          {report?.collector.relay.url != null ? <Row label="Relay">{report.collector.relay.url}</Row> : null}
          {view.platform === "darwin" ? <Row label="Menu bar app">{view.app ? "installed" : "not installed"}</Row> : null}
        </div>
      </div>
      {job !== null && job.status !== "running" && job.kind !== "collect" && Date.now() - (job.endedAt ?? 0) < 10 * 60_000 ? (
        <Banner tone={job.status === "failed" ? "out" : "quiet"}>{job.message}</Banner>
      ) : null}
      {has ? (
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" disabled={running} onClick={() => void start("update", "Updating ai-usage…")}>
            Update ai-usage
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={running}
            onClick={() => rpc.call("schedule", { hostId: view.hostId, on: paused }).catch((e: unknown) => toast.error(message(e)))}
          >
            {paused ? "Resume collecting" : "Pause collecting"}
          </Button>
          {canApp ? (
            <Button size="sm" variant="outline" disabled={running} onClick={() => void start("appInstall", "Installing the menu bar app…")}>
              Install menu bar app
            </Button>
          ) : null}
          {view.canUninstall ? (
            <Button size="sm" variant="ghost" className="text-destructive-text" disabled={running} onClick={() => setRemoving(true)}>
              Remove ai-usage…
            </Button>
          ) : null}
          {running ? (
            <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Spinner /> {job?.kind}…
            </span>
          ) : null}
        </div>
      ) : (
        <Button size="sm" variant="ghost" onClick={example.show}>
          See an example team
        </Button>
      )}
      <Machines />
      <p className="text-xs text-muted-foreground">
        Removing this plugin leaves ai-usage collecting on every machine it is on. Anything that runs in this BB as you, agents included, can read the team report through
        this plugin and ask it for the team key.
      </p>
      <RemoveDialog open={removing} onOpenChange={setRemoving} view={view} />
    </div>
  );
}

function RemoveDialog({ open, onOpenChange, view }: { open: boolean; onOpenChange: (open: boolean) => void; view: HostView }) {
  const rpc = useRpc<typeof rpcContract>();
  const [forget, setForget] = useState(true);
  const [purge, setPurge] = useState(false);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Remove ai-usage from {view.hostName ?? "this machine"}</DialogTitle>
          <DialogDescription>
            Stops collecting and removes the schedule{view.method === "homebrew" ? "; Homebrew's copy stays, and the result says how to remove it" : ", the menu bar app, and the binary"}.
          </DialogDescription>
        </DialogHeader>
        <label className="flex items-center gap-2 text-sm">
          <Checkbox checked={forget} onCheckedChange={(v) => setForget(v === true)} /> Drop this machine from the team
        </label>
        <label className="flex items-center gap-2 text-sm">
          <Checkbox checked={purge} onCheckedChange={(v) => setPurge(v === true)} /> Also delete its history and the team key
        </label>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={() => {
              rpc
                .call("uninstall", { hostId: view.hostId, forget: forget || purge, purge })
                .then(() => {
                  onOpenChange(false);
                  toast("Removing ai-usage…");
                })
                .catch((e: unknown) => toast.error(message(e)));
            }}
          >
            Remove
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

interface MachineLine {
  id: string;
  name: string;
  primary: boolean;
  connected: boolean;
  phase: Phase;
  platform: string | null;
  user: string | null;
  root: boolean;
  version: string | null;
  job: JobView | null;
}

function Machines() {
  const rpc = useRpc<typeof rpcContract>();
  const [machines, setMachines] = useState<MachineLine[] | null>(null);
  const load = useCallback(() => {
    rpc.call("machines").then((r) => setMachines(r.machines), () => setMachines([]));
  }, [rpc]);
  useEffect(() => load(), [load]);
  useRealtime("job", (p) => {
    const status = (p as { status?: string } | null)?.status;
    if (status !== "running") load();
  });
  if (machines === null || machines.length <= 1) return null;
  return (
    <div>
      <h3 className="text-sm font-medium">BB machines</h3>
      <p className="mt-0.5 text-xs text-muted-foreground">Install puts ai-usage on another machine, in this machine's team. Each machine is a device of the team.</p>
      <ul className="mt-2 divide-y divide-border">
        {machines.map((m) => (
          <li key={m.id} className="flex items-center justify-between gap-3 py-2 text-sm">
            <div className="min-w-0">
              <span className="font-medium">{m.name}</span>
              {m.primary ? <span className="text-muted-foreground"> · runs BB</span> : null}
              <div className="text-xs text-muted-foreground">
                {[m.platform, m.root ? "root" : m.user, m.version ?? PHASE_WORDS[m.phase]].filter((x) => x !== null).join(" · ")}
              </div>
            </div>
            {(m.phase === "absent" || m.phase === "install-failed") && m.connected ? (
              <Button
                size="sm"
                variant="outline"
                onClick={() =>
                  rpc
                    .call("install", { hostId: m.id, teamKey: null, relay: null, copyTeam: true })
                    .then(() => {
                      toast(`Installing ai-usage on ${m.name}…`);
                      load();
                    })
                    .catch((e: unknown) => toast.error(message(e)))
                }
              >
                Install
              </Button>
            ) : m.phase === "installing" ? (
              <Spinner />
            ) : (
              <span className="text-xs text-muted-foreground">{m.phase === "loading" && m.version !== null ? "installed" : PHASE_WORDS[m.phase]}</span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
