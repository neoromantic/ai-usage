// Screenshots of the demo reports, for the README and for posts.
//
//   bun scripts/demo/shots.ts            # every shot
//   bun scripts/demo/shots.ts team solo  # shots whose name starts so
//
// It regenerates docs/demo/*.json with `go run ./scripts/demo`, builds
// ai-usage, and shows each report the way a person would see it: printed,
// or in the interactive view inside a tmux of its own. The terminal's text
// and colors become an HTML page, and agent-browser photographs it.
// Needs go, tmux, agent-browser, and ffmpeg for the tour video.

import { $ } from "bun";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dir, "../..");
const out = join(root, "docs/demo");
const tmp = mkdtempSync(join(tmpdir(), "ai-usage-shots-"));
const bin = join(tmp, "ai-usage");
const session = "ai-usage-shots";
const tmuxSocket = "ai-usage-shots";

type Theme = "dark" | "light";

type Shot = {
  name: string;
  // The report under docs/demo, or any saved --json.
  from: string;
  cols: number;
  // What the prompt shows was typed; the report is printed below it.
  typed?: string;
  args?: string[];
  // Keys pressed in the interactive view before the picture, rather than
  // printing. rows is the terminal's height then.
  keys?: string[];
  rows?: number;
  // Only lines [first, last) of the printed report.
  lines?: [number, number];
  // Only the section from the line that starts with the first text to the
  // line before the one that starts with the second.
  cut?: [string, string];
  // A shell line run instead of ai-usage, with $AI_USAGE as the binary.
  shell?: string;
  theme?: Theme;
  // Also a picture on a backdrop, sized for a post.
  social?: boolean;
};

const shots: Shot[] = [
  { name: "team", from: "team.json", cols: 124, typed: "ai-usage", social: true },
  { name: "team-light", from: "team.json", cols: 124, typed: "ai-usage", theme: "light" },
  { name: "team-devices", from: "team.json", cols: 124, typed: "ai-usage --devices", args: ["--devices"] },
  { name: "team-80", from: "team.json", cols: 80, typed: "ai-usage" },
  { name: "team-attention", from: "team.json", cols: 124, typed: "ai-usage", lines: [0, 25], social: true },
  { name: "team-matrix", from: "team.json", cols: 124, typed: "ai-usage", cut: ["DEVICES", "PROJECTS"], social: true },
  { name: "team-bots", from: "team.json", cols: 124, keys: ["%"], rows: 52, social: true },
  { name: "team-status", from: "team.json", cols: 124, keys: ["s"], rows: 52 },
  {
    name: "devices-status",
    from: "team.json",
    cols: 124,
    typed: "ai-usage --devices",
    args: ["--devices"],
    cut: ["DEVICES", "PROJECTS"],
    social: true,
  },
  { name: "team-help", from: "team.json", cols: 124, keys: ["?"], rows: 40 },
  { name: "solo", from: "solo.json", cols: 110, typed: "ai-usage", social: true },
  { name: "solo-forecast", from: "solo.json", cols: 110, typed: "ai-usage", lines: [0, 19], social: true },
  {
    name: "install",
    from: "solo.json",
    cols: 110,
    typed: "curl -fsSL https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh | sh",
    shell: `printf '%s\n' 'ai-usage install: downloading ai-usage_darwin_arm64' \
      'ai-usage install: downloading ai-usage_darwin_app.zip' \
      'ai-usage install: installed v0.3.3 to /Users/mira/.local/bin/ai-usage' \
      'ai-usage install: first run: collecting and registering with the system scheduler'
      go run ./scripts/demo guide 110
      printf '%s\n' 'ai-usage install: installed the menu bar app, AI Usage 0.3.3, to /Users/mira/Applications/AI Usage.app' \
      'ai-usage install: the app is in the menu bar; it starts at login and updates with ai-usage'`,
    social: true,
  },
  {
    name: "relay-view",
    from: "solo.json",
    cols: 100,
    typed: "# what the relay keeps for mira-mbp, trimmed",
    shell: `go run ./scripts/demo snapshot | jq -C '{collector_version, device_label, os_user, account: .accounts[0] | {provider, label, plan, windows: [.windows[] | {name, percent}], sessions, projects: [.projects[0:2][].path]}}'`,
    social: true,
  },
  {
    name: "json",
    from: "team.json",
    cols: 122,
    typed: `ai-usage --json | jq -c '.team.providers[] | .provider as $p | .accounts[] | select(.subscription) | {$p, name, state}'`,
    shell: `"$AI_USAGE" report --from docs/demo/team.json --json | jq -C -c '.team.providers[] | .provider as $p | .accounts[] | select(.subscription) | {$p, name, state}'`,
    social: true,
  },
];

