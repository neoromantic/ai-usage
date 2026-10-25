// The team report, drawn from the JSON as ai-usage worded it. Nothing here
// is worked out: the words, states, forecasts, and totals are the report's.
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import {
  ago,
  attentionTone,
  attentionWords,
  clock,
  deviceName,
  duration,
  forecastLine,
  left,
  limitingWindows,
  matrixView,
  plan,
  provider,
  share,
  shortName,
  tokenLabel,
  usd,
} from "../src/format";
import { PERIODS, type CostQuestion, type Period, type Report, type TeamAccount } from "../src/report";
import { Badge, Bar, Dot, Section } from "./bits";

export interface ReportProps {
  report: Report;
  now: number;
  /** The time zone clocks are drawn in: the example's own, else this window's. */
  timeZone?: string;
  /** Answers a cost question with `ai-usage cost`; absent for the example. */
  answer?: (args: string[]) => Promise<void>;
}

export function ReportView(props: ReportProps) {
  const { report } = props;
  return (
    <div className="space-y-6">
      <AttentionList {...props} />
      <Subscriptions {...props} />
      {report.cost !== null ? <CostBlock {...props} /> : null}
      <MatrixBlock report={report} />
      <Devices {...props} />
    </div>
  );
}

function AttentionList({ report }: ReportProps) {
  if (report.attention.length === 0) return null;
  return (
    <Section title="Attention">
      <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-card">
        {report.attention.map((a, i) => {
          const words = attentionWords(a);
          const tone = attentionTone(a.kind);
          return (
            <li key={i} className="flex gap-3 px-3 py-2 text-sm">
              <Dot state={tone === "out" ? "out" : tone === "tight" ? "tight" : null} className="mt-1.5" />
              <div className="min-w-0">
                <span className="font-medium">{words.subject}</span>
                {words.text !== "" ? <span className="text-muted-foreground"> · {words.text}</span> : null}
              </div>
            </li>
          );
        })}
      </ul>
    </Section>
  );
}

function accountTitle(p: string, a: TeamAccount): string {
  return `${provider(p)} ${a.alias ?? a.name}`;
}

function Subscriptions({ report, now, timeZone }: ReportProps) {
  const groups = report.team.providers
    .map((p) => ({ provider: p.provider, accounts: p.accounts.filter((a) => a.subscription) }))
    .filter((p) => p.accounts.length > 0);
  if (groups.length === 0) return null;
  return (
    <Section title="Subscriptions">
      <div className="grid gap-3 sm:grid-cols-2">
        {groups.flatMap((g) =>
          g.accounts.map((a) => {
            const windows = limitingWindows(a);
            return (
              <div key={`${g.provider}/${a.label}`} className="space-y-2 rounded-lg border border-border bg-card p-3">
                <div className="flex items-center justify-between gap-2">
                  <div className="min-w-0 truncate text-sm font-medium" title={a.label}>
                    {accountTitle(g.provider, a)}
                    {a.plan !== null ? <span className="font-normal text-muted-foreground"> · {plan(a.plan)}</span> : null}
                  </div>
                  <Badge className={cn(a.state === "out" || a.state === "over" ? "text-destructive-text" : a.state === "tight" ? "text-warning-text" : "")}>
                    {a.state}
                  </Badge>
                </div>
                {windows.map((w) => {
                  const forecast = forecastLine(w);
                  return (
                    <div key={w.name} className="space-y-1">
                      <div className="flex items-baseline justify-between gap-2 text-xs">
                        <span className="text-muted-foreground">{shortName(a, w.name)}</span>
                        <span>
                          <span className="font-medium">{left(w)}</span> left
                          {w.resets_at !== null ? (
                            <span className="text-muted-foreground"> · resets {clock(w.resets_at, now, timeZone)}</span>
                          ) : null}
                        </span>
                      </div>
                      <Bar percent={w.percent} state={w.state} />
                      {forecast !== null ? <div className="text-[11px] text-muted-foreground">{forecast}</div> : null}
                    </div>
                  );
                })}
                {windows.length === 0 && a.no_reading !== null ? <div className="text-xs text-muted-foreground">{a.no_reading}</div> : null}
                {windows.length === 0 && a.no_reading === null ? <div className="text-xs text-muted-foreground">No reading of its limits yet</div> : null}
                <div className="text-[11px] text-muted-foreground">
                  {a.users === 1 ? "1 user" : `${a.users} users`}
                  {a.busiest !== null && a.users > 1 ? ` · most: ${a.busiest}` : ""}
                  {a.last_active_at !== null ? ` · last used ${ago(a.last_active_at, now)}` : ""}
                  {a.quota !== null && a.quota.age_seconds >= 600 ? ` · reading ${duration(a.quota.age_seconds)} old` : ""}
                </div>
              </div>
            );
          }),
        )}
      </div>
    </Section>
  );
}

