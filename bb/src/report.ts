// The report `ai-usage report --json` prints, schema version 4, as
// docs/json-schema.md describes it and the menu bar app reads it
// (macos/Sources/AIUsageKit/Report.swift). Only the fields the plugin shows
// are typed here. A field an earlier release leaves out reads as the app
// reads it: empty, false, 0, or null.
import { SCHEMA_VERSION } from "./version.js";

export type Period = "today" | "7d" | "30d" | "90d";
export const PERIODS: { id: Period; title: string }[] = [
  { id: "today", title: "Today" },
  { id: "7d", title: "7 Days" },
  { id: "30d", title: "30 Days" },
  { id: "90d", title: "90 Days" },
];

export type Usage = Record<Period, number>;
export type Share = Record<Period, number | null>;

export interface Health {
  item: string;
  status: string;
  state: string;
  at: string | null;
  release: string | null;
}

export interface Collector {
  version: string;
  device: string;
  device_label: string;
  os_user: string;
  name: string;
  team: string;
  last_run_at: string | null;
  last_success_at: string | null;
  last_error: string | null;
  relay: { url: string | null; last_push_at: string | null; last_pull_at: string | null; pending: boolean; last_error: string | null };
  schedule: { registered: boolean; foreground: boolean; error: string | null };
  update: { checked_at: string | null; latest: string | null; staged: string | null; error: string | null; method: string; command: string | null };
  health: Health[];
}

export interface Attention {
  kind: string;
  subject: string;
  text: string;
  provider: string | null;
  account: string | null;
  name: string | null;
  window: string | null;
  devices: string[] | null;
  at: string | null;
  resets_at: string | null;
  percent: number | null;
  message: string | null;
}

export interface Forecast {
  percent: number;
  elapsed: number;
  runs_out_at: string | null;
  basis: string | null;
  pace: number | null;
  stopped: boolean;
}

export interface Window {
  name: string;
  percent: number;
  resets_at: string | null;
  minutes: number | null;
  main: boolean;
  limits: boolean;
  stale: boolean;
  reset: boolean;
  unread: boolean | null;
  state: string;
  forecast: Forecast | null;
}

export interface Quota {
  observed_at: string;
  age_seconds: number;
  stale: boolean;
  device: string | null;
  windows: Window[];
}

export interface TeamAccount {
  label: string;
  name: string;
  alias: string | null;
  subscription: boolean;
  current: boolean;
  devices: string[];
  plan: string | null;
  state: string;
  quota: Quota | null;
  sessions: number;
  usage: Usage;
  burn: Usage | null;
  users: number;
  busiest: string | null;
  last_active_at: string | null;
  no_reading: string | null;
}

export interface TeamDevice {
  device: string;
  label: string;
  os_user: string;
  name: string;
  this_device: boolean;
  collector_version: string;
  collected_at: string | null;
  age_seconds: number;
  error: string | null;
  silent: boolean;
  old: boolean;
  usage: Usage;
  burn: Usage | null;
  note: string | null;
  pooled: string[];
}

export interface MatrixColumn {
  provider: string;
  label: string | null;
  name: string;
  no_quota: boolean;
  state: string;
  percent: number | null;
  usage: Usage;
  burn: Usage | null;
}

export interface MatrixCell {
  usage: Usage;
  burn: Usage | null;
  share: Share;
  burn_share: Share | null;
}

export interface MatrixRow {
  device: string;
  device_id: string;
  cells: MatrixCell[];
  usage: Usage;
  burn: Usage | null;
  share: Share;
  burn_share: Share | null;
  burn_estimated: boolean;
}

export interface Team {
  pulled_at: string | null;
  latest_version: string | null;
  today_local: boolean;
  estimated_reason: string | null;
  devices: TeamDevice[];
  providers: { provider: string; accounts: TeamAccount[] }[];
  matrix: { columns: MatrixColumn[]; rows: MatrixRow[] };
}

export interface CostQuestion {
  id: string;
  kind: string;
  provider: string;
  account: string;
  text: string;
  suggestion: string | null;
  suggestion_yearly: string | null;
  choices: string[];
}

export interface CostSubscription {
  provider: string;
  account: string;
  name: string;
  alias: string | null;
  plan_name: string | null;
  price_usd: number | null;
  state: string;
  renews_at: string | null;
}

export interface Cost {
  currency: string;
  total_usd: number;
  without_price: number;
  used_usd: number | null;
  used_pct: number | null;
  unused_usd: number | null;
  used_estimated: boolean;
  next_charge: { provider: string; account: string; at: string; usd: number | null } | null;
  subscriptions: CostSubscription[];
  questions: CostQuestion[];
}

