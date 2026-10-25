// How the panel prints the report's numbers and times, the way the menu bar
// app does (macos/Sources/AIUsageKit/Format.swift, Glance.swift). It picks
// among what the report decided and works out nothing of its own.
import type { Attention, MatrixCell, MatrixColumn, MatrixRow, Report, Share, TeamAccount, TeamDevice, Usage, Window } from "./report.js";

export const NONE = "–";

/** Tokens in whole millions: 603, "<1" under a million, the none mark for none. */
export function millions(n: number): string {
  if (n <= 0) return NONE;
  if (n < 1_000_000) return "<1";
  return String(Math.round(n / 1e6));
}

/** Tokens in millions with their unit: "167M", "1,975M", "<1M". */
export function tokensM(n: number): string {
  if (n <= 0) return NONE;
  if (n < 1_000_000) return "<1M";
  return `${Math.round(n / 1e6).toLocaleString("en-US")}M`;
}

/** A share in whole percents: "<1" under one, ">99" short of all of it. */
export function share(v: number | null | undefined): string {
  if (v === null || v === undefined || v <= 0) return NONE;
  if (v < 1) return "<1";
  if (v > 99 && v < 100) return ">99";
  return String(Math.round(v));
}

/** A duration in at most two units: 34m, 7h 5m, 1d 23h, 12d. */
export function duration(seconds: number): string {
  if (seconds < 60) return "<1m";
  const m = Math.floor(seconds / 60);
  const [days, hours, mins] = [Math.floor(m / 1440), Math.floor(m / 60) % 24, m % 60];
  if (days > 0) return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
  if (hours > 0) return mins > 0 ? `${hours}h ${mins}m` : `${hours}h`;
  return `${mins}m`;
}

/** How long ago: "just now", "7m ago". */
export function ago(at: string | null | undefined, now: number): string | null {
  if (at === null || at === undefined || at === "") return null;
  const d = (now - Date.parse(at)) / 1000;
  if (!Number.isFinite(d)) return null;
  return d < 60 ? "just now" : `${duration(d)} ago`;
}

