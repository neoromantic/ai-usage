// The host entry on a made-up machine: a temporary home with fake ai-usage
// binaries that write down how they were called.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { install, probe, runCommand, type Machine } from "../src/host-impl";

let home: string;
let calls: string;

function fake(file: string, version: string, status: string) {
  mkdirSync(path.dirname(file), { recursive: true });
  writeFileSync(
    file,
    `#!/bin/sh
echo "$0 $* AI_USAGE_HOME=$AI_USAGE_HOME CALLER=$AI_USAGE_CALLER" >> "${calls}"
case "$1" in
  version) echo "${version}" ;;
  status) cat <<'JSON'
${status}
JSON
  ;;
  --help) echo "  ai-usage uninstall [--forget] [--purge]" ;;
  report) echo "ai-usage: nothing collected yet" >&2; exit 1 ;;
  team) read key; echo "joined team f00d"; [ "$key" = "aiu-team-1:AAEC" ] || exit 3 ;;
esac
`,
  );
  chmodSync(file, 0o755);
}

function machine(over: Partial<Machine> = {}): Machine {
  return {
    env: { PATH: `${path.join(home, "fakebin")}:/usr/bin:/bin`, HOME: home },
    home,
    dirs: [path.join(home, ".local/bin"), path.join(home, "bin")],
    platform: "darwin",
    arch: "arm64",
    uid: 501,
    user: "ann",
    tempDir: path.join(home, "tmp"),
    fetch,
    ...over,
  };
}

beforeAll(() => {
  home = mkdtempSync(path.join(os.tmpdir(), "aiu-plugin-"));
  calls = path.join(home, "calls.log");
  mkdirSync(path.join(home, "tmp"));
  // A crontab with someone else's line that only looks like ours.
  mkdirSync(path.join(home, "fakebin"));
  writeFileSync(path.join(home, "fakebin/crontab"), "#!/bin/sh\necho '*/15 * * * * /opt/bots/run # ai-usage bots'\n");
  chmodSync(path.join(home, "fakebin/crontab"), 0o755);
});
afterAll(() => rmSync(home, { recursive: true, force: true }));

const linux = { platform: "linux" as const };

describe("probe", () => {
  test("finds no ai-usage and no schedule on a fresh machine", async () => {
    const p = await probe(machine(linux));
    expect(p.bin).toBeNull();
    expect(p.version).toBeNull();
    expect(p.scheduleEntry).toBe(false);
  });

  test("a launch agent without a binary is an install all the same", async () => {
    const agents = path.join(home, "Library/LaunchAgents");
    mkdirSync(agents, { recursive: true });
    writeFileSync(path.join(agents, "io.github.neoromantic.ai-usage.plist"), "<plist/>");
    const p = await probe(machine({ env: { PATH: "/usr/bin:/bin", HOME: home } }));
    expect(p.bin).toBeNull();
    expect(p.scheduleEntry).toBe(true);
    rmSync(agents, { recursive: true });
  });

  test("takes the binary a launch agent runs from outside PATH, but never another program", async () => {
    const agents = path.join(home, "Library/LaunchAgents");
    mkdirSync(agents, { recursive: true });
    writeFileSync(path.join(agents, "io.github.neoromantic.ai-usage.plist"), "<plist/>");
    const elsewhere = path.join(home, "apps/ai-usage");
    fake(elsewhere, "v0.10.2", "{}");
    const plutil = (program: string) => {
      writeFileSync(path.join(home, "fakebin/plutil"), `#!/bin/sh\necho '${program}'\n`);
      chmodSync(path.join(home, "fakebin/plutil"), 0o755);
    };
    plutil(elsewhere);
    let p = await probe(machine());
    expect(p.scheduleEntry).toBe(true);
    expect(p.bin).toBe(elsewhere);
    expect(p.version).toBe("v0.10.2");
    plutil("/bin/sh");
    p = await probe(machine());
    expect(p.scheduleEntry).toBe(true);
    expect(p.bin).toBeNull();
    rmSync(agents, { recursive: true });
    rmSync(path.join(home, "fakebin/plutil"));
  });

  test("an older release is not asked for status, which made a team key before v0.11.0", async () => {
    fake(path.join(home, ".local/bin/ai-usage"), "v0.10.2", "{}");
    writeFileSync(calls, "");
    const p = await probe(machine(linux));
    expect(p.bin).toBe(path.join(home, ".local/bin/ai-usage"));
    expect(p.version).toBe("v0.10.2");
    expect(p.canUninstall).toBe(true);
    expect(readFileSync(calls, "utf8")).not.toContain(" status ");
  });

  test("runs the copy the scheduler runs, with its state folder", async () => {
    const scheduled = path.join(home, "opt/ai-usage/bin/ai-usage");
    const state = path.join(home, "state");
    const status = JSON.stringify({ schema_version: 4, collector: null, sources: [], install: { version: "v0.11.0", method: "self", home: state, collected: true, schedule: { state: "other", program: scheduled, home: state, error: null } } });
    fake(path.join(home, ".local/bin/ai-usage"), "v0.11.0", status);
    fake(scheduled, "v0.11.1", status);
    writeFileSync(calls, "");
    const p = await probe(machine(linux));
    expect(p.bin).toBe(scheduled);
    expect(p.aiHome).toBe(state);
    expect(p.version).toBe("v0.11.1");
    // Marked as BB's, so the runs record no usage stats.
    expect(readFileSync(calls, "utf8")).toContain(`${scheduled} status --json AI_USAGE_HOME=${state} CALLER=bb`);
  });
});

