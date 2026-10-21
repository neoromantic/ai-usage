# Install, update, schedule

## Sub-features

- `install.sh` (macOS, Linux) and `install.ps1` (Windows): download, checksum, install, register, the app on macOS
- Self-update at each scheduled run from `releases/latest`, the app with it; a suffixed version never updates
- `schedule install|remove|status|run`: launchd, cron, Task Scheduler, or the foreground in a container; on macOS, status and the report read the job launchd has loaded (`launchctl print`), which a run of the agent cannot reload, not only the plist
- `ai-usage update`, `ai-usage version`
- Homebrew, from v0.8.0: `.github/scripts/formula.sh` writes the formula, `tap.sh` puts it in `neoromantic/homebrew-tap` after a release; a copy in a keg never replaces itself, registers brew's opt path, and `ai-usage update` runs `brew upgrade neoromantic/tap/ai-usage` (`cmd/ai-usage/brew.go`); a script copy hands the schedule to the user's own Homebrew copy, and `install.sh` stops at one
- `ai-usage app install`: the menu bar app of the binary's release, whatever installed it

## How to get to it (user POV)

The one-line install in the README, or `brew install neoromantic/tap/ai-usage`
and a first `ai-usage`; afterwards nothing, the scheduler and self-update
keep it current, or, for a Homebrew copy, `ai-usage update`.

## Driving it

`scripts/verify.sh --push` runs `install` when an installer,
`.github/scripts`, self-update, or scheduling changed; `scripts/verify.sh
install` runs it now. It builds this machine's release file (and the app on
macOS), serves it on 127.0.0.1, and installs it twice, fresh and upgrade,
in homes of its own; log in `.verify/latest/install.txt`.

## Proof

`internal/selfupdate` and `internal/schedule` tests pass, and the smoke
says `install-smoke: ok`. Windows installs only in CI. CI's `brew` job installs the formula from a
local build on macOS and Linux, checks the JSON says `homebrew`, that cron
runs the opt path, and runs `brew style` and `brew audit --strict`.

## Try

- An install over an older release: is the old binary replaced and the schedule kept?
- `AI_USAGE_NO_SCHEDULE=1`: is nothing registered?
- A formula from a local build in a throwaway tap (`brew tap-new --no-git`, a
  `file://` base for `formula.sh`): does `brew install` and `brew test` pass?
  Run only `version` with what it installed, then `brew uninstall` and
  `brew untap`.

## Gotchas

Never run `schedule install` or an installer against this machine's own
home: it replaces the user's ai-usage and its launchd agent. On macOS 27,
launchd pins the agent to the binary it registered; see `internal/schedule/launchd.go`
for how an update survives it. CI runs no macOS job; `scripts/verify.sh
--push` runs the install smoke on a Mac.