export interface Report {
  schema_version: number;
  generated_at: string;
  collector: Collector;
  attention: Attention[];
  team: Team;
  cost: Cost | null;
}

export class SchemaError extends Error {
  constructor(readonly version: unknown) {
    super(`This plugin reads report schema ${SCHEMA_VERSION}; ai-usage printed ${String(version)}. Update the AI Usage plugin and ai-usage to the same release.`);
  }
}

type Obj = Record<string, unknown>;
const obj = (v: unknown): Obj => (v !== null && typeof v === "object" && !Array.isArray(v) ? (v as Obj) : {});
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const str = (v: unknown, absent = ""): string => (typeof v === "string" ? v : absent);
const ostr = (v: unknown): string | null => (typeof v === "string" ? v : null);
const num = (v: unknown, absent = 0): number => (typeof v === "number" && Number.isFinite(v) ? v : absent);
const onum = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) ? v : null);
const bool = (v: unknown): boolean => v === true;

function usage(v: unknown): Usage {
  const o = obj(v);
  return { today: num(o.today), "7d": num(o["7d"]), "30d": num(o["30d"]), "90d": num(o["90d"]) };
}
const ousage = (v: unknown): Usage | null => (v === null || v === undefined ? null : usage(v));
function share(v: unknown): Share {
  const o = obj(v);
  return { today: onum(o.today), "7d": onum(o["7d"]), "30d": onum(o["30d"]), "90d": onum(o["90d"]) };
}
const oshare = (v: unknown): Share | null => (v === null || v === undefined ? null : share(v));

function window(v: unknown): Window {
  const o = obj(v);
  const f = o.forecast === null || o.forecast === undefined ? null : obj(o.forecast);
  return {
    name: str(o.name),
    percent: num(o.percent),
    resets_at: ostr(o.resets_at),
    minutes: onum(o.minutes),
    main: bool(o.main),
    limits: bool(o.limits),
    stale: bool(o.stale),
    reset: bool(o.reset),
    unread: typeof o.unread === "boolean" ? o.unread : null,
    state: str(o.state, "unknown"),
    forecast:
      f === null
        ? null
        : { percent: num(f.percent), elapsed: num(f.elapsed), runs_out_at: ostr(f.runs_out_at), basis: ostr(f.basis), pace: onum(f.pace), stopped: bool(f.stopped) },
  };
}

/**
 * Reads a report, or throws a SchemaError when its schema is not the one
 * this plugin reads, as the app refuses one.
 */
