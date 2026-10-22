# Demo reports and screenshots

Every machine, person, account, and number here is made up. The pictures in the README and in posts come from these files, never from a real team.

## The reports

Three reports, saved as `ai-usage --json` prints them:

- **`solo.json`**: one developer, `mira-mbp`, with Claude Max, ChatGPT Pro, and SuperGrok. Claude runs out on Saturday, 12 hours before its weekly reset, and its Fable limit runs out tonight. Codex has room, and Grok is paid for but almost unused. Over the quarter, Claude's week ran out 5 times in 13, and once she was away from the laptop for half a week (`~`); her Codex and Grok never got past 71%. She told ai-usage when Claude and Codex charge, so COST says the next charge, Claude's on Saturday, and asks when SuperGrok does, monthly or yearly.
- **`team.json`**: a small studio. It has two laptops (`mira-mbp` and `leo-air`) and a Linux server, `build-01`. Nine Hermes bots run in containers of their own (`scout`, `editor`, `herald`, …), and all of them bill through one Codex login, `bots@studio.dev`. That login runs out on Friday, nearly 3 days before its reset. Mira's Claude 5-hour window is full, and at the last day's pace her week runs out on Saturday; her Codex week ends tight. Leo's Codex Plus is out, and his laptop runs v0.3.4, an older release whose Codex fails and which sends no weighted tokens, so its totals are estimated (`~`). Since that release reads no quota cycles, no machine sends them, and UTILIZATION shows Mira's own, with a line saying why. The `courier` bot has not reported since yesterday. A triage job on `build-01` runs Claude Code in a folder of its own that nobody logged in to, so its tokens go to `unknown`, and ATTENTION says where to log in. Mira stopped using SuperGrok nine days ago, and ATTENTION marks it idle: paid and unused, $25 a month, as she pays it yearly.

  COST has every state a subscription can be in. Mira types prices with VAT, as the studio pays them: the bots' Codex Pro at $238 (`*`), and her Claude Max 5x at $119, which she moved to Max 20x two days ago, so that price is for another plan (`typed for max 5x`), the list's $200 applies, and ATTENTION says the plan changed. Her Codex charges on Sunday, so it is next, and her Claude's charge day is a question, with the day its tool says it began. Leo's Claude has no price (`?`), since his old release reads no tier. Jun left the studio in August, and nobody has said whether his Claude Pro is still paid, so ATTENTION asks (CHECK); the CI's ChatGPT Plus was cancelled ten days ago and shows as ended. The devices that send no quota cycles leave the Used of their subscriptions unknown, so it is their whole price, split by tokens (`~`).
- **`homebrew.json`**: `solo.json`'s laptop where Homebrew installed ai-usage, and v0.5.1 is out, which brew has not installed yet: `collector.update` has `"method": "homebrew"` and `"command": "brew upgrade neoromantic/tap/ai-usage"`. The menu bar app's checks read it, and `AIUsageBar --render docs/demo/homebrew.json OUTDIR` draws Settings' About with "Command-Line Tool (Homebrew)" at `/opt/homebrew/opt/ai-usage/bin/ai-usage`, the path Homebrew keeps for it, which the launch agent names. Every render's `popover-not-installed` shows the Homebrew command under the installer's.

`scripts/demo/main.go` builds them with the code that builds a real report, from made-up readings and token logs. Their forecasts, ATTENTION, and matrix therefore agree with the numbers, as they would in a real run. Every report is taken at Thursday 24 September 2026, 15:40 in Berlin.

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
| `team-bots.png` | the interactive view after `%` and `G`: each machine's share of each subscription, such as how the server and the nine bots split `bots` |
| `team-cost.png` | COST only: each subscription's price a month and where it comes from, what the team used of it over the last 4 weeks, the next charge, and its state |
| `team-dollars.png` | the interactive view after `$` and `G`: what each machine used of each subscription in dollars over 4 weeks, what nobody used, and each price |
| `team-status.png`, `team-devices.png` | DEVICES as each machine's status: release, last report, tools, errors; interactive and printed |
| `devices-status.png` | the status view alone, printed with `--devices` |
| `team-help.png` | the interactive view's help, `?` |
| `team-detail.png` | the interactive view after `Enter`, `j`, `j`, `Enter`: the detail of `bots@studio.dev`, the Codex login the server and the nine bots share, with each window's forecast and when it runs out, and each machine's share of it |
| `solo.png` | one developer's page, with USAGE in place of the matrix |
| `solo-forecast.png` | the solo page's top: the forecast lines |
| `solo-utilization.png` | UTILIZATION alone: how full each window got before each reset over the quarter |
| `install.png` | `curl … \| sh` through the first report and the guide printed under it, for `solo.json`'s developer |
| `relay-view.png` | what the relay keeps for `mira-mbp`, trimmed: names, emails, and paths sealed, numbers plain |
| `json.png` | `ai-usage --json` through `jq`: what an agent reads |
| `menubar.png`, `menubar-light.png` | the macOS menu bar app for the team: its item in the menu bar, and under it the popover on Limits, dark and light; the README's picture of the app |
| `app-limits.png`, `app-usage.png`, `app-projects.png` | the app's three tabs, each with `-light.png` beside it |
| `app-alerts.png`, `app-alerts-light.png` | the app's alerts, every entry of ATTENTION in place of Limits, with the subscription with the most room under them |
| `app-details.png`, `app-details-light.png` | the app's Limits with the details of a subscription that runs out open: its countdown, each window, the forecast, the bot that took its reading, who spent the window, and when it was last used |
| `app-menubar.png`, `app-menubar-light.png` | the app's item in the menu bar in each state: on track, tight, used up, stale, over, out, time until reset, no reading, and the ring alone |
| `app-menu.png`, `app-menu-light.png` | the app's menu, opened from the popover's ⋯: the collector's status and this Mac, then its actions, Terminal's views, Settings, and Quit, each under its header |
| `app-team.png`, `app-team-light.png` | the app's Team window on Usage: every device against every subscription over 7 days, shaded by tokens |
| `app-team-status.png`, `app-team-status-light.png` | the app's Team window on Status: each device's release, last report, tools, tokens in each period, and note |
| `app-team-cost.png`, `app-team-cost-light.png` | the app's Team window on Cost: what each device used of each subscription in dollars, what nobody used, and each price |
| `app-settings.png`, `app-settings-light.png` | the app's Settings on General: what the menu bar shows and tracks, notifications, this Mac's name and opening at login, and About: the app's version and where the command-line tool is |
| `app-settings-cost.png`, `app-settings-cost-light.png` | the app's Settings on Cost: this Mac's questions on top, then each subscription with its plan, price, Used, next charge, and state, and the cost notifications |
| `social/app-tabs.png` | the app's three tabs side by side on a backdrop, for posts |
| `social/architecture.png` | how a team's numbers travel, drawn from `scripts/demo/diagram.html` |
| `social/*.png` | the pictures marked for posts, on a backdrop; the README shows these |
| `social/tour.mp4`, `social/tour.gif` | the interactive view, key by key: the page, `s` status, `%` share, `$` dollars, `Enter` the detail of the bots' Codex login, `p` 30 days, `?` help |