describe("run", () => {
  test("passes the key on standard input and refuses other commands", async () => {
    const bin = path.join(home, ".local/bin/ai-usage");
    const out = await runCommand(machine(linux), { bin, aiHome: null, args: ["team", "join"], stdin: "aiu-team-1:AAEC\n", json: false, timeoutMs: 10_000 });
    expect(out.code).toBe(0);
    expect(readFileSync(calls, "utf8")).not.toContain("aiu-team-1");
    await expect(runCommand(machine(linux), { bin, aiHome: null, args: ["sh"], stdin: null, json: false, timeoutMs: 10_000 })).rejects.toThrow();
    await expect(runCommand(machine(linux), { bin: "/bin/sh", aiHome: null, args: ["version"], stdin: null, json: false, timeoutMs: 10_000 })).rejects.toThrow();
  });

  test("says ai-usage's own error", async () => {
    const out = await runCommand(machine(linux), { bin: path.join(home, ".local/bin/ai-usage"), aiHome: null, args: ["report", "--json"], stdin: null, json: true, timeoutMs: 10_000 });
    expect(out.code).toBe(1);
    expect(out.json).toBeNull();
    expect(out.message).toBe("nothing collected yet");
  });
});

describe("install", () => {
  test("runs install.sh of the latest release, pinned to it, without the app or shell profiles, with the key only in its environment", async () => {
    const script = `#!/bin/sh
echo "ai-usage install: url=$AI_USAGE_DOWNLOAD_URL app=$AI_USAGE_NO_APP path=$AI_USAGE_NO_MODIFY_PATH relay=$AI_USAGE_RELAY caller=$AI_USAGE_CALLER" >&2
echo "ai-usage install: key=$AI_USAGE_TEAM_KEY" >&2
echo "ai-usage install: PATH=$PATH" >&2
echo "REPORT that stays out of the log"
`;
    const seen: string[] = [];
    const fetchFake = (async (url: string | URL | Request) => {
      seen.push(String(url));
      if (String(url).endsWith("/releases/latest")) return new Response(null, { status: 302, headers: { location: "https://github.com/neoromantic/ai-usage/releases/tag/v0.11.0" } });
      return new Response(script, { status: 200 });
    }) as typeof fetch;
    const lines: string[] = [];
    const out = await install(machine({ ...linux, fetch: fetchFake }), {
      teamKey: "aiu-team-1:made-up-key-for-tests",
      relay: "https://relay.example.com",
      onLine: (l) => lines.push(l),
    });
    expect(out).toMatchObject({ ok: true, code: 0, tag: "v0.11.0" });
    expect(seen).toEqual(["https://github.com/neoromantic/ai-usage/releases/latest", "https://raw.githubusercontent.com/neoromantic/ai-usage/v0.11.0/install.sh"]);
    const log = lines.join("\n");
    expect(log).toContain("url=https://github.com/neoromantic/ai-usage/releases/download/v0.11.0 app=1 path=1 relay=https://relay.example.com caller=bb");
    expect(log).toContain("key=aiu-team-…");
    expect(log).not.toContain("made-up-key");
    expect(log).not.toContain("REPORT");
  });

  test("a failed install says the installer's error", async () => {
    const fetchFake = (async (url: string | URL | Request) =>
      String(url).endsWith("/latest")
        ? new Response(null, { status: 302, headers: { location: "https://github.com/neoromantic/ai-usage/releases/tag/v0.11.0" } })
        : new Response('#!/bin/sh\necho "ai-usage install: ai-usage is installed with Homebrew" >&2\nexit 1\n')) as typeof fetch;
    const out = await install(machine({ ...linux, fetch: fetchFake }), { teamKey: null, relay: null, onLine: () => {} });
    expect(out.ok).toBe(false);
    expect(out.message).toBe("ai-usage is installed with Homebrew");
  });

  test("does not run on Windows", async () => {
    const out = await install(machine({ platform: "win32" }), { teamKey: null, relay: null, onLine: () => {} });
    expect(out.ok).toBe(false);
  });
});
