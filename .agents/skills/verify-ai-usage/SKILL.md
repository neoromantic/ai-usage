---
name: verify-ai-usage
description: Prove an ai-usage change works the way a person sees it, at the least cost. Picks the checks from what changed with scripts/verify.sh (Go tests, lint, the printed report and the interactive view driven in tmux, two made-up devices through a local relay, the menu bar app's checks and pictures), keeps every screen as evidence, and says how to look further by hand. Use before saying an ai-usage change works, before a push, when a check fails, or when asked to QA or bug bash ai-usage.
---

# Verify ai-usage

Read [AGENTS.md](../../../AGENTS.md) first: what is where, the public-repo
rule, and that the app only shows the Go report. This skill says how to
prove a change, and how to spend no more time on it than the change needs.

ai-usage has no web UI, so the e2e runner does not apply. Its surfaces are
a CLI with a printed page and an interactive view (Go), a relay (an HTTP
JSON API), and a menu bar app (Swift). Each has a cheap, isolated proof,
and [scripts/verify.sh](../../../scripts/verify.sh) runs the ones a change
needs.

## The loop

```sh
scripts/verify.sh --plan    # which steps the changes need, and why; runs nothing
scripts/verify.sh           # run them; while you work
scripts/verify.sh --push    # at the depth CI checks; once, before a push
```

It reads the branch's changes since `origin/main` (commits, staged,
unstaged, untracked; `--base REF` for another base) and runs only the steps
they touch, cheapest first. It always runs every selected step and ends
with a table of results and seconds, and exits 1 if any failed. `scripts/verify.sh STEP...` runs
those steps whatever changed, for example `scripts/verify.sh page` after an
edit to a golden file.

| Step | Runs when | What it proves | Cost, warm |
| --- | --- | --- | --- |
| `shell` | a `.sh` or `install.ps1` changed | `sh -n` and `shellcheck -s sh` on every script CI checks; `install.ps1` parses (only with `pwsh`) | 1s |
| `go` | any Go file, `go.mod`, testdata | gofmt, vet, build, `go test ./...`. The test cache reruns only what changed; never pass `-count=1` to save time, it costs it | 1s cached, 12s cold |
| `lint` | Go code changed | staticcheck and deadcode at CI's pinned versions for this OS; `--push`: linux, darwin, windows | 3s; 14s with `--push` |
| `page` | Go code or a demo report | both demo reports printed at 80 and 110 columns, and the interactive view driven by keys in a tmux of its own | 4s |
| `stack` | Go code | a relay on 127.0.0.1, two devices in made-up homes: `team key`, `team join`, a collect on each, and the first's report lists both | under 1s |
| `app` | `macos/`, `internal/view`, `internal/snapshot`, a demo report | `AIUsageChecks`, the app's decoding and words, and a debug build of the app | 6s; 25s cold |
| `pictures` | the app's UI or words (`AIUsageBar`, `AIUsageKit`), a demo report | every tab and state of the popover, the Team window, Settings, and the menu bar, light and dark, from `team.json`; `--push` adds `solo.json` | 45s; +20s |
| `build` | `--push` and `macos/` changed | `macos/build.sh`: the universal, signed release bundle | 80s |
| `install` | `--push` and an installer, `.github/scripts`, `macos/`, self-update, the app's install, or scheduling changed | `install-smoke.sh`: install and upgrade from a local release, in homes of their own; on macOS the app too | 15s; 90s cold |
| `brew` | `--push`, on macOS, and the formula, `dist.sh`, self-update, or Homebrew detection changed | CI's Homebrew smoke: `formula.sh` refuses old tags; a formula of local files installs from a tap of its own, passes `brew test`, reports `"method": "homebrew"` from a home of its own, and passes `brew style` and `brew audit --strict`; tap and install are removed after | about 3 min cold, while `brew style` installs its gems |

CI runs no macOS job, so on a Mac `--push` is the macOS gate: `go`, `app`, `build`, `install` and `brew` there replace CI's macOS jobs.

A change to docs only needs no step; read its diff for real data.
The work after a step passes is reading its evidence: a step that passes
proves the page did not break, not that it says the right thing.

## Doctor

`scripts/verify.sh --doctor` checks, read-only, the Go version against
`go.mod`, staticcheck and deadcode against the versions CI pins in
`.github/workflows/ci.yml` (and prints the `go install` line when one is
off), shellcheck, tmux, jq, Swift, pwsh, and that the base ref exists. Run
it first when a step fails for a reason that is not the code.

## Evidence

Each run writes `.verify/<time>/` (gitignored; the ten newest are kept)
and points `.verify/latest` at it.

| Path | Holds |
| --- | --- |
| `summary.txt` | each step, ok or FAIL, and its seconds, at which commit |
| `go-test.txt` | the whole `go test` output |
| `page/solo-80.txt` … `team-110.txt` | the printed page of each demo report at each width |
| `page/tui-0-start.txt` … `tui-10-back.txt` | the interactive view at start, then after `s` (DEVICES as status), `%` (shares), `7` (the week), `G` (the bottom), `?` (help), `esc`, `enter` (a row picked), `j` (the next row), `enter` (its detail), `esc` (back) |
| `stack/` | each device's commands and pages, `a.json`, the report of `dev-a` that lists both devices, and `relay.log` |
| `app/checks.txt`, `app/team/*.png`, `app/solo/*.png` | the app's checks, and its pictures |

