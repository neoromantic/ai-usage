import { describe, expect, test } from "bun:test";
import { experimental_sanitizeInheritedChildProcessEnv as sanitize } from "@get-bb/plugin-sdk/host";
import { allowed } from "../src/args";
import { candidates, cleanEnv, standardDirs } from "../src/env";
import { inviteMessage } from "../src/invite";
import { BusyError, Jobs } from "../src/jobs";
import { errorMessage, redact } from "../src/message";
import { autoInstallable, installRefusal, phaseOf, type HostFacts } from "../src/phase";
import { NoTeamError, teamSource, type SourceMachine } from "../src/team-source";
import type { Probe } from "../src/contract";
import { atLeast } from "../src/version";

describe("cleanEnv", () => {
  const env = {
    PATH: "/Applications/bb.app/Contents/Resources/app.asar.unpacked/node_modules/bb-app/host-daemon/dist:/home/user/.bb/bin:/home/user/.local/bin:/usr/bin:/usr/bin::/opt/homebrew/bin",
    BB_CLI: "/home/user/.bb/bin/bb",
    BB_SERVER_URL: "http://127.0.0.1:1",
    NODE_ENV: "production",
    HOME: "/home/user",
    ANTHROPIC_BASE_URL: "http://127.0.0.1:1/api/v1/plugins/account-pool/claude",
    CODEX_POOL_AUTH_TOKEN: "made-up",
  };
  const out = cleanEnv(env, sanitize);

  test("drops BB's variables and its folders from PATH", () => {
    expect(out.BB_CLI).toBeUndefined();
    expect(out.BB_SERVER_URL).toBeUndefined();
    expect(out.NODE_ENV).toBeUndefined();
    expect(out.PATH).toBe("/home/user/.local/bin:/usr/bin:/opt/homebrew/bin");
  });

  test("leaves the pool's variables to ai-usage", () => {
    expect(out.ANTHROPIC_BASE_URL).toBe(env.ANTHROPIC_BASE_URL);
    expect(out.CODEX_POOL_AUTH_TOKEN).toBe("made-up");
    expect(out.HOME).toBe("/home/user");
  });

  test("an empty PATH gets the system's", () => {
    expect(cleanEnv({ PATH: "/home/user/.bb/bin", BB_CLI: "/home/user/.bb/bin/bb" }, sanitize).PATH).toBe("/usr/bin:/bin:/usr/sbin:/sbin");
  });

  test("looks on PATH first, then the standard folders, each once", () => {
    expect(candidates({ PATH: "/usr/bin:relative:/home/user/.local/bin" }, standardDirs("/home/user"))).toEqual([
      "/usr/bin/ai-usage",
      "/home/user/.local/bin/ai-usage",
      "/home/user/bin/ai-usage",
      "/opt/homebrew/bin/ai-usage",
      "/usr/local/bin/ai-usage",
      "/home/linuxbrew/.linuxbrew/bin/ai-usage",
    ]);
  });
});

describe("allowed", () => {
  test.each([
    [["report", "--json"]],
    [["collect", "--json"]],
    [["team", "join"]],
    [["uninstall"]],
    [["uninstall", "--forget", "--purge"]],
    [["relay", "set", "https://relay.example.com"]],
    [["cost", "confirm", "claude:ann@example.com"]],
    [["cost", "skip", "codex:ann@example.com", "still_paid"]],
    [["cost", "set", "claude:ann@example.com", "--price", "19.99", "--monthly"]],
    [["cost", "set", "claude:ann@example.com", "--yearly", "--renews", "2026-11-04"]],
  ])("runs %j", (args) => expect(allowed(args)).toBe(true));

  test.each([
    [["team", "join", "aiu-team-1:abc"]],
    [["report"]],
    [["uninstall", "--forget", "--forget"]],
    [["relay", "set", "file:///etc"]],
    [["cost", "set", "claude:ann@example.com"]],
    [["cost", "set", "claude:ann@example.com", "--price", "1; rm -rf ~"]],
    [["cost", "confirm", "hermes:ann"]],
    [["home", "add", "claude", "/tmp"]],
    [["sh", "-c", "id"]],
  ])("refuses %j", (args) => expect(allowed(args)).toBe(false));
});