// The tour is the interactive view, key after key, as a video.
const tour = { from: "team.json", cols: 124, rows: 52, steps: [[], ["s"], ["%"], ["p", "p"], ["?"]] as string[][], seconds: 2.6 };

const env = {
  ...process.env,
  TZ: "Europe/Berlin",
  LANG: "en_US.UTF-8",
  LC_ALL: "en_US.UTF-8",
  TERM: "xterm-256color",
  COLORTERM: "truecolor",
  NO_COLOR: "",
};

const palette = {
  dark: {
    bg: "#0f1115", fg: "#d7dae0", bar: "#1a1d23", border: "#2b2f37", dot: ["#ff5f57", "#febc2e", "#28c840"],
    prompt: "#5fafff", backdrop: "linear-gradient(135deg, #1e2a4a 0%, #3b1f4a 55%, #4a2a1f 100%)",
    ansi: ["#2b2f37", "#ff5f5f", "#87d75f", "#ffd75f", "#5fafff", "#d787ff", "#5fd7d7", "#d7dae0",
      "#6c7280", "#ff8787", "#afff87", "#ffff87", "#87d7ff", "#ffafff", "#87ffff", "#ffffff"],
  },
  light: {
    bg: "#ffffff", fg: "#1f2328", bar: "#eef0f3", border: "#d0d7de", dot: ["#ff5f57", "#febc2e", "#28c840"],
    prompt: "#0087af", backdrop: "linear-gradient(135deg, #dfe8ff 0%, #f3e3ff 55%, #ffe9dc 100%)",
    ansi: ["#1f2328", "#d70000", "#008700", "#af8700", "#005fd7", "#8700af", "#008787", "#6c6c6c",
      "#8a8a8a", "#ff0000", "#00af00", "#d7af00", "#0087ff", "#af00d7", "#00afaf", "#1f2328"],
  },
};

function xterm256(n: number, theme: Theme): string {
  if (n < 16) return palette[theme].ansi[n];
  if (n >= 232) {
    const v = 8 + (n - 232) * 10;
    return hex(v, v, v);
  }
  const i = n - 16;
  const step = (c: number) => (c === 0 ? 0 : 55 + c * 40);
  return hex(step(Math.floor(i / 36)), step(Math.floor(i / 6) % 6), step(i % 6));
}

const hex = (r: number, g: number, b: number) => "#" + [r, g, b].map((v) => v.toString(16).padStart(2, "0")).join("");

type Style = { fg?: string; bg?: string; bold?: boolean; faint?: boolean; italic?: boolean; under?: boolean; reverse?: boolean };

function sgr(codes: number[], s: Style, theme: Theme): Style {
  s = { ...s };
  if (codes.length === 0) codes = [0];
  for (let i = 0; i < codes.length; i++) {
    const c = codes[i];
    if (c === 0) s = {};
    else if (c === 1) s.bold = true;
    else if (c === 2) s.faint = true;
    else if (c === 3) s.italic = true;
    else if (c === 4) s.under = true;
    else if (c === 7) s.reverse = true;
    else if (c === 22) s.bold = s.faint = false;
    else if (c === 23) s.italic = false;
    else if (c === 24) s.under = false;
    else if (c === 27) s.reverse = false;
    else if (c >= 30 && c <= 37) s.fg = palette[theme].ansi[c - 30];
    else if (c >= 90 && c <= 97) s.fg = palette[theme].ansi[c - 90 + 8];
    else if (c === 39) s.fg = undefined;
    else if (c >= 40 && c <= 47) s.bg = palette[theme].ansi[c - 40];
    else if (c >= 100 && c <= 107) s.bg = palette[theme].ansi[c - 100 + 8];
    else if (c === 49) s.bg = undefined;
    else if (c === 38 || c === 48) {
      let color: string | undefined;
      if (codes[i + 1] === 5) {
        color = xterm256(codes[i + 2], theme);
        i += 2;
      } else if (codes[i + 1] === 2) {
        color = hex(codes[i + 2], codes[i + 3], codes[i + 4]);
        i += 4;
      }
      if (c === 38) s.fg = color;
      else s.bg = color;
    }
  }
  return s;
}

