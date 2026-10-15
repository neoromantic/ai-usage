# Feature map

What a person uses in ai-usage, where its tests are, and the step of
`scripts/verify.sh` that shows it. A change to a feature is proven by its
tests and by reading that step's evidence. When a command, key, tab, or
field changes, change its file here in the same commit.

| Feature | How a person reaches it | Pinned by | Live proof |
| --- | --- | --- | --- |
| [The printed page](page.md) | `ai-usage`, `ai-usage report --plain` | `internal/view` golden files | `page` |
| [The interactive view](interactive.md) | `ai-usage` on a terminal | `internal/tui` tests | `page` (tmux) |
| [JSON for agents](json.md) | `ai-usage --json`, `report --json` | `internal/view` JSON goldens, `docs/json-schema.md` | `stack` (`a.json`), `app` |
| [Teams and the relay](team.md) | `team key`, `team join`, `name`, `alias`, `relay` | `relay/`, `cmd/ai-usage/team_test.go`, `internal/collect/team_test.go` | `stack` |
| [Collection](collection.md) | the scheduler every 15 minutes, `ai-usage collect` | `internal/collect`, `internal/logs`, `internal/probe` | none; Go tests only |
| [Menu bar app](menubar.md) | the ring in the menu bar, the popover, Team window, Settings | `AIUsageChecks` | `app`, `pictures` |
| [Install, update, schedule](install.md) | `install.sh`, `install.ps1`, `ai-usage update`, `schedule` | `internal/selfupdate`, `internal/schedule`, `install-smoke.sh` | `install` (`--push`) |