The pictures are at twice the size of the page, drawn in JetBrains Mono. The menu bar app's are at twice its size in points, in the system font.

## Making them again

```sh
bun scripts/demo/shots.ts            # every picture
bun scripts/demo/shots.ts team-bots  # the pictures whose names start so
```

The script regenerates the reports and builds `ai-usage`. It shows each picture's report the way a person would see it: printed, or in the interactive view in a tmux of its own, with keys pressed. The terminal's text and colors become an HTML page, which agent-browser photographs in Chrome. The PNGs are then redrawn in 256 colors, at about half the size. It needs Go, bun, tmux, agent-browser, jq, and ffmpeg, and on macOS Xcode or the Command Line Tools for the app's pictures.

- **Another picture:** add an entry to `shots` in `scripts/demo/shots.ts`: the report, the width, the flags or keys, and whether it is for posts and the README.
- **Another story:** add a report in `scripts/demo/main.go`: its machines, their logins and readings, how full their windows got before each reset, and how much each spends a day on which project.
- **The first run and the relay's view:** `go run ./scripts/demo guide 110` prints the solo report with the guide a first run prints under it, and `go run ./scripts/demo snapshot` the snapshot `mira-mbp` would publish, sealed with a made-up team key. `install` and `relay-view` are made from them.
- **After a change to the report's schema:** run the script again. `--from` refuses a report of another `schema_version`.

The menu bar app draws its own pictures with `AIUsageBar --render REPORT OUTDIR`, which writes them in light and dark as PNGs into `OUTDIR` and exits. It reads the report instead of running `ai-usage`, and takes its `generated_at` as the time now, so the ages and countdowns match the terminal pictures. On macOS, `shots.ts` runs it on `team.json` and copies the pictures above into place; `bun scripts/demo/shots.ts app` makes only those. For each look, `light` or `dark`, the renderer writes:

- `popover-TAB-LOOK.png`, each tab as tall as the popover shows it, and `popover-TAB-full-LOOK.png`, the whole tab. TAB is `limits`, `usage`, `usage-subscriptions` (Usage by subscription), and `projects`; a report of one device has no `usage-subscriptions`
- `popover-STATE-LOOK.png`, the popover after a click: `limits-expanded`, `usage-expanded`, and `projects-expanded`, a row opened; `usage-filtered`, `usage-filtered-silent`, and `usage-filtered-not-updating`, Usage after a click on the chip of devices with errors, not reporting, or not updating, from Usage by subscription; `alerts`, the alerts after a click on the verdict's link; `limits-expanded-over` and `limits-expanded-idle`, Limits as tall as it is with the details of the first subscription that runs out or is idle open; `limits-unread`, Limits as tall as it is with the line of the subscriptions not read open; `limits-stale`, Limits with a reading of the subscription that runs out 36 hours old, so its row says it ran out; and `calm`, the popover with nothing out or running out
- `popover-STATE-LOOK.png`, the popover with no report to show: `loading`, `not-installed`, `not-collected`, `mismatch` (a report of another schema), and `failed`
- `team-usage-LOOK.png`, `team-share-LOOK.png`, `team-status-LOOK.png`, and `team-cost-LOOK.png`, the Team window on Usage in tokens and in shares, on Status, and on Cost, which only a report with a cost has; a report of one device has none
- `settings-PANE-LOOK.png`, each Settings pane: `general`, `accounts`, `team`, and `cost`
- `menubar-LOOK.png`, the app's item in the menu bar with the popover on Limits under it, and `menubar-states-LOOK.png`, the item in each state
- `menu-LOOK.png`, the menu that ⋯ opens. AppKit draws a menu only while it is open, so it shows on the screen for a moment, in the corner away from the pointer, and is drawn from its window; with no screen to show it on, as over SSH, the renderer says so and draws the rest

```sh
cd macos
xcrun swift build --product AIUsageBar
TZ=Europe/Berlin "$(xcrun swift build --show-bin-path)/AIUsageBar" --render ../docs/demo/team.json /tmp/menubar
```

It needs macOS 14 or later with Xcode or the Command Line Tools. `AIUsageBar --demo REPORT` runs the app in the menu bar on a report the same way, to try it by hand.
