// The AI Usage page: the team report of the machine BB runs on, or what it
// takes to get one there.
import { useState, type ReactNode } from "react";
import { toast } from "sonner";
import { experimental_copyToClipboard, useRpc } from "@get-bb/plugin-sdk/app";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { ago } from "../src/format";
import type { Report } from "../src/report";
import type { HostView, JobView, rpcContract } from "../src/rpc";
import { atLeast, MIN_AI_USAGE } from "../src/version";
import { Banner, Dot, Log, Page, Spinner } from "./bits";
import { ReportView } from "./report-view";
import { EXAMPLE, useAiUsage, useDemoReport, useExample, useNow, type AiUsageState } from "./use-ai-usage";

const DEMO_ZONE = "Europe/Berlin";

function machine(view: HostView): string {
  return view.hostName ?? "this machine";
}

function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause);
}

export function PanelHeader({ subPath }: { subPath: string }) {
  const demo = subPath === EXAMPLE;
  const example = useExample();
  const { rpc, state } = useAiUsage();
  const now = useNow();
  if (demo) {
    return (
      <div className="flex items-center gap-2 text-xs">
        <span className="rounded-md bg-muted px-2 py-0.5 text-muted-foreground">Example team · made-up data</span>
        <Button size="sm" variant="ghost" onClick={example.leave}>
          Back
        </Button>
      </div>
    );
  }
  const report = state?.report ?? null;
  const ready = state?.view.phase === "ready" || state?.view.phase === "offline" || state?.view.phase === "schema-mismatch";
  if (report === null || !ready) return null;
  const busy = state?.job?.status === "running" && state.job.kind === "collect";
  const pulled = ago(report.team.pulled_at, now);
  return (
    <div className="flex items-center gap-3 text-xs text-muted-foreground">
      <span className="flex items-center gap-1.5">
        {report.collector.health.map((h) => (
          <Dot key={h.item} state={h.state} title={`${h.item}: ${h.status}${h.release !== null ? ` ${h.release}` : ""}`} />
        ))}
      </span>
      {pulled !== null ? <span className="hidden sm:inline">team read {pulled}</span> : null}
      <Button
        size="sm"
        variant="ghost"
        disabled={busy || state?.view.hostId == null}
        onClick={() => {
          rpc.call("collect", { hostId: state!.view.hostId }).catch((e: unknown) => toast.error(message(e)));
        }}
      >
        {busy ? <Spinner /> : null}
        Refresh
      </Button>
    </div>
  );
}

export function PanelPage({ subPath }: { subPath: string }) {
  const demo = subPath === EXAMPLE;
  const demoReport = useDemoReport(demo);
  const { rpc, state, error } = useAiUsage();
  const now = useNow();
  if (demo) {
    return (
      <Page>
        {demoReport === null ? (
          <Quiet>
            <Spinner /> Loading the example…
          </Quiet>
        ) : (
          <ReportView report={demoReport} now={Date.parse(demoReport.generated_at)} timeZone={DEMO_ZONE} />
        )}
      </Page>
    );
  }
  if (state === null) {
    return (
      <Page>
        <Quiet>{error !== null ? error : <><Spinner /> Looking for ai-usage…</>}</Quiet>
      </Page>
    );
  }
  return (
    <Page>
      <Body state={state} now={now} answer={async (args) => {
        try {
          const r = await rpc.call("cost", { hostId: state.view.hostId, args });
          if (r.ok) toast.success(r.message || "Saved");
          else toast.error(r.message);
        } catch (e) {
          toast.error(message(e));
        }
      }} />
    </Page>
  );
}

function Quiet({ children }: { children: ReactNode }) {
  return <div className="flex items-center gap-2 rounded-lg border border-dashed border-border px-4 py-6 text-sm text-muted-foreground">{children}</div>;
}

function Body({ state, now, answer }: { state: AiUsageState; now: number; answer: (args: string[]) => Promise<void> }) {
  const { view, report, job } = state;
  switch (view.phase) {
    case "loading":
      return (
        <Quiet>
          <Spinner /> Looking for ai-usage on {machine(view)}…
        </Quiet>
      );
    case "no-host":
      return <Quiet>BB has no machine to run ai-usage on yet. Connect one under Settings → Machines.</Quiet>;
    case "unsupported":
      return (
        <Quiet>
          ai-usage installs from BB on macOS and Linux. On Windows, install it in PowerShell as the ai-usage README says, then open this page again.
        </Quiet>
      );
    case "absent":
    case "install-failed":
      return <Setup view={view} job={job} />;
    case "installing":
      return (
        <div className="space-y-3">
          <Quiet>
            <Spinner /> Installing ai-usage on {machine(view)}. This takes a minute or two; the first run reads every log once.
          </Quiet>
          <Log lines={job?.log ?? []} />
        </div>
      );
    case "needs-first-run":
      return <FirstRun view={view} job={job} />;
    case "error":
      return <Failed view={view} job={job} />;
    default:
      break;
  }
  return (
    <div className="space-y-6">
      <Banners view={view} report={report} now={now} reportAt={state.reportAt} />
      {report !== null ? (
        <>
          <ReportView report={report} now={now} answer={answer} />
          <TeamCard view={view} report={report} />
        </>
      ) : (
        <Quiet>
          <Spinner /> Reading the report…
        </Quiet>
      )}
    </div>
  );
}

