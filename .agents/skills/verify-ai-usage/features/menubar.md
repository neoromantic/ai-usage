# Menu bar app

## Sub-features

- The ring in the menu bar and its states (`menubar-states-*.png`)
- The popover: the verdict line, Limits, Usage (by device and by subscription), Projects; a row's details; the alerts
- The ⋯ menu: status, Collect Now, Team Window…, Open in Terminal, Settings
- The popover's cost a month under its title, and the Limits details' cost line
- The Team window: Usage in tokens and shares, Status, Cost
- Settings: General, Accounts, Team, Cost
- States with no report: loading, not installed, not collected, mismatch, failed
- Notifications when a window is out or will run out, and of the cost: changes, a charge soon, questions with actions

## How to get to it (user POV)

`~/Applications/AI Usage.app`, installed by `install.sh` on macOS 14 and
later; it runs `ai-usage report --json` and shows it.

## Driving it

`scripts/verify.sh app` (checks, build) and `scripts/verify.sh pictures`
(every state from `team.json`, light and dark, in
`.verify/latest/app/team/`). [docs/demo/README.md](../../../../docs/demo/README.md)
names each picture. `AIUsageBar --demo docs/demo/team.json` runs it in the
menu bar for a person to click.

## Proof

`AIUsageChecks` passes, and the pictures of the states the change touches
read right in both looks: open them, do not only count them. The app
works out nothing: a number, forecast, or alert it shows is in the report's
JSON first ([json.md](json.md)).

## Try

- `popover-limits-expanded-over-*`: does the countdown match the page's ATTENTION?
- `popover-usage-filtered-*`: does each chip's filter show only its devices?
- `popover-mismatch-*` and `popover-failed-*`: does each say what to do?

## Gotchas

The renderer takes `generated_at` as now, so ages match the terminal
pictures. It opens the ⋯ menu on the screen for a moment; over SSH it says
so and draws the rest. Clicking the live popover takes the user's pointer:
ask first. The installed app keeps running; never kill `AIUsageBar` by
name, only the PID you started.