/** A time within six days as a weekday and a 24-hour clock, "Sun 01:00"; else "2 Oct 15:04". */
export function clock(at: string, now: number, timeZone?: string): string {
  const t = Date.parse(at);
  const near = Math.abs(t - now) < 6 * 86400_000;
  const parts = new Intl.DateTimeFormat("en-GB", {
    timeZone,
    weekday: near ? "short" : undefined,
    day: near ? undefined : "numeric",
    month: near ? undefined : "short",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).formatToParts(new Date(t));
  const get = (type: string) => parts.find((p) => p.type === type)?.value ?? "";
  const time = `${get("hour")}:${get("minute")}`;
  return near ? `${get("weekday")} ${time}` : `${get("day")} ${get("month")} ${time}`;
}

/** "Claude", "Codex". */
export function provider(id: string): string {
  return id.slice(0, 1).toUpperCase() + id.slice(1);
}

/** "Max", "SuperGrok". */
export function plan(p: string): string {
  return p.slice(0, 1).toUpperCase() + p.slice(1);
}

export function usd(v: number): string {
  return `$${Math.round(v).toLocaleString("en-US")}`;
}

/** Weighted tokens where the report sent them, else input plus output. */
export function shown(x: { burn: Usage | null; usage: Usage }): Usage {
  return x.burn ?? x.usage;
}
export function shownShare(x: { burn_share: Share | null; share: Share }): Share {
  return x.burn_share ?? x.share;
}

/** The words for the tokens the lists show, from the first matrix row, as the app's tokenLabel. */
export function tokenLabel(r: Report): string {
  const first = r.team.matrix.rows[0];
  return first === undefined || first.burn !== null ? "weighted tokens" : "tokens in+out";
}

/** How full the window is now is known: it has not reset since it was read, and the reading covers it. */
export function known(w: Window): boolean {
  return !w.reset && w.unread !== true;
}

export function percentLeft(w: Window): number {
  return Math.max(100 - Math.floor(w.percent), 0);
}

/** What is left of a window: "42%", "~42%" for a stale reading, "?" when not known. */
export function left(w: Window | undefined): string {
  if (w === undefined || !known(w)) return "?";
  return `${w.stale ? "~" : ""}${percentLeft(w)}%`;
}

/** A window's name beside the main one: "7d Fable" is "Fable" when the main window is "7d". */
export function shortName(a: TeamAccount, name: string): string {
  const main = a.quota?.windows.find((w) => w.main)?.name;
  return main !== undefined && name.startsWith(`${main} `) ? name.slice(main.length + 1) : name;
}

/**
 * The windows that limit an account, as the app's Limits row has them: the
 * main one, or the one that limits it when the main one has no reading, then
 * the others the report marks with `limits`, the one with the least left first.
 */
export function limitingWindows(a: TeamAccount): Window[] {
  const windows = a.quota?.windows ?? [];
  const main = windows.find((w) => w.main);
  let tightest: Window | undefined;
  for (const w of windows) {
    if (known(w) && (w.limits || w.main) && (tightest === undefined || percentLeft(w) < percentLeft(tightest))) tightest = w;
  }
  const first = main !== undefined && known(main) ? main : tightest;
  if (first === undefined) return [];
  const others = windows
    .map((w, i) => ({ w, i }))
    .filter(({ w }) => w.limits && known(w) && w.name !== first.name)
    .sort((x, y) => percentLeft(x.w) - percentLeft(y.w) || x.i - y.i)
    .map(({ w }) => w);
  return [first, ...others];
}

/** The pace a window's forecast takes, as the report names it. */
export function pace(w: Window): string {
  const length = w.minutes !== null && w.minutes > 0 ? w.minutes : windowMinutes(w.name);
  if (w.forecast?.basis === "recent") return (length ?? 10080) < 1440 ? "the recent pace" : "the last day's pace";
  return (length ?? 10080) === 10080 ? "this week's pace" : "this window's pace";
}

function windowMinutes(name: string): number | null {
  const m = /^(\d+)([mhd])$/.exec(name);
  if (m === null) return null;
  const n = Number(m[1]);
  return m[2] === "m" ? n : m[2] === "h" ? n * 60 : n * 1440;
}

/** "113% at reset · this week's pace"; "would reach 108% at reset" for a stopped forecast. */
export function forecastLine(w: Window): string | null {
  const f = w.forecast;
  if (f === null) return null;
  return `${f.stopped ? "would reach " : ""}${Math.round(f.percent)}% at reset · ${pace(w)}`;
}

/**
 * What an attention entry is about and what to know, as the report words it.
 * An entry from an ai-usage that does not word its entries is about its
 * devices, and says its message.
 */
export function attentionWords(a: Attention): { subject: string; text: string } {
  const subject = a.subject !== "" ? a.subject : (a.devices ?? []).join(", ");
  return { subject, text: a.text !== "" ? a.text : (a.message ?? "") };
}

/** The tone of an attention entry's mark: out and over red, the warnings yellow, the rest quiet. */
export function attentionTone(kind: string): "out" | "tight" | "quiet" {
  if (kind === "out" || kind === "over") return "out";
  if (["tight", "outside", "error", "no_reading", "silent", "check"].includes(kind)) return "tight";
  return "quiet";
}

/** A step of the heat map from 1 to 5 against the largest cell, as the console shades it; 0 for none. */
export function heatStep(v: number, top: number): number {
  if (v <= 0 || top <= 0) return 0;
  return Math.min(Math.max(5 - Math.floor(Math.log10(top / v) * 2), 1), 5);
}

/** "~" before a number that is an estimate, never before the none mark. */
function estimated(text: string, on: boolean): string {
  return on && text !== NONE ? `~${text}` : text;
}

/** The matrix in a period: each cell's tokens, the row totals, and each column's. */
export function matrixView(r: Report, p: "today" | "7d" | "30d" | "90d") {
  const { columns, rows } = r.team.matrix;
  const top = Math.max(0, ...rows.flatMap((row) => row.cells.map((c) => shown(c)[p])));
  const cell = (c: MatrixCell | undefined) => {
    const tokens = c === undefined ? 0 : shown(c)[p];
    return { text: millions(tokens), heat: heatStep(tokens, top), tokens, share: c === undefined ? null : shownShare(c)[p] };
  };
  return {
    columns: columns.map((c: MatrixColumn) => ({
      key: `${c.provider}/${c.no_quota ? "" : (c.label ?? c.name)}`,
      provider: c.provider,
      title: c.no_quota ? "no quota" : c.name,
      state: c.no_quota || c.state === "unknown" ? null : c.state,
      total: millions(shown(c)[p]),
    })),
    rows: rows.map((row: MatrixRow) => ({
      device: row.device,
      id: row.device_id,
      cells: columns.map((_, i) => cell(row.cells[i])),
      total: estimated(millions(shown(row)[p]), row.burn_estimated),
    })),
    // The team's total is an estimate when a row in it is, as the app's.
    total: estimated(millions(rows.reduce((sum, row) => sum + shown(row)[p], 0)), rows.some((row) => row.burn_estimated)),
  };
}

/** The device the report calls this one, as its rows do: by name, or by label before v0.9.0. */
export function deviceName(d: TeamDevice): string {
  return d.name !== "" ? d.name : d.label;
}
