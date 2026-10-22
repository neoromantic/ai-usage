# Cost

## Sub-features

- The price list: `internal/cost/prices.json`, built in, served by the relay at `GET /v1/prices`, fetched by a collection once a day; `ai-usage cost prices`
- The cost store, `cost.json` in the state folder: each subscription's plans, first and last seen, the answers people typed, skipped questions, news told
- COST after SUBSCRIPTIONS, and the team's total a month at the end of the header: PLAN, $/MO (`*` typed, `?` unknown, `typed for PLAN` stale), USED in $ and % over 4 weeks (`?` when no week is known), NEXT, and idle / still paid? / ended
- ATTENTION: IDLE with its price, CHECK (still paid?), COST (new, plan changed, list price changed) for 3 days
- DEVICES × SUBSCRIPTIONS in dollars (`$` in the interactive view), with `unused` and TOTAL rows, `~` on a split of a subscription whose Used is not known, and a line for what no device has; a subscription's and a device's detail in dollars
- Questions: the prompt line in the interactive view (Enter, `s`, `S`, `y`/`n`, `Esc`), the cadence before the charge day, a plan without its tier offered its tiers' prices, `e` to edit, the first run's questions after the guide
- `ai-usage cost [--json]`, `cost set|confirm|cancel|reset|skip|never-ask`
- `cost` in `report --json`: subscriptions, devices, questions, events
- Team sharing: typed answers sealed in the snapshot's `costs`, behind the v0.10.0 gate
- The menu bar app: the popover's total, the Team window on Cost, the Limits cost line, Settings → Cost, the cost notifications

## How to get to it (user POV)

`ai-usage` shows COST on the page; `ai-usage cost` prints it with what
each device used; `ai-usage cost set ACCOUNT --price N` types a price. In
the app, the total under the popover's title opens the Team window on Cost.

## Driving it

`docs/demo/team.json` has every state: active, idle, unconfirmed, ended,
typed, stale, unknown price, estimated, a yearly plan, and the questions
and events they bring; `TestTeamCost` in `scripts/demo` pins that. Print
it with `report --from docs/demo/team.json --plain`, and press `$` in the
interactive view. `--from` never asks questions and has no `e`: those need a
state folder. For the commands, use a made-up home as
[SKILL.md](../SKILL.md#driving-it-by-hand) says: `cost prices` works in an
empty one, but `cost set` needs an account the home knows, which a home
with no login has not got, so the numbers of a subscription are proven by
the demo report and the Go tests. The local relay of `scripts/verify.sh
stack` serves `/v1/prices` too: `curl -i` it for the `Cache-Control`.

## Proof

`internal/cost` tests (prices, store, wire, the numbers), the
`internal/view` goldens `cost-*`, `team-cost-*`, and `detail-cost-*`,
`internal/tui/cost_test.go`, `cmd/ai-usage/cost_test.go`,
`internal/collect/cost_test.go` (a teammate's answer through a real relay),
and `AIUsageChecks`. Read the COST section and the `$` matrix in
`.verify/latest/page/`, and `team-cost-*.png` and `settings-cost-*.png` in
`.verify/latest/app/team/`: each column of the `$` matrix adds up to its
price, and its TOTAL to the columns', with a line under it for what no
device has and the subscriptions without a price; the header's total is
COST's.

## Try

- A price typed for one plan, then a change of plan: does the row say `typed for` the old plan and count the list price?
- `cost cancel` on a subscription: does it leave every total and show `ended`, muted?
- `cost skip ACCOUNT renewal`, then a new plan: is the charge day asked again?
- A relay before v0.10.0 (404 on `/v1/prices`): does the run stay quiet and use the built-in list?
- A Claude Max read without its tier, as from an older release: does the price question offer Max 5x and Max 20x?
- A plan billed yearly: is the cadence asked before the day, with the next anniversary offered, and a day of the month refused?
- Cancelled from the app: does the app keep quiet about the ended event it caused?

## Gotchas

The demo is taken at 24 Sep 2026, but its prices are the built-in list's,
whose `updated` may be later. Prices are in USD a month; a yearly price
typed with `--yearly` is the year's, and the report shows a twelfth.
Hermes accounts cost nothing of their own: what they spend counts on the
login they borrow.