describe("versions", () => {
  test("compares releases, and a build from source is newest", () => {
    expect(atLeast("v0.11.0", "v0.11.0")).toBe(true);
    expect(atLeast("v0.10.2", "v0.11.0")).toBe(false);
    expect(atLeast("v1.0.0", "v0.11.0")).toBe(true);
    expect(atLeast("devel", "v0.11.0")).toBe(true);
    expect(atLeast(null, "v0.11.0")).toBe(false);
  });
});

describe("messages", () => {
  test("reads ai-usage's own error, as the app does", () => {
    expect(errorMessage("ai-usage: waiting for another run\nai-usage: nothing collected yet\n\nUsage: …")).toBe("nothing collected yet");
    expect(errorMessage("brew noise\nai-usage: update failed\nsecond line")).toBe("update failed\nsecond line");
    expect(errorMessage("", "Homebrew's formula is behind\n")).toBe("Homebrew's formula is behind");
  });

  test("never lets a key through", () => {
    expect(redact("joined with aiu-team-1:made-up-key-for-tests ok")).toBe("joined with aiu-team-… ok");
  });

  test("an invite names a relay only when it is not the built-in one", () => {
    const key = "aiu-team-1:AAEC";
    expect(inviteMessage(key, { configured: "https://a.example", builtIn: "https://a.example" })).not.toContain("AI_USAGE_RELAY");
    const custom = inviteMessage(key, { configured: "https://relay.example.com", builtIn: "https://a.example" });
    expect(custom).toContain("AI_USAGE_RELAY='https://relay.example.com' AI_USAGE_TEAM_KEY='aiu-team-1:AAEC' sh");
    expect(custom.endsWith(key)).toBe(true);
  });
});

const probe = (over: Partial<Probe> = {}): Probe => ({
  platform: "darwin",
  arch: "arm64",
  uid: 501,
  user: "ann",
  macos: "15.1",
  bin: "/opt/homebrew/bin/ai-usage",
  aiHome: null,
  version: "v0.11.0",
  status: null,
  canUninstall: true,
  scheduleEntry: false,
  app: false,
  error: null,
  ...over,
});

const facts = (over: Partial<HostFacts> = {}): HostFacts => ({
  host: { status: "connected", type: "persistent" },
  bbPlatform: "darwin",
  probe: probe(),
  job: null,
  report: { ok: true, schemaMismatch: false, nothingCollected: false, error: null },
  ...over,
});

describe("phases", () => {
  const job = (status: "running" | "failed") => ({ id: "j", hostId: "h", kind: "install" as const, status, startedAt: 0, endedAt: null, message: "", log: [] });

  test("each state of a machine", () => {
    expect(phaseOf(facts({ host: null }))).toBe("no-host");
    expect(phaseOf(facts({ host: { status: "disconnected", type: "persistent" } }))).toBe("offline");
    expect(phaseOf(facts({ probe: null }))).toBe("loading");
    expect(phaseOf(facts({ probe: probe({ platform: "win32" }) }))).toBe("unsupported");
    expect(phaseOf(facts({ probe: probe({ bin: null }), report: null }))).toBe("absent");
    expect(phaseOf(facts({ probe: probe({ bin: null }), job: job("running") }))).toBe("installing");
    expect(phaseOf(facts({ probe: probe({ bin: null }), job: job("failed") }))).toBe("install-failed");
    expect(phaseOf(facts({ report: { ok: false, schemaMismatch: false, nothingCollected: true, error: "nothing collected yet" } }))).toBe("needs-first-run");
    expect(phaseOf(facts({ report: null, probe: probe({ status: { install: { collected: false } } }) }))).toBe("needs-first-run");
    expect(phaseOf(facts({ report: { ok: false, schemaMismatch: true, nothingCollected: false, error: "schema" } }))).toBe("schema-mismatch");
    expect(phaseOf(facts({ report: { ok: false, schemaMismatch: false, nothingCollected: false, error: "boom" } }))).toBe("error");
    expect(phaseOf(facts())).toBe("ready");
  });

  test("installs by itself only where nobody has to answer for it", () => {
    const absent = facts({ probe: probe({ bin: null }), report: null });
    expect(autoInstallable(absent, false)).toBe(true);
    expect(autoInstallable(absent, true)).toBe(false);
    expect(autoInstallable(facts(), false)).toBe(false);
    expect(autoInstallable({ ...absent, probe: probe({ bin: null, uid: 0 }) }, false)).toBe(false);
    expect(autoInstallable({ ...absent, probe: probe({ bin: null, scheduleEntry: true }) }, false)).toBe(false);
    expect(autoInstallable({ ...absent, bbPlatform: "wsl", probe: probe({ bin: null, platform: "linux" }) }, false)).toBe(false);
    expect(autoInstallable({ ...absent, host: { status: "connected", type: "ephemeral" } }, false)).toBe(false);
    expect(autoInstallable({ ...absent, host: { status: "disconnected", type: "persistent" } }, false)).toBe(false);
    expect(autoInstallable({ ...absent, probe: probe({ bin: null, platform: "win32" }) }, false)).toBe(false);
  });
});

