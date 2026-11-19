import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import path from "node:path";
import {
  attentionWords,
  limitingWindows,
  matrixView,
  millions,
  shown,
  tokenLabel,
} from "../src/format";
import { readReport, SchemaError } from "../src/report";

const demo = (name: string) =>
  JSON.parse(readFileSync(path.join(import.meta.dir, "../../docs/demo", name), "utf8"));

const ADDED = new Set([
  "health",
  "method",
  "command",
  "limits",
  "today_local",
  "folders",
  "subject",
  "text",
  "note",
  "pooled",
  "name",
  "cost",
  "burn",
  "burn_share",
  "window_burn",
  "burn_days",
  "fresh",
  "basis",
  "pace",
  "stopped",
  "estimated_reason",
  "no_reading",
]);
const strip: (v: unknown) => unknown = (v) => {
  if (Array.isArray(v)) return v.map(strip);
  if (v === null || typeof v !== "object") return v;
  return Object.fromEntries(
    Object.entries(v)
      .filter(([k]) => !ADDED.has(k))
      .map(([k, x]) => [k, strip(x)]),
  );
};

describe.each(["team.json", "solo.json", "homebrew.json"])("%s", (name) => {
  const raw = demo(name);
  const r = readReport(raw);

  test("reads every part the panel shows", () => {
    expect(r.team.devices.length).toBe(raw.team.devices.length);
    expect(r.attention.length).toBe(raw.attention.length);
    expect(r.collector.health.length).toBe(raw.collector.health.length);
    expect(r.team.devices.every((d) => Array.isArray(d.pooled))).toBe(true);
    expect(r.cost).not.toBeNull();
  });

  test("the matrix's totals are the report's", () => {
    const m = matrixView(r, "7d");
    expect(m.rows.length).toBe(raw.team.matrix.rows.length);
    raw.team.matrix.columns.forEach((c: { burn: Record<string, number> }, i: number) =>
      expect(m.columns[i]!.total).toBe(millions(c.burn["7d"]!)),
    );
    expect(tokenLabel(r)).toBe("weighted tokens");
  });
});

test("a total with an estimated row in it is an estimate, as the app's", () => {
  const r = readReport(demo("team.json"));
  const estimated = r.team.matrix.rows.filter((row) => row.burn_estimated).map((row) => row.device);
  expect(estimated.length).toBeGreaterThan(0);
  const m = matrixView(r, "7d");
  for (const row of m.rows)
    expect(row.total.startsWith("~")).toBe(estimated.includes(row.device) && row.total !== "~–");
  expect(m.total.startsWith("~")).toBe(true);
  const sure = readReport(demo("solo.json"));
  expect(matrixView(sure, "7d").total.startsWith("~")).toBe(false);
});

test("a report of an earlier release reads its missing fields as the app does", () => {
  const r = readReport(strip(demo("team.json")));
  expect(r.collector.health).toEqual([]);
  expect(r.collector.update.method).toBe("self");
  expect(r.collector.update.command).toBeNull();
  expect(r.cost).toBeNull();
  expect(r.team.today_local).toBe(false);
  expect(
    r.team.devices.every((d) => d.name === "" && d.note === null && d.pooled.length === 0),
  ).toBe(true);
  expect(tokenLabel(r)).toBe("tokens in+out");
  const row = r.team.matrix.rows[0]!;
  expect(shown(row)).toEqual(row.usage);
  for (const p of r.team.providers)
    for (const a of p.accounts)
      for (const w of limitingWindows(a)) expect(w.main || !w.limits).toBe(true);
  const old = r.attention.find((a) => a.kind === "old")!;
  expect(attentionWords(old).subject).toBe((old.devices ?? []).join(", "));
});

test("shows the report's words as given, of a kind it does not know too", () => {
  const raw = demo("team.json");
  raw.attention.push({
    kind: "someday",
    subject: "leo@leo-air",
    text: "a kind from a later release",
  });
  const r = readReport(raw);
  expect(r.attention.map(attentionWords)).toEqual(
    raw.attention.map((a: { subject: string; text: string }) => ({
      subject: a.subject,
      text: a.text,
    })),
  );
});

test("another schema is refused", () => {
  expect(() => readReport({ schema_version: 5 })).toThrow(SchemaError);
  expect(() => readReport({ schema_version: 3, generated_at: "2026-09-24T13:40:00Z" })).toThrow(
    SchemaError,
  );
});
