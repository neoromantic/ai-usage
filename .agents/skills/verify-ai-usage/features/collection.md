# Collection

## Sub-features

- Token counts from the files Claude Code, the Claude app, Codex, Grok, and Hermes write (`internal/logs`)
- The account and quota, asked of each installed tool, which spends no tokens (`internal/probe`)
- Weighted tokens by kind and by hour (`internal/meter`)
- Harness homes: the default ones, `home add|remove`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `GROK_HOME`, `HERMES_HOME`
- BB's Account Pooler: probes drop its route from their environment (`internal/probe/pool.go`); a Codex rollout's `model_provider`, the run's own environment, and BB's `bb pool status --json` (`Env.PoolUse`) mark the device `pooled` (`internal/collect/pool.go`); a team device marked `pooled` turns OUTSIDE into POOLED (`pool_rise`, `internal/view/team.go`)
- Samples, quota cycles, the ledger, and the snapshot a run publishes (`internal/collect`, `internal/state`)

## How to get to it (user POV)

The scheduler runs `ai-usage collect --quiet` every 15 minutes; `ai-usage`
collects now. `ai-usage status` says how the last run went.

## Driving it

Only through Go tests: `internal/collect/world_test.go` fakes the harness
homes and the tools; `internal/probe/fake_test.go` fakes each tool's
answers; `internal/logs` reads fixture logs. There is no live proof on
this machine: a run with the real home reads real accounts and publishes
them to the real team.

## Proof

The package's tests pass, and a new kind of log or answer has a fixture of
its own, with made-up accounts and paths. When a change reaches the page or
the JSON, `go run ./scripts/demo` makes the demo reports with the same code,
so the `page` and `pictures` steps show it.

## Try

- A log line from a newer harness version: is it counted, or reported as malformed rather than dropped?
- A tool that hangs: does the probe time out and the run still publish?

## Gotchas

`AI_USAGE_TEST_HOLD_LOCK` exists for tests of the run lock. A dev build is
version `devel`; never let one publish to the real team.