function Banners({ view, report, now, reportAt }: { view: HostView; report: Report | null; now: number; reportAt: number | null }) {
  const rpc = useRpc<typeof rpcContract>();
  const out: ReactNode[] = [];
  if (view.phase === "schema-mismatch") {
    out.push(
      <Banner key="schema" tone="tight">
        ai-usage on {machine(view)} prints a report this plugin does not read. Update AI Usage under Settings → Plugins.
      </Banner>,
    );
  }
  if (view.phase === "offline") {
    out.push(
      <Banner key="offline" tone="tight">
        {machine(view)} is offline{reportAt !== null ? ` · the report is from ${ago(new Date(reportAt).toISOString(), now)}` : ""}
      </Banner>,
    );
  }
  const c = report?.collector;
  if (c !== undefined && view.phase !== "offline") {
    const old = !atLeast(c.version, MIN_AI_USAGE);
    const update = c.health.find((h) => h.item === "update");
    if (old || (update?.status === "available" && c.update.method === "homebrew")) {
      out.push(
        <Banner
          key="update"
          tone={old ? "tight" : "quiet"}
          action={
            <Button size="sm" variant="outline" onClick={() => rpc.call("update", { hostId: view.hostId }).catch((e: unknown) => toast.error(message(e)))}>
              Update
            </Button>
          }
        >
          {old ? `ai-usage ${c.version} is older than this plugin reads in full.` : `ai-usage ${update?.release ?? "a newer release"} is out.`}
        </Banner>,
      );
    }
    const collection = c.health.find((h) => h.item === "collection");
    if (collection?.status === "unscheduled") {
      out.push(
        <Banner
          key="paused"
          tone="tight"
          action={
            <Button size="sm" variant="outline" onClick={() => rpc.call("schedule", { hostId: view.hostId, on: true }).catch((e: unknown) => toast.error(message(e)))}>
              Resume
            </Button>
          }
        >
          Collecting is paused on {machine(view)}: the scheduler does not run ai-usage.
        </Banner>,
      );
    } else if (c.schedule.error !== null) {
      out.push(
        <Banner key="schedule" tone="tight">
          {c.schedule.error}
        </Banner>,
      );
    }
  }
  return out.length > 0 ? <div className="space-y-2">{out}</div> : null;
}

function Facts() {
  return (
    <ul className="space-y-1.5 text-sm text-muted-foreground">
      <li>· ai-usage, an open-source command, goes into ~/.local/bin, and the system scheduler runs it every 15 minutes, with BB open or not. It updates itself from GitHub Releases.</li>
      <li>· It reads the logs Claude Code, Codex, Grok and Hermes keep, and asks those tools what is left of each limit. It spends none of your quota.</li>
      <li>· The team's machines meet on a relay, which sees tools, plans and numbers. Names, emails and project paths are encrypted with the team key.</li>
    </ul>
  );
}