const esc = (t: string) => t.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

// html is the terminal's text as lines of styled spans.
function html(text: string, theme: Theme): string {
  const p = palette[theme];
  text = text.replace(/\x1b\][^\x07\x1b]*(\x07|\x1b\\)/g, "").replace(/\x1b\[[0-9;?]*[A-La-ln-z]/g, "");
  let style: Style = {};
  const lines: string[] = [];
  for (const line of text.replace(/\n+$/, "").split("\n")) {
    let out = "";
    const parts = line.split(/\x1b\[([0-9;:]*)m/);
    for (let i = 0; i < parts.length; i++) {
      if (i % 2 === 1) {
        style = sgr(parts[i] === "" ? [] : parts[i].split(/[;:]/).map(Number), style, theme);
        continue;
      }
      if (parts[i] === "") continue;
      let fg = style.fg ?? p.fg;
      let bg = style.bg;
      if (style.reverse) [fg, bg] = [bg ?? p.bg, fg];
      const css = [
        `color:${fg}`,
        bg ? `background:${bg}` : "",
        style.bold ? "font-weight:700" : "",
        style.faint ? "opacity:.6" : "",
        style.italic ? "font-style:italic" : "",
        style.under ? "text-decoration:underline" : "",
      ].filter(Boolean).join(";");
      out += `<span style="${css}">${esc(parts[i])}</span>`;
    }
    lines.push(out || " ");
  }
  return lines.map((l) => `<div class="l">${l}</div>`).join("");
}

function page(body: string, cols: number, theme: Theme, title: string): string {
  const p = palette[theme];
  return `<!doctype html><meta charset="utf-8">
<link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;700&display=block" rel="stylesheet">
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:${p.bg};font:14px/1.32 "JetBrains Mono",Menlo,monospace;font-variant-ligatures:none;-webkit-font-smoothing:antialiased}
.frame{display:inline-block;padding:56px 64px;background:${p.backdrop}}
.window{display:inline-block;background:${p.bg};color:${p.fg};border:1px solid ${p.border};border-radius:10px;overflow:hidden;box-shadow:0 24px 64px rgba(0,0,0,.35)}
.bar{height:34px;background:${p.bar};border-bottom:1px solid ${p.border};display:flex;align-items:center;padding:0 14px;gap:8px;position:relative}
.bar i{width:12px;height:12px;border-radius:50%;display:block}
.bar span{position:absolute;left:0;right:0;text-align:center;font:12px -apple-system,BlinkMacSystemFont,sans-serif;color:${theme === "dark" ? "#8b919c" : "#57606a"}}
.term{padding:14px 18px 16px;width:calc(${cols}ch + 36px);white-space:pre}
.l{height:1.32em}
.prompt{color:${p.prompt};font-weight:700}
</style>
<body><div class="frame"><div class="window"><div class="bar">${p.dot.map((c) => `<i style="background:${c}"></i>`).join("")}<span>${esc(title)}</span></div>
<div class="term">${body}</div></div></div>
<script>document.fonts.ready.then(()=>document.body.classList.add("ready"))</script>`;
}

const tmux = (...args: string[]) => $`tmux -L ${tmuxSocket} -f /dev/null ${args}`.env(env).quiet().nothrow();
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// view runs the interactive view in a terminal cols by rows, presses keys,
// and returns the screen.
async function view(from: string, cols: number, rows: number, theme: Theme, keys: string[]): Promise<string> {
  await tmux("kill-server");
  const fgbg = theme === "dark" ? "15;0" : "0;15";
  const cmd = `${bin} report --from ${join(out, from)}; sleep 600`;
  await tmux("new-session", "-d", "-s", "shot", "-x", String(cols), "-y", String(rows),
    "-e", `TZ=${env.TZ}`, "-e", "COLORTERM=truecolor", "-e", `COLORFGBG=${fgbg}`, "-e", `LANG=${env.LANG}`, cmd);
  await sleep(1200);
  for (const k of keys) {
    await tmux("send-keys", "-t", "shot", "-l", k);
    await sleep(400);
  }
  await sleep(500);
  const screen = (await tmux("capture-pane", "-p", "-e", "-t", "shot")).text();
  await tmux("kill-server");
  return screen;
}

async function printed(s: Shot, theme: Theme): Promise<string> {
  const fgbg = theme === "dark" ? "15;0" : "0;15";
  const e = { ...env, COLORFGBG: fgbg, AI_USAGE: bin };
  let text: string;
  if (s.shell) {
    text = await $`sh -c ${s.shell}`.cwd(root).env(e).text();
  } else {
    text = await $`${bin} report --from ${join(out, s.from)} --width ${s.cols} --color always --plain ${s.args ?? []}`.env(e).text();
  }
  if (s.lines) text = text.split("\n").slice(...s.lines).join("\n");
  if (s.cut) {
    const lines = text.split("\n");
    const plain = lines.map((l) => l.replace(/\x1b\[[0-9;]*m/g, ""));
    const first = plain.findIndex((l) => l.startsWith(s.cut![0]));
    const last = plain.findIndex((l, i) => i > first && l.startsWith(s.cut![1]));
    text = lines.slice(first, last < 0 ? undefined : last).join("\n");
  }
  return text;
}

async function browser(...args: string[]) {
  const r = await $`agent-browser --engine chrome --session ${session} ${args}`.quiet().nothrow();
  if (r.exitCode !== 0) throw new Error(`agent-browser ${args.join(" ")}: ${r.stderr.toString() || r.stdout.toString()}`);
  return r.stdout.toString();
}

async function photograph(file: string, pngs: { selector: string; path: string }[]) {
  await browser("open", "file://" + file);
  await browser("wait", "body.ready");
  for (const p of pngs) {
    await browser("screenshot", p.selector, p.path);
    if (p.path.startsWith(out)) await shrink(p.path);
  }
}

// shrink redraws a picture in 256 colors, which a terminal's needs, at
// about half the size; the backdrop's gradient is dithered.
async function shrink(path: string) {
  const dither = path.includes("/social/") ? "sierra2_4a" : "none";
  const tmpPng = path + ".tmp.png";
  await $`ffmpeg -y -loglevel error -i ${path} -vf ${`split[a][b];[a]palettegen=max_colors=256:reserve_transparent=0[p];[b][p]paletteuse=dither=${dither}`} ${tmpPng}`;
  await $`mv -f ${tmpPng} ${path}`;
}

async function shoot(s: Shot) {
  const theme = s.theme ?? "dark";
  let body: string;
  let title = "ai-usage";
  if (s.keys) {
    body = html(await view(s.from, s.cols, s.rows ?? 50, theme, s.keys), theme);
  } else {
    const typed = `<div class="l"><span class="prompt">❯ </span><span>${esc(s.typed ?? "ai-usage")}</span></div>`;
    body = typed + html(await printed(s, theme), theme);
  }
  const file = join(tmp, s.name + ".html");
  writeFileSync(file, page(body, s.cols, theme, title));
  const pngs = [{ selector: ".window", path: join(out, s.name + ".png") }];
  if (s.social) pngs.push({ selector: ".frame", path: join(out, "social", s.name + ".png") });
  await photograph(file, pngs);
  console.log(pngs.map((p) => p.path.replace(root + "/", "")).join("\n"));
}

async function shootTour() {
  const frames: string[] = [];
  for (const [i, keys] of tour.steps.entries()) {
    const body = html(await view(tour.from, tour.cols, tour.rows, "dark", keys), "dark");
    const file = join(tmp, `tour-${i}.html`);
    writeFileSync(file, page(body, tour.cols, "dark", "ai-usage"));
    const png = join(tmp, `tour-${i}.png`);
    await photograph(file, [{ selector: ".frame", path: png }]);
    frames.push(png);
  }
  const list = join(tmp, "tour.txt");
  writeFileSync(list, frames.map((f) => `file '${f}'\nduration ${tour.seconds}`).join("\n") + `\nfile '${frames.at(-1)}'\n`);
  const mp4 = join(out, "social", "tour.mp4");
  const gif = join(out, "social", "tour.gif");
  const scale = "scale=trunc(iw/4)*2:trunc(ih/4)*2";
  await $`ffmpeg -y -loglevel error -f concat -safe 0 -i ${list} -vf ${scale + ",format=yuv420p"} -r 30 -c:v libx264 -crf 20 -movflags +faststart ${mp4}`;
  await $`ffmpeg -y -loglevel error -f concat -safe 0 -i ${list} -vf ${"scale=1200:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128[p];[b][p]paletteuse=dither=none"} ${gif}`;
  console.log([mp4, gif].map((p) => p.replace(root + "/", "")).join("\n"));
}

// app draws the macOS menu bar app with its own renderer: its item in the
// menu bar over the popover, the popover's tabs, and the menu bar's looks,
// each dark and light, and for posts the three tabs side by side on the
// backdrop. Only on macOS, with Xcode or the Command Line Tools.
async function app() {
  if (process.platform !== "darwin") return;
  const macos = join(root, "macos");
  await $`xcrun swift build --product AIUsageBar`.cwd(macos).quiet();
  const exe = join((await $`xcrun swift build --show-bin-path`.cwd(macos).text()).trim(), "AIUsageBar");
  const drawn = join(tmp, "app");
  await $`${exe} --render ${join(out, "team.json")} ${drawn}`.env(env).quiet();
  const pictures: [string, string][] = [
    ["menubar", "menubar"],
    ["popover-limits", "app-limits"],
    ["popover-usage", "app-usage"],
    ["popover-projects", "app-projects"],
    ["menubar-states", "app-menubar"],
  ];
  for (const [from, to] of pictures) {
    await $`cp -f ${join(drawn, from + "-dark.png")} ${join(out, to + ".png")}`;
    await $`cp -f ${join(drawn, from + "-light.png")} ${join(out, to + "-light.png")}`;
  }
  const tabs = ["limits", "usage", "projects"].map((t) => `<img src="${join(drawn, `popover-${t}-dark.png`)}">`).join("");
  const file = join(tmp, "app.html");
  writeFileSync(file, `<!doctype html><meta charset="utf-8"><style>
*{margin:0;padding:0;box-sizing:border-box}
.frame{display:inline-flex;gap:40px;align-items:flex-start;padding:64px 72px;background:${palette.dark.backdrop}}
img{width:400px;border-radius:12px;box-shadow:0 24px 64px rgba(0,0,0,.45)}
</style><body><div class="frame">${tabs}</div>
<script>Promise.all([...document.images].map((i)=>i.decode())).then(()=>document.body.classList.add("ready"))</script>`);
  const png = join(tmp, "app-tabs.png");
  await photograph(file, [{ selector: ".frame", path: png }]);
  await $`cp -f ${png} ${join(out, "social", "app-tabs.png")}`;
  console.log(["menubar", ...pictures.slice(1).map((p) => p[1])].map((n) => `docs/demo/${n}.png`).concat("docs/demo/social/app-tabs.png").join("\n"));
}

// diagram photographs scripts/demo/diagram.html, the architecture.
async function diagram() {
  const path = join(out, "social", "architecture.png");
  await photograph(join(import.meta.dir, "diagram.html"), [{ selector: ".frame", path }]);
  console.log(path.replace(root + "/", ""));
}

const only = process.argv.slice(2);
const wanted = (name: string) => only.length === 0 || only.some((o) => name.startsWith(o));
try {
  await $`go run ./scripts/demo docs/demo`.cwd(root);
  await $`go build -o ${bin} ./cmd/ai-usage`.cwd(root);
  mkdirSync(join(out, "social"), { recursive: true });
  await browser("set", "viewport", "1600", "2400", "2");
  for (const s of shots) if (wanted(s.name)) await shoot(s);
  if (wanted("architecture")) await diagram();
  if (wanted("app") || wanted("menubar")) await app();
  if (wanted("tour")) await shootTour();
} finally {
  await tmux("kill-server");
  await $`agent-browser --session ${session} close`.quiet().nothrow();
  rmSync(tmp, { recursive: true, force: true });
}