function MatrixBlock({ report }: { report: Report }) {
  const [period, setPeriod] = useState<Period>("7d");
  if (report.team.matrix.rows.length === 0) return null;
  const m = matrixView(report, period);
  const span = period === "today" && report.team.today_local ? "Today" : `${PERIODS.find((p) => p.id === period)?.title} UTC`;
  return (
    <Section
      title="Devices × subscriptions"
      aside={
        <div className="flex gap-1" role="tablist">
          {PERIODS.map((p) => (
            <button
              key={p.id}
              type="button"
              role="tab"
              aria-selected={p.id === period}
              onClick={() => setPeriod(p.id)}
              className={cn("rounded-md px-2 py-0.5 text-xs", p.id === period ? "bg-muted text-foreground" : "text-muted-foreground hover:text-foreground")}
            >
              {p.title}
            </button>
          ))}
        </div>
      }
    >
      <div className="overflow-x-auto rounded-lg border border-border bg-card">
        <table className="w-full border-collapse text-xs tabular-nums">
          <thead>
            <tr className="border-b border-border text-muted-foreground">
              <th className="px-3 py-2 text-left font-medium">Device</th>
              {m.columns.map((c) => (
                <th key={c.key} className="px-2 py-2 text-right font-medium" title={c.key}>
                  <span className="inline-flex items-center gap-1">
                    {c.state !== null ? <Dot state={c.state} /> : null}
                    <span className="whitespace-nowrap">{c.title}</span>
                  </span>
                  <div className="text-[10px] font-normal">{provider(c.provider)}</div>
                </th>
              ))}
              <th className="px-3 py-2 text-right font-medium">Total</th>
            </tr>
          </thead>
          <tbody>
            {m.rows.map((row) => (
              <tr key={row.id} className="border-b border-border/60 last:border-0">
                <td className="whitespace-nowrap px-3 py-1.5">{row.device}</td>
                {row.cells.map((cell, i) => (
                  <td key={i} className="px-2 py-1.5 text-right">
                    <span
                      className={cn("inline-block min-w-8 rounded px-1 text-right", cell.heat > 0 ? "bg-foreground text-background" : "text-muted-foreground")}
                      style={cell.heat > 0 ? { opacity: 0.25 + cell.heat * 0.15 } : undefined}
                      title={cell.share !== null ? `${share(cell.share)}% of column` : undefined}
                    >
                      {cell.text}
                    </span>
                  </td>
                ))}
                <td className="px-3 py-1.5 text-right font-medium">{row.total}</td>
              </tr>
            ))}
            <tr className="border-t border-border text-muted-foreground">
              <td className="px-3 py-1.5 font-medium">Total</td>
              {m.columns.map((c) => (
                <td key={c.key} className="px-2 py-1.5 text-right">
                  {c.total}
                </td>
              ))}
              <td className="px-3 py-1.5 text-right font-medium text-foreground">{m.total}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p className="text-[11px] text-muted-foreground">
        {span} · M {tokenLabel(report)}
        {report.team.estimated_reason !== null ? ` · ~ ${report.team.estimated_reason}` : ""}
      </p>
    </Section>
  );
}

function Devices({ report, now }: ReportProps) {
  const devices = report.team.devices;
  if (devices.length === 0) return null;
  return (
    <Section title={devices.length === 1 ? "1 device" : `${devices.length} devices`}>
      <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-card">
        {devices.map((d) => (
          <li key={d.device} className="space-y-0.5 px-3 py-2 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <Dot state={d.silent ? "tight" : d.error !== null ? "out" : "ok"} />
              <span className="font-medium">{deviceName(d)}</span>
              {d.this_device ? <Badge>this machine</Badge> : null}
              {d.pooled.length > 0 ? <Badge title={d.pooled.join(", ")}>Account Pooler</Badge> : null}
              <span className="text-xs text-muted-foreground">
                {d.collector_version}
                {d.collected_at !== null ? ` · reported ${ago(d.collected_at, now)}` : ""}
              </span>
            </div>
            {d.note !== null && d.note !== "" ? <div className="pl-4 text-xs text-muted-foreground">{d.note}</div> : null}
          </li>
        ))}
      </ul>
    </Section>
  );
}

