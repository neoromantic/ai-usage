# Install, update, schedule

## Sub-features

- `install.sh` (macOS, Linux) and `install.ps1` (Windows): download, checksum, install, register, the app on macOS
- Self-update at each scheduled run from `releases/latest`, the app with it; a suffixed version never updates
- `schedule install|remove|status|run`: launchd, cron, Task Scheduler, or the foreground in a container
- `ai-usage update`, `ai-usage version`

## How to get to it (user POV)

The one-line install in the README; afterwards nothing, the scheduler and
self-update keep it current.

## Driving it

`scripts/verify.sh --push` runs `install` when an installer,
`.github/scripts`, self-update, or scheduling changed; `scripts/verify.sh
install` runs it now. It builds this machine's release file (and the app on
macOS), serves it on 127.0.0.1, and installs it twice, fresh and upgrade,
in homes of its own; log in `.verify/latest/install.txt`.

## Proof

`internal/selfupdate` and `internal/schedule` tests pass, and the smoke
says `install-smoke: ok`. Windows installs only in CI.

## Try

- An install over an older release: is the old binary replaced and the schedule kept?
- `AI_USAGE_NO_SCHEDULE=1`: is nothing registered?

## Gotchas

Never run `schedule install` or an installer against this machine's own
home: it replaces the user's ai-usage and its launchd agent. On macOS 27,
launchd pins the agent to the binary it registered; see `internal/schedule/launchd.go`
for how an update survives it. CI's macOS install job flakes now and then:
rerun it before reading it as a regression.
