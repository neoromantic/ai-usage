# Usage stats

## Sub-features

- `internal/telemetry/telemetry.go`: the shared client, a copy devkit keeps identical to its template; never edit it here
- `cmd/ai-usage/telemetry.go`: which commands record what (`statsCommands`, `textCommands`), the mode (`scheduled` for `collect --quiet`, `daemon` for `schedule run`, `server` for `relay serve`), the channel (`installChannel`), and errors by type alone where their text can repeat what was typed
- the notice: on stderr at the first run on a terminal or for an agent, never in a scheduled run; `ai-usage telemetry notice` for the installers; the app's banner in the popover with OK (`telemetry notice --seen`) and Turn Off
- `ai-usage telemetry [status] [--json] | on | off | notice`, and the hidden `record` the app uses for `app_started` and `feature_used` (`popover`, `team window`, `settings`)
- `AI_USAGE_CALLER`: the app's and BB's runs of ai-usage record nothing
- off switches: `ai-usage telemetry off`, Settings' Share Usage Stats, `DO_NOT_TRACK=1`, `AI_USAGE_TELEMETRY=0`; nothing in CI, tests, or a development build

## How to get to it (user POV)

The notice at the first `ai-usage`, in the installer, or in the app's popover;
then `ai-usage telemetry`, and Settings → General → Share Usage Stats in the app.

## Driving it

Never send from a dev build to the real endpoint by accident: a suffixed
version sends nothing unless `AI_USAGE_TELEMETRY` says so.
`AI_USAGE_TELEMETRY=debug` prints each event to stderr instead of sending
it, so in a made-up home:

```sh
go build -ldflags "-X main.version=v9.9.9" -o /tmp/au ./cmd/ai-usage
export AI_USAGE_HOME=/tmp/au-home AI_USAGE_NO_SCHEDULE=1
/tmp/au telemetry status --json       # notice due, id null
AI_AGENT=x AI_USAGE_TELEMETRY=debug /tmp/au name show   # prints the notice, sends nothing
AI_AGENT=x AI_USAGE_TELEMETRY=debug /tmp/au name show   # prints app_installed, app_started, feature_used
```

The app's banner is the renderer's `popover-usage-notice`, and Settings'
switch is in `settings-general`.

## Proof

`TestUsageStats*`, `TestInstallChannel`, and `TestStatsFlags` in
`cmd/ai-usage`, against a local server; `AIUsageChecks`' usage stats checks;
`bb/test/host.test.ts` for `AI_USAGE_CALLER=bb`.