export function readReport(raw: unknown): Report {
  const r = obj(raw);
  if (r.schema_version !== SCHEMA_VERSION) throw new SchemaError(r.schema_version);
  const c = obj(r.collector);
  const relay = obj(c.relay);
  const schedule = obj(c.schedule);
  const update = obj(c.update);
  const team = obj(r.team);
  const matrix = obj(team.matrix);
  const cost = r.cost === null || r.cost === undefined ? null : obj(r.cost);
  return {
    schema_version: SCHEMA_VERSION,
    generated_at: str(r.generated_at),
    collector: {
      version: str(c.version),
      device: str(c.device),
      device_label: str(c.device_label),
      os_user: str(c.os_user),
      name: str(c.name),
      team: str(c.team),
      last_run_at: ostr(c.last_run_at),
      last_success_at: ostr(c.last_success_at),
      last_error: ostr(c.last_error),
      relay: { url: ostr(relay.url), last_push_at: ostr(relay.last_push_at), last_pull_at: ostr(relay.last_pull_at), pending: bool(relay.pending), last_error: ostr(relay.last_error) },
      schedule: { registered: bool(schedule.registered), foreground: bool(schedule.foreground), error: ostr(schedule.error) },
      update: {
        checked_at: ostr(update.checked_at),
        latest: ostr(update.latest),
        staged: ostr(update.staged),
        error: ostr(update.error),
        method: str(update.method, "self"),
        command: ostr(update.command),
      },
      health: arr(c.health).map((h) => {
        const o = obj(h);
        return { item: str(o.item), status: str(o.status), state: str(o.state), at: ostr(o.at), release: ostr(o.release) };
      }),
    },
    attention: arr(r.attention).map((a) => {
      const o = obj(a);
      return {
        kind: str(o.kind),
        subject: str(o.subject),
        text: str(o.text),
        provider: ostr(o.provider),
        account: ostr(o.account),
        name: ostr(o.name),
        window: ostr(o.window),
        devices: Array.isArray(o.devices) ? o.devices.map((d) => str(d)) : null,
        at: ostr(o.at),
        resets_at: ostr(o.resets_at),
        percent: onum(o.percent),
        message: ostr(o.message),
      };
    }),
    team: {
      pulled_at: ostr(team.pulled_at),
      latest_version: ostr(team.latest_version),
      today_local: bool(team.today_local),
      estimated_reason: ostr(team.estimated_reason),
      devices: arr(team.devices).map((d) => {
        const o = obj(d);
        return {
          device: str(o.device),
          label: str(o.label),
          os_user: str(o.os_user),
          name: str(o.name),
          this_device: bool(o.this_device),
          collector_version: str(o.collector_version),
          collected_at: ostr(o.collected_at),
          age_seconds: num(o.age_seconds),
          error: ostr(o.error),
          silent: bool(o.silent),
          old: bool(o.old),
          usage: usage(o.usage),
          burn: ousage(o.burn),
          note: ostr(o.note),
          pooled: arr(o.pooled).map((p) => str(p)).filter((p) => p !== ""),
        };
      }),
      providers: arr(team.providers).map((p) => {
        const o = obj(p);
        return {
          provider: str(o.provider),
          accounts: arr(o.accounts).map((a) => {
            const x = obj(a);
            const q = x.quota === null || x.quota === undefined ? null : obj(x.quota);
            return {
              label: str(x.label),
              name: str(x.name),
              alias: ostr(x.alias),
              subscription: bool(x.subscription),
              current: bool(x.current),
              devices: arr(x.devices).map((d) => str(d)),
              plan: ostr(x.plan),
              state: str(x.state, "unknown"),
              quota:
                q === null
                  ? null
                  : { observed_at: str(q.observed_at), age_seconds: num(q.age_seconds), stale: bool(q.stale), device: ostr(q.device), windows: arr(q.windows).map(window) },
              sessions: num(x.sessions),
              usage: usage(x.usage),
              burn: ousage(x.burn),
              users: num(x.users),
              busiest: ostr(x.busiest),
              last_active_at: ostr(x.last_active_at),
              no_reading: ostr(x.no_reading),
            };
          }),
        };
      }),
      matrix: {
        columns: arr(matrix.columns).map((col) => {
          const o = obj(col);
          return {
            provider: str(o.provider),
            label: ostr(o.label),
            name: str(o.name),
            no_quota: bool(o.no_quota),
            state: str(o.state, "unknown"),
            percent: onum(o.percent),
            usage: usage(o.usage),
            burn: ousage(o.burn),
          };
        }),
        rows: arr(matrix.rows).map((row) => {
          const o = obj(row);
          return {
            device: str(o.device),
            device_id: str(o.device_id),
            cells: arr(o.cells).map((cell) => {
              const x = obj(cell);
              return { usage: usage(x.usage), burn: ousage(x.burn), share: share(x.share), burn_share: oshare(x.burn_share) };
            }),
            usage: usage(o.usage),
            burn: ousage(o.burn),
            share: share(o.share),
            burn_share: oshare(o.burn_share),
            burn_estimated: bool(o.burn_estimated),
          };
        }),
      },
    },
    cost:
      cost === null
        ? null
        : {
            currency: str(cost.currency, "USD"),
            total_usd: num(cost.total_usd),
            without_price: num(cost.without_price),
            used_usd: onum(cost.used_usd),
            used_pct: onum(cost.used_pct),
            unused_usd: onum(cost.unused_usd),
            used_estimated: bool(cost.used_estimated),
            next_charge:
              cost.next_charge === null || cost.next_charge === undefined
                ? null
                : (() => {
                    const n = obj(cost.next_charge);
                    return { provider: str(n.provider), account: str(n.account), at: str(n.at), usd: onum(n.usd) };
                  })(),
            subscriptions: arr(cost.subscriptions).map((s) => {
              const o = obj(s);
              return {
                provider: str(o.provider),
                account: str(o.account),
                name: str(o.name),
                alias: ostr(o.alias),
                plan_name: ostr(o.plan_name),
                price_usd: onum(o.price_usd),
                state: str(o.state),
                renews_at: ostr(o.renews_at),
              };
            }),
            questions: arr(cost.questions).map((q) => {
              const o = obj(q);
              return {
                id: str(o.id),
                kind: str(o.kind),
                provider: str(o.provider),
                account: str(o.account),
                text: str(o.text),
                suggestion: ostr(o.suggestion),
                suggestion_yearly: ostr(o.suggestion_yearly),
                choices: arr(o.choices).map((c) => str(c)),
              };
            }),
          },
  };
}