describe("jobs", () => {
  test("one job a machine, joined by a request for the same one", async () => {
    const events: string[] = [];
    const jobs = new Jobs({ changed: (j) => events.push(`${j.kind}:${j.status}`), line: (_j, l) => events.push(l) });
    let finish = (_: string) => {};
    const a = jobs.start("h", "collect", (_job, log) => {
      log("reading");
      return new Promise<string>((done) => (finish = done));
    });
    expect(jobs.start("h", "collect", async () => "x").id).toBe(a.id);
    expect(() => jobs.start("h", "update", async () => "x")).toThrow(BusyError);
    expect(jobs.start("other", "update", async () => "x").hostId).toBe("other");
    finish("collected");
    await Bun.sleep(0);
    expect(jobs.get(a.id)?.status).toBe("done");
    expect(jobs.get(a.id)?.message).toBe("collected");
    expect(jobs.get(a.id)?.log).toEqual(["reading"]);
    const b = jobs.start("h", "update", async () => {
      throw new Error("no network");
    });
    await Bun.sleep(0);
    expect(jobs.latest("h")?.id).toBe(b.id);
    expect(jobs.get(b.id)?.status).toBe("failed");
    expect(jobs.get(b.id)?.message).toBe("no network");
    expect(events).toContain("collect:running");
  });
});

describe("installing", () => {
  test("never a second copy beside one the scheduler runs", () => {
    expect(installRefusal(probe({ bin: null }))).toBeNull();
    expect(installRefusal(probe())).toContain("already installed");
    expect(installRefusal(probe({ bin: null, scheduleEntry: true }))).toContain("scheduler on this machine already runs an ai-usage");
  });
});

describe("the team an install from Settings joins", () => {
  const machines = (m: Record<string, { bin: boolean; collected?: boolean; key?: string; relay?: string | null; fails?: boolean }>): SourceMachine => ({
    hasAiUsage: async (id) => m[id]?.bin === true,
    collected: async (id) => m[id]?.collected === true,
    key: async (id) => {
      if (m[id]?.fails) throw new Error("did not answer");
      return `${m[id]?.key ?? ""}\n`;
    },
    relay: async (id) => ({ configured: m[id]?.relay ?? "https://built-in.example", builtIn: "https://built-in.example" }),
  });

  test("no machine runs ai-usage: a team of its own", async () => {
    expect(await teamSource(["a", "b"], machines({ a: { bin: false } }))).toBeNull();
  });

  test("the first machine that gives its key, with its relay only when it is not the built-in one", async () => {
    const m = machines({ a: { bin: true, collected: true, fails: true }, b: { bin: true, collected: true, key: "aiu-team-1:made-up" } });
    expect(await teamSource(["a", "b"], m)).toEqual({ key: "aiu-team-1:made-up", relay: null });
    const custom = machines({ b: { bin: true, collected: true, key: "aiu-team-1:made-up", relay: "https://relay.example.com" } });
    expect(await teamSource(["b"], custom)).toEqual({ key: "aiu-team-1:made-up", relay: "https://relay.example.com" });
  });

  test("a machine that runs ai-usage but gives no key is an error, never a new team", async () => {
    await expect(teamSource(["a"], machines({ a: { bin: true, collected: false } }))).rejects.toThrow(NoTeamError);
    await expect(teamSource(["a"], machines({ a: { bin: true, collected: true, fails: true } }))).rejects.toThrow("did not answer");
    await expect(teamSource(["a"], machines({ a: { bin: true, collected: true, key: "" } }))).rejects.toThrow(NoTeamError);
  });
});
