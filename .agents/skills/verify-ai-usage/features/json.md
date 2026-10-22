# JSON for agents

## Sub-features

- `schema_version`, `generated_at`, `collector`, `attention`, `providers`, `projects`, `team`
- `team.devices[]` (each with `label`, `device`, `this_device`, `silent`, `old`), `team.matrix`, `team.providers`
- `cost`: totals, subscriptions, devices, questions, and events ([cost.md](cost.md))
- Every field and its meaning in `docs/json-schema.md`

## How to get to it (user POV)

`ai-usage --json` (collect, then print) or `ai-usage report --json`. The
menu bar app reads `report --json` every minute.

## Driving it

The JSON golden files in `internal/view/testdata/*.json.golden`;
`.verify/latest/stack/a.json` is a real one from two devices, and
`docs/demo/*.json` are the demo's.

## Proof

A field the app needs is in the Go JSON, described in
`docs/json-schema.md`, tested in Go, and its use checked in
`AIUsageChecks` (`app` step). `jq` on `stack/a.json` shows it from a real
run, not only in a golden file.

## Try

- `jq '.team.devices[] | {label, silent, old}' docs/demo/team.json`: does each match the page?
- A field the page shows but the JSON lacks: the app cannot show it.

## Gotchas

A change to the schema needs `schema_version` and the demo reports
regenerated, or `--from` and the app refuse them.