function CostBlock({ report, now, timeZone, answer }: ReportProps) {
  const c = report.cost!;
  const active = c.subscriptions.filter((s) => s.state !== "ended");
  const next = c.next_charge;
  const nextSub = next === null ? undefined : c.subscriptions.find((s) => s.provider === next.provider && s.account === next.account);
  const parts = [
    `${usd(c.total_usd)} a month for ${active.length === 1 ? "1 subscription" : `${active.length} subscriptions`}${c.without_price > 0 ? `, +${c.without_price} without a price` : ""}`,
    c.used_pct !== null ? `the team used ${c.used_estimated ? "~" : ""}${share(c.used_pct)}% over 4 weeks` : null,
    next !== null
      ? `next charge ${provider(next.provider)} ${nextSub?.alias ?? nextSub?.name ?? next.account} ${clock(next.at, now, timeZone)}${next.usd !== null ? ` · ${usd(next.usd)}` : ""}`
      : null,
  ].filter((x) => x !== null);
  return (
    <Section title="Cost">
      <p className="text-sm">{parts.join(" · ")}</p>
      {c.questions.length > 0 ? (
        <div className="space-y-2">
          {c.questions.map((q) => (
            <Question key={q.id} q={q} answer={answer} />
          ))}
        </div>
      ) : null}
    </Section>
  );
}

function Question({ q, answer }: { q: CostQuestion; answer?: (args: string[]) => Promise<void> }) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const target = `${q.provider}:${q.account}`;
  const act = async (args: string[]) => {
    if (answer === undefined) {
      toast("This is an example team: its questions answer nothing.");
      return;
    }
    setBusy(true);
    try {
      await answer(args);
    } finally {
      setBusy(false);
    }
  };
  const dollars = (v: string) => {
    const m = /^\$?(\d{1,3}(,\d{3})+|\d+)(\.\d{1,2})?$/.exec(v.trim());
    return m === null ? null : v.trim().replace(/^\$/, "").replaceAll(",", "");
  };
  return (
    <div className="space-y-2 rounded-lg border border-border bg-card p-3 text-sm">
      <p>{q.text}</p>
      <div className="flex flex-wrap items-center gap-2">
        {q.kind === "still_paid" ? (
          <>
            <Button size="sm" disabled={busy} onClick={() => void act(["cost", "confirm", target])}>
              Still paying
            </Button>
            <Button size="sm" variant="outline" disabled={busy} onClick={() => void act(["cost", "cancel", target])}>
              Cancelled
            </Button>
          </>
        ) : null}
        {q.kind === "renewal" && q.suggestion !== null ? (
          <Button size="sm" disabled={busy} onClick={() => void act(["cost", "set", target, "--renews", q.suggestion!])}>
            Charges {q.suggestion}
          </Button>
        ) : null}
        {q.kind === "renewal" && q.suggestion_yearly !== null && q.choices.includes("yearly") ? (
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void act(["cost", "set", target, "--yearly", "--renews", q.suggestion_yearly!])}>
            Yearly, {q.suggestion_yearly}
          </Button>
        ) : null}
        {q.kind === "price"
          ? q.choices.map((choice) => {
              const price = /\$([\d,.]+)/.exec(choice)?.[1];
              return price === undefined ? null : (
                <Button key={choice} size="sm" variant="outline" disabled={busy} onClick={() => void act(["cost", "set", target, "--price", price.replaceAll(",", "")])}>
                  {choice}
                </Button>
              );
            })
          : null}
        {q.kind === "renewal" || q.kind === "price" ? (
          <form
            className="flex items-center gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (q.kind === "price") {
                const v = dollars(typed);
                if (v === null) return void toast.error("Type a price in dollars, as 20 or 19.99");
                void act(["cost", "set", target, "--price", v]);
              } else {
                if (!/^(\d{4}-\d{2}-\d{2}|\d{1,2})$/.test(typed.trim())) return void toast.error("Type a day, as 2026-11-04, or a day of the month");
                void act(["cost", "set", target, "--renews", typed.trim()]);
              }
            }}
          >
            <Input className="h-8 w-36" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={q.kind === "price" ? "$ a month" : "YYYY-MM-DD"} aria-label="Answer" />
            <Button size="sm" variant="outline" type="submit" disabled={busy || typed.trim() === ""}>
              Save
            </Button>
          </form>
        ) : null}
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => void act(["cost", "skip", target, q.kind])}>
          Don't ask again
        </Button>
      </div>
    </div>
  );
}
