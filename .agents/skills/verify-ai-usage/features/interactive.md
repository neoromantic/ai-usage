# The interactive view

## Sub-features

- The same page in a full-screen view, its header pinned, with a key bar at the bottom
- Scrolling: `j`/`k`, `space`/`pgdown`, `pgup`, `g`/`G`, the mouse wheel
- The matrix sideways: `h`/`l`, shift+wheel
- The period: `p` cycles, `1` today, `7` week, `3` month, `9` quarter
- `%` shares instead of tokens; `$` the matrix in dollars; `s` DEVICES as each device's status
- Cost questions on the line over the key bar, and `e` to edit a subscription's cost, with a state folder only, not `--from` ([cost.md](cost.md))
- `?` help, `esc` closes it (or quits), `q` quits, `r` refreshes
- Drill-down: `enter` picks a row (an ATTENTION line about one account or
  device, an account in SUBSCRIPTIONS, UTILIZATION, or USAGE, a device in
  DEVICES), `j`/`k` `g`/`G` move the pick, `enter` or a click on the picked
  row opens its detail, `esc`/`backspace` go back

## How to get to it (user POV)

`ai-usage` or `ai-usage report` on a terminal, without `--plain`.

## Driving it

`scripts/verify.sh page` drives `team.json` in a tmux of its own and keeps
each screen in `.verify/latest/page/tui-*.txt`. For other keys, the tmux
recipe in [SKILL.md](../SKILL.md#driving-it-by-hand); `capture-pane -e`
keeps the colors.

## Proof

`internal/tui` tests (key bar, periods, share, device views, scroll) pass,
and the captured screen after each key shows the change: the period in the
key bar, the matrix in shares, DEVICES as status, the help's STATES, a
row picked, a device's detail, and the page as it was after `esc`.
`internal/view`'s `detail-*` golden files are the detail pages.

## Try

- `%` then `s` then `%`: is share mode kept or explained in the status view?
- At 80 columns, `l` to the last column of the matrix, then `7`: does the matrix keep its place?
- `?`, scroll the help to its end, `esc`: is the page where it was?
- `enter`, `G`, `enter`, then `3`, then `esc`: is the detail by 30d, and is the
  same device picked and in sight on the page, reordered by 30d?
- At 12 rows, `enter` and `j` down through DEVICES: is the picked row ever
  hidden under the pinned head?
- `enter`, then the wheel or `space` down into PROJECTS, then `r`: does the
  page stay where it was while the spinner runs?

## Gotchas

`TestRun` depends on timing and fails now and then on a busy machine;
`scripts/verify.sh` reruns it once. The view needs a terminal: without
one, `ai-usage` prints the page instead, which is not a bug.
