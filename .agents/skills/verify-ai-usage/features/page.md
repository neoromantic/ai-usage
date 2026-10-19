# The printed page

## Sub-features

- The header: device, team, collected and relay ages, release state
- ATTENTION: out, over, tight, error, no reading, silent, old, idle; only when something is wrong
- SUBSCRIPTIONS: each account's windows, bar, left, reset, forecast at reset, devices
- DEVICES × SUBSCRIPTIONS, the matrix, and DEVICES as status (`--devices`)
- UTILIZATION, how full each window got before its reset over 90 days
- PROJECTS (`--projects` for all) and USAGE; the first run's HOW IT WORKS guide
- Display: `--width 80..160`, `--ascii`, `--color`, `NO_COLOR`

## How to get to it (user POV)

`ai-usage` collects and shows it; `ai-usage report --plain` prints the last
one; `ai-usage report --from FILE` shows a saved `--json` report, such as
`docs/demo/team.json` (a team with every kind of trouble) or
`docs/demo/solo.json` (one developer).

## Driving it

`scripts/verify.sh page` prints both demo reports at 80 and 110 columns.
For another width or flag:
`TZ=Europe/Berlin /tmp/au report --from docs/demo/team.json --plain --width 160 --ascii`.

## Proof

The golden files in `internal/view/testdata` match (`go` step), and the
page in `.verify/latest/page/` reads right: words, order, alignment at 80
columns, no line past the width. A change to what the page says also
changes the README's pictures (`bun scripts/demo/shots.ts`).

## Try

- At 80 columns with `team.json`: does any row wrap or lose its RESETS?
- `--ascii` and `--color=never`: is every glyph replaced and no escape left?
- A report whose every window is fine: is ATTENTION gone, not empty?

## Gotchas

The demo is taken at Thu 24 Sep 2026 15:40 in Berlin; without
`TZ=Europe/Berlin` the times shift but nothing is wrong. `--from` refuses a
report of another `schema_version`: after a schema change, regenerate the
demo with `go run ./scripts/demo` (or `shots.ts`).