Read the screens a change touches; do not trust a passing step for words,
order, or alignment. Proof is the real path: the binary a person runs,
`--from` a demo report, the keys a person presses, and the renderer the
README's pictures come from, never an internal setter. When a fact must
reach the app, check it in `stack/a.json` or the demo report and in the
picture, not only in a Go test.

## Driving it by hand

Everything here is isolated. Never run a dev build with this machine's own
home: it collects real data and publishes it, as a `devel` version, to the
real team's relay. Never run `schedule install` outside a made-up home.

- **The printed page:** `go build -o /tmp/au ./cmd/ai-usage`, then
  `TZ=Europe/Berlin /tmp/au report --from docs/demo/team.json --plain --width 110`.
  `--from` reads only the file. The demo is taken at Thu 24 Sep 2026 15:40 in Berlin, so
  `TZ=Europe/Berlin` makes the times match the pictures.
- **The interactive view:** in a tmux of its own, always with `-f /dev/null`.
  A tmux config with a plugin that restores sessions, such as
  tmux-resurrect, would restore them into any new server:
  ```sh
  t() { tmux -L au-verify -f /dev/null "$@"; }
  t new-session -d -s v -x 110 -y 45 "TZ=Europe/Berlin TERM=xterm-256color /tmp/au report --from docs/demo/team.json"
  t send-keys -t v -l s; sleep 0.4; t capture-pane -p -t v   # -e keeps the colors
  t kill-server
  ```
  Keys: `q` quit, `?` help, `j`/`k` scroll, `G`/`g` end/top, `h`/`l` the
  matrix sideways, `p` or `1 7 3 9` the period, `%` shares, `s` DEVICES as
  status, `r` refresh, `enter` pick a row, then `j`/`k` move the pick,
  `enter` its detail, `esc` back. Send `Enter` and `Escape` as key names,
  without `-l`.
- **A team:** what `step_stack` in `scripts/verify.sh` does: `ai-usage relay serve --addr
  127.0.0.1:PORT` (memory store), then each device as
  `env -i HOME=<dir> PATH=/usr/bin:/bin AI_USAGE_NO_SCHEDULE=1 AI_USAGE_RELAY=http://127.0.0.1:PORT AI_USAGE_NAME=<name> /tmp/au ...`.
  Never `collect --quiet` by hand: that is the scheduled run, which first
  waits up to a minute so that a team's runs spread out. The probe still
  finds this machine's installed tools in the usual install places, not
  only on PATH, and asks them in the made-up home, where they find no login
  and write only there. With no login and no logs in the home, the
  subscriptions are empty; a subscription's numbers are proven by the demo reports and the Go
  tests (`internal/collect`'s `world_test.go`), not here.
- **The menu bar app:** `cd macos && xcrun swift build --product AIUsageBar`, then
  `TZ=Europe/Berlin "$(xcrun swift build --show-bin-path)/AIUsageBar" --render ../docs/demo/team.json OUTDIR`
  draws every state; [docs/demo/README.md](../../../docs/demo/README.md)
  names each picture. `--demo REPORT` runs the app in the menu bar on a
  report, with no collection, update, or notification. Its popover opens
  only on a click on the menu bar, which takes over the user's pointer, so
  drive it (cua-driver) only when the user says so, and stop the process you
  started, by its PID.
- **A picture for the README:** `bun scripts/demo/shots.ts NAME`, as
  docs/demo/README.md says, after a visible change, and look at it.

## Golden files

`internal/view/testdata/*.golden` are the printed page and the JSON.
`go test ./internal/view -update` rewrites them; run it only when the page
is meant to change, then read `/usr/bin/git diff internal/view/testdata`
line by line. A golden diff no one meant is the bug.

## Bug bash

There is no explore runner here; a bug bash is a person's or an agent's
session over the surfaces above, one feature at a time, from
[features/](features/README.md). Each feature file lists what to try. A
finding is a bug once a test fails on it: a Go test (a golden, a
`world_test`, a TUI model test) or an `AIUsageChecks` check, added in the
same branch as the fix.

## Cleanup

`scripts/verify.sh` stops what it starts, the relay and its tmux server,
even when a step fails, and keeps `.verify/`. By hand: kill the relay and
the `--demo` app by the PID you started, `tmux -L <socket> kill-server` on
your own socket only, never by a process name, which could hit the user's
installed AI Usage.app or their tmux. `rm -rf .verify` clears the evidence.

## Gotchas

- `TestRun` in `internal/tui` depends on timing; `scripts/verify.sh` runs
  that package once more before calling it a failure.
- The shell may run commands through a filter (rtk) that shortens `go test`
  and `git diff` output, down to "N passed" or "Files are identical". Read
  `.verify/latest/go-test.txt`, or call go and git by their full paths
  (`$(command -v go)`, `/usr/bin/git`).
- Port 8765, `install-smoke.sh`'s own, is often taken on a desktop;
  `scripts/verify.sh install` uses 28765, and `stack` tries 28741 to 28745.
- The `pictures` step shows the app's ⋯ menu on the screen for a moment;
  AppKit draws a menu only while it is open.
- The `install` and `brew` steps stop when Homebrew has installed ai-usage
  on this Mac; `brew uninstall ai-usage` first, and install it again after.
- After a release, GitHub's `releases/latest` serves the old tag for a
  couple of minutes, so `ai-usage update` says nothing is new; wait.
