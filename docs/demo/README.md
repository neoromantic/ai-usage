# Demo reports and screenshots

Every machine, person, account, and number here is made up. The pictures in the README and in posts come from these files, never from a real team.

## The reports

Two reports, saved as `ai-usage --json` prints them:

- **`solo.json`**: one developer, `mira-mbp`, with Claude Max, ChatGPT Pro, and SuperGrok. Claude runs out on Saturday, 12 hours before its weekly reset, and its Fable limit runs out tonight. Codex has room, and Grok is paid for but almost unused.
- **`team.json`**: a small studio. It has two laptops (`mira-mbp` and `leo-air`) and a Linux server, `build-01`. Nine Hermes bots run in containers of their own (`scout`, `editor`, `herald`, …), and all of them bill through one Codex login, `bots@studio.dev`. That login runs out on Friday, 2½ days before its reset. Claude's 5-hour window is full. Leo's Codex Plus is out, and his laptop runs an older release whose Codex fails. The `courier` bot has not reported since yesterday.

`scripts/demo/main.go` builds both with the code that builds a real report, from made-up readings and token logs. Their forecasts, ATTENTION, and matrix therefore agree with the numbers, as they would in a real run. Every report is taken at Thursday 24 September 2026, 15:40 in Berlin.

To show one in your own terminal, interactive view included:

```sh
ai-usage report --from docs/demo/team.json
TZ=Europe/Berlin ai-usage report --from docs/demo/solo.json --plain --width 110
```

`--from` shows any report saved with `--json`. It reads no state of this machine and collects nothing.

## The pictures

| File | What it shows |
| --- | --- |
| `team.png`, `team-light.png` | the whole team page, dark and light |
| `team-80.png` | the same page at 80 columns, the narrowest layout; tall, for phones |
| `team-attention.png` | ATTENTION and SUBSCRIPTIONS only |
| `team-matrix.png` | DEVICES × SUBSCRIPTIONS only: every machine and bot against every subscription |
| `team-bots.png` | the interactive view after `%`: each machine's share of each subscription, such as how the server and the nine bots split `bots` |
| `team-status.png`, `team-devices.png` | DEVICES as each machine's status: release, last report, tools, errors; interactive and printed |
| `devices-status.png` | the status view alone, printed with `--devices` |
| `team-help.png` | the interactive view's help, `?` |
| `solo.png` | one developer's page, with USAGE in place of the matrix |
| `solo-forecast.png` | the solo page's top: the forecast lines |
| `install.png` | `curl … \| sh` through the first report and the guide printed under it, for `solo.json`'s developer |
| `relay-view.png` | what the relay keeps for `mira-mbp`, trimmed: names, emails, and paths sealed, numbers plain |
| `json.png` | `ai-usage --json` through `jq`: what an agent reads |
| `menubar.png`, `menubar-light.png` | the macOS menu bar app for the team: its item in the menu bar, and under it the popover on Limits, dark and light; the README's picture of the app |
| `social/architecture.png` | how a team's numbers travel, drawn from `scripts/demo/diagram.html` |
| `social/*.png` | the pictures marked for posts, on a backdrop; the README shows these |
| `social/tour.mp4`, `social/tour.gif` | the interactive view, key by key: the page, `s` status, `%` share, `p` 30 days, `?` help |

The pictures are at twice the size of the page, drawn in JetBrains Mono. The menu bar app's are at twice its size in points, in the system font.

## Making them again

```sh
bun scripts/demo/shots.ts            # every picture
bun scripts/demo/shots.ts team-bots  # the pictures whose names start so
```

The script regenerates the two reports and builds `ai-usage`. It shows each picture's report the way a person would see it: printed, or in the interactive view in a tmux of its own, with keys pressed. The terminal's text and colors become an HTML page, which agent-browser photographs in Chrome. The PNGs are then redrawn in 256 colors, at about half the size. It needs Go, bun, tmux, agent-browser, jq, and ffmpeg.

- **Another picture:** add an entry to `shots` in `scripts/demo/shots.ts`: the report, the width, the flags or keys, and whether it is for posts and the README.
- **Another story:** add a report in `scripts/demo/main.go`: its machines, their logins and readings, and how much each spends a day on which project.
- **The first run and the relay's view:** `go run ./scripts/demo guide 110` prints the solo report with the guide a first run prints under it, and `go run ./scripts/demo snapshot` the snapshot `mira-mbp` would publish, sealed with a made-up team key. `install` and `relay-view` are made from them.
- **After a change to the report's schema:** run the script again. `--from` refuses a report of another `schema_version`.

The menu bar app draws its own pictures; `shots.ts` does not make them. `AIUsageBar --render REPORT OUTDIR` draws them in light and dark as PNGs into `OUTDIR`, and exits. It reads the report instead of running `ai-usage`, and takes its `generated_at` as the time now, so the ages and countdowns match the terminal pictures. For each look, `light` or `dark`, it writes:

- `popover-TAB-LOOK.png`, each tab of the popover as tall as the app shows it, and `popover-TAB-full-LOOK.png`, the whole tab. TAB is `subscriptions`, `usage`, `usage-share` (Usage in shares), `devices`, and `projects`; a report of one device has no `usage-share` or `devices`
- `popover-STATE-LOOK.png`, the popover with no report to show: `not-installed`, `not-collected`, `mismatch` (a report of another schema), and `failed`
- `settings-PANE-LOOK.png`, each Settings pane: `general`, `accounts`, and `team`
- `menubar-LOOK.png`, the app's item in the menu bar with the popover on Limits under it

```sh
cd macos
xcrun swift build --product AIUsageBar
TZ=Europe/Berlin "$(xcrun swift build --show-bin-path)/AIUsageBar" --render ../docs/demo/team.json /tmp/menubar
cp /tmp/menubar/menubar-dark.png ../docs/demo/menubar.png
cp /tmp/menubar/menubar-light.png ../docs/demo/menubar-light.png
```

It needs macOS 14 or later with Xcode or the Command Line Tools. `AIUsageBar --demo REPORT` runs the app in the menu bar on a report the same way, to try it by hand.