function Setup({ view, job }: { view: HostView; job: JobView | null }) {
  const rpc = useRpc<typeof rpcContract>();
  const example = useExample();
  const [key, setKey] = useState("");
  const [relay, setRelay] = useState("");
  const [showRelay, setShowRelay] = useState(false);
  const [busy, setBusy] = useState(false);
  const failed = view.phase === "install-failed" ? job : null;
  return (
    <div className="space-y-5">
      <div className="space-y-2">
        <h1 className="text-lg font-semibold">See what your team's AI subscriptions have left</h1>
        <p className="text-sm text-muted-foreground">
          AI Usage shows every Claude Code, Codex and Grok subscription your team shares: how much of each limit is left, when it runs out at the current pace, who used it, and
          what it costs.
        </p>
      </div>
      <Facts />
      {failed !== null ? (
        <Banner tone="out">
          <div className="space-y-1">
            <div>The install on {machine(view)} failed: {failed.message}</div>
          </div>
        </Banner>
      ) : null}
      {view.scheduleEntry ? (
        <Banner tone="tight">
          {machine(view)} has a schedule of ai-usage, but its binary is not in PATH, ~/.local/bin, ~/bin or Homebrew. Install puts a new copy in ~/.local/bin.
        </Banner>
      ) : null}
      {view.root ? <Banner>BB runs as root on {machine(view)}: ai-usage installs for root, with root's crontab.</Banner> : null}
      <form
        className="space-y-3 rounded-lg border border-border bg-card p-4"
        onSubmit={(e) => {
          e.preventDefault();
          setBusy(true);
          rpc
            .call("install", { hostId: view.hostId, teamKey: key.trim() === "" ? null : key.trim(), relay: relay.trim() === "" ? null : relay.trim(), copyTeam: false })
            .catch((err: unknown) => toast.error(message(err)))
            .finally(() => setBusy(false));
        }}
      >
        <label className="block space-y-1 text-sm">
          <span>Team key, if a colleague sent you one</span>
          <Input type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder="aiu-team-1:…" />
          <span className="block text-xs text-muted-foreground">Without one, ai-usage starts a team of its own, and Invite gives you a key to send.</span>
        </label>
        {showRelay ? (
          <label className="block space-y-1 text-sm">
            <span>The team's own relay</span>
            <Input value={relay} onChange={(e) => setRelay(e.target.value)} placeholder="https://relay.example.com" />
          </label>
        ) : (
          <button type="button" className="text-xs text-muted-foreground underline-offset-2 hover:underline" onClick={() => setShowRelay(true)}>
            The team runs its own relay
          </button>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <Button type="submit" disabled={busy}>
            {busy ? <Spinner /> : null}
            {failed !== null ? "Retry" : `Install ai-usage on ${machine(view)}`}
          </Button>
          <Button type="button" variant="ghost" onClick={example.show}>
            See an example team
          </Button>
        </div>
      </form>
      {failed !== null ? <Log lines={failed.log} /> : null}
      <p className="text-xs text-muted-foreground">Removing this plugin leaves ai-usage running. Settings → AI Usage has Remove ai-usage.</p>
    </div>
  );
}

function FirstRun({ view, job }: { view: HostView; job: JobView | null }) {
  const rpc = useRpc<typeof rpcContract>();
  const example = useExample();
  const running = job?.status === "running";
  return (
    <div className="space-y-3">
      <Quiet>ai-usage {view.version ?? ""} is on {machine(view)} but has not collected yet. Its first run reads every log once and registers the schedule.</Quiet>
      {job?.status === "failed" ? <Banner tone="out">{job.message}</Banner> : null}
      <div className="flex gap-2">
        <Button disabled={running} onClick={() => rpc.call("collect", { hostId: view.hostId }).catch((e: unknown) => toast.error(message(e)))}>
          {running ? <Spinner /> : null}
          Finish setup
        </Button>
        <Button variant="ghost" onClick={example.show}>
          See an example team
        </Button>
      </div>
      {job !== null ? <Log lines={job.log} /> : null}
    </div>
  );
}

function Failed({ view, job }: { view: HostView; job: JobView | null }) {
  const rpc = useRpc<typeof rpcContract>();
  return (
    <div className="space-y-3">
      <Banner tone="out">ai-usage on {machine(view)} did not give a report: {view.error ?? "no answer"}</Banner>
      <div className="flex gap-2">
        <Button variant="outline" onClick={() => void rpc.call("state", { hostId: view.hostId, hash: null })}>
          Try again
        </Button>
        <Button variant="ghost" onClick={() => rpc.call("collect", { hostId: view.hostId }).catch((e: unknown) => toast.error(message(e)))}>
          Collect now
        </Button>
      </div>
      {job !== null && job.status === "failed" ? <Log lines={[...job.log, job.message]} /> : null}
    </div>
  );
}

function TeamCard({ view, report }: { view: HostView; report: Report }) {
  const rpc = useRpc<typeof rpcContract>();
  const [joining, setJoining] = useState(false);
  const [key, setKey] = useState("");
  const [relay, setRelay] = useState("");
  const [busy, setBusy] = useState(false);
  const devices = report.team.devices.length;
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-card p-3 text-sm">
      <div className="text-muted-foreground">{devices <= 1 ? "Only this machine is in the team so far." : `${devices} devices in this team`}</div>
      <div className="flex gap-2">
        <Button
          size="sm"
          disabled={busy}
          onClick={() => {
            setBusy(true);
            rpc
              .call("invite", { hostId: view.hostId })
              .then(async (r) => {
                const copied = await experimental_copyToClipboard({ text: r.message });
                if (copied) toast.success("Copied an invitation with the team key. Send it like a password: the key cannot be revoked.");
                else toast.error("Could not copy the invitation");
              })
              .catch((e: unknown) => toast.error(message(e)))
              .finally(() => setBusy(false));
          }}
        >
          Invite
        </Button>
        <Button size="sm" variant="outline" onClick={() => setJoining(true)}>
          Join a team…
        </Button>
      </div>
      <Dialog open={joining} onOpenChange={setJoining}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Join another team</DialogTitle>
            <DialogDescription>
              {machine(view)} leaves its current team and joins the one whose key you paste. Its current key is kept once, as team.key.previous; the old team may still see this
              machine for a while.
            </DialogDescription>
          </DialogHeader>
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              rpc
                .call("join", { hostId: view.hostId, key: key.trim(), relay: relay.trim() === "" ? null : relay.trim() })
                .then(() => {
                  setJoining(false);
                  setKey("");
                  toast("Joining the team, then collecting…");
                })
                .catch((err: unknown) => toast.error(message(err)));
            }}
          >
            <Input type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder="aiu-team-1:…" aria-label="Team key" />
            <Input value={relay} onChange={(e) => setRelay(e.target.value)} placeholder="The team's own relay, if it has one" aria-label="Relay" />
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => setJoining(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={key.trim() === ""}>
                Join
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
