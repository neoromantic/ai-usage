# AGENTS.md

<!-- devkit:agents v1 -->
<!-- managed by devkit v1: edit in https://github.com/neoromantic/devkit/blob/main/agents/AGENTS.shared.md -->
## Shared rules (devkit)

These rules apply to people and coding agents in every repo that uses
[devkit](https://github.com/neoromantic/devkit). Project rules follow in `## Project` below.
Where they conflict, the project section wins.

### Data

- Use only made-up data in tests, fixtures, examples, docs and screenshots. No real people, chats,
  phone numbers, tokens, session files or local paths such as home directories.
- Never commit `.env` files or credentials. Tests run offline by default; live API tests are
  opt-in and read their credentials from the environment.

### Commands

Every repo exposes the same script names, so CI and agents need no per-repo knowledge:

| Script | What it does |
|---|---|
| `check` | lint and typecheck (or `lint` and `typecheck` separately) |
| `test` | offline unit tests |
| `test:e2e` | end-to-end tests, still offline |
| `build` | produce the shipped artifact, if any |

- Bun repos: `bun run <script>`. Python repos: `uv run …` (never bare `pip` or `python`).
  Go repos: `go` tooling from `go.mod`.
- Use Bun, uv or Go modules for installs. Use npm only for `npm publish` and `npm view`.

### Definition of done

- The change works end to end, tests cover it, and `check` and `test` pass locally.
- README and docs change in the same commit as the behaviour they describe.
- No new warnings, no commented-out code, no stray debug output.

### Commits and releases

- Conventional Commits: `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`, `ci:`.
  A breaking change adds `!` (`feat!:`) and a `BREAKING CHANGE:` footer.
- One logical change per commit. Never rewrite published history or force-push `main`.
- Releases happen only through the repo's Release PR or release workflow. Don't bump versions,
  edit `CHANGELOG.md` or push tags by hand.

### Files

- Don't add planning or progress files (`progress.md`, `docs/plans/`). Plans live in issues.
  `ROADMAP.md`, if present, is a short "Known gaps" list that links issues.
- Don't add agent hooks that rewrite tracked files.
- `AGENTS.md` is the canonical instruction file. `CLAUDE.md` contains only `@AGENTS.md`.
- Files marked "managed by devkit" (this block, `SECURITY.md`, `CONTRIBUTING.md`, the PR
  template, `.gitleaks.toml`, `.github/zizmor.yml`, `renovate.json`, `lefthook.yml` and the
  CI stub) are synced from devkit. Change them there, not here.
<!-- /devkit:agents -->

## Project

How to work in this repository. [README.md](README.md) says what ai-usage does and how people use it.

### What is here

ai-usage is a Go command-line tool: a collector that the system scheduler runs every 15 minutes, the report it prints, and a relay through which a team's machines see each other. `macos/` holds a menu bar app in Swift that shows the Go report.

| Path | What it holds |
| --- | --- |
| `cmd/ai-usage/` | the command: its subcommands, help, scheduling, and updates |
| `internal/collect/` | one run: read the logs, ask each tool, store the sample, publish to the relay |
| `internal/logs/`, `internal/probe/` | token counts from the files the tools write; the account and quota, asked of the tools |
| `internal/meter/` | tokens by kind and by hour, and weighted tokens |
| `internal/state/`, `internal/snapshot/`, `internal/team/` | the collector's own folder; the document a device publishes; the team key |
| `internal/view/` | the report: the JSON and the printed page, with golden files in `testdata/` |
| `internal/tui/` | the interactive report |
| `internal/schedule/`, `internal/selfupdate/`, `internal/fsutil/` | the system scheduler, self-update, and shared file helpers |
| `relay/`, `api/` | the relay and its client; the relay as a Vercel function |
| `macos/` | the menu bar app, a Swift package: `AIUsageBar`, the app; `AIUsageKit`, what it reads from the report and the words it shows; `AIUsageChecks`, its checks; `build.sh` builds the bundle |
| `scripts/demo/` | the made-up reports and every picture in `docs/demo/` |
| `scripts/verify.sh`, `.agents/skills/verify-ai-usage/` | the checks a change needs, and the skill that says how to prove a change, with a map of the features |
| `docs/` | the JSON reference, the relay and its protocol, containers, releasing, the release notes in `docs/releases/`, and the demo in `docs/demo/` |
| `install.sh`, `install.ps1`, `.github/` | the installers; CI, the release workflow, and their scripts, among them `.github/scripts/formula.sh`, the Homebrew formula, and `tap.sh`, which puts it in the tap `neoromantic/homebrew-tap` after each release |

### The repository is public

No real data goes anywhere in it: not in code, tests, golden files, docs, pictures, release notes, or commit messages. That means no real emails, names of people, hosts, devices, or bots, no account IDs, and no names or paths of private companies or repositories. Use made-up ones, such as ann@acme.dev, mira-mbp, or srv1. The demo data is made up in `scripts/demo/main.go`, which writes `docs/demo/`; take reports and pictures from there, never from a real machine.

The made-up demo reports, golden files and tests hold made-up home folders, so `.privacy-scan-ignore` exempts those files from the devkit privacy scan's home-path rule, and `.gitleaksignore` lists the made-up device IDs that gitleaks takes for keys. Add to them only for made-up data.

Before every push, read the diff and the commit messages for real data. Push only your own branch. Never push an old ref or a tag, and never use `git push --tags`: a pushed tag publishes a release.

### The menu bar app shows the Go report

The app runs `ai-usage report --json` and shows what it says. It never works out a report, a forecast, an alert, or a total of its own. When it needs something the report does not have, add the field to the Go JSON: describe it in [docs/json-schema.md](docs/json-schema.md), test it in Go, and check the app's use of it in `AIUsageChecks`.

### Checks before a push

`scripts/verify.sh` runs the checks the branch's changes need and keeps what they showed in `.verify/latest/`; `scripts/verify.sh --push` runs them at the depth CI does. Run that before every push. The skill [verify-ai-usage](.agents/skills/verify-ai-usage/SKILL.md) says what each step proves, how to read its evidence, and how to drive the report, a team, and the app by hand without touching this machine's own ai-usage.

CI, `.github/workflows/ci.yml`, runs these:

```sh
gofmt -l .              # prints nothing
go vet ./...
go build ./...
go test ./...
go mod tidy -diff
```

staticcheck and deadcode run at the versions CI pins, for each release OS:

```sh
go install honnef.co/go/tools/cmd/staticcheck@v0.8.2
go install golang.org/x/tools/cmd/deadcode@v0.52.0
bin=$(go env GOPATH)/bin
for os in linux darwin windows; do
  GOOS=$os go vet ./...
  GOOS=$os "$bin/staticcheck" ./...
  GOOS=$os "$bin/deadcode" -test ./...   # prints nothing
done
```

After a change under `macos/`, to the report's JSON, or to `docs/demo/*.json`, which the app's checks read, also run the app's checks and build it:

```sh
(cd macos && xcrun swift run AIUsageChecks)
sh macos/build.sh v9.9.9-local /tmp/ai-usage-app.zip
```

After a change to a shell script, CI runs `sh -n` and `shellcheck -s sh` on it.

After a change to a workflow, CI's hygiene job runs actionlint and zizmor on it; run `actionlint` and `uvx zizmor@1.31.1 --offline .` first. Pin each action to a full commit with its version in a comment, as in `actions/checkout@<sha> # v7.0.1`, and give a checkout `persist-credentials: false`.

`go test ./internal/view -update` rewrites the golden files. Do it only on purpose, and read the diff. `TestRun` in `internal/tui` depends on timing and fails now and then on a busy machine; run it once more before taking a failure as real.

### Docs are part of every change

A change is done when its docs say so, in the same branch:

- `README.md`, for anything a person sees or runs
- `docs/*.md`: the JSON in `docs/json-schema.md`, the relay in `docs/relay.md` and `docs/relay-protocol.md`, containers, and releasing

When something visible changes, in the report, the interactive view, or the app, make its pictures again with the demo tooling, as [docs/demo/README.md](docs/demo/README.md) says, and look at them.

### Commits and releases

Each commit is one plain-English sentence that says what changed and why, as in `git log`, with the issue at the end when there is one, such as `(#20)`. Releases follow [docs/releasing.md](docs/releasing.md), with notes in `docs/releases/<tag>.md` for the people who install them.

### Website

[au.sergeybaus.com](https://au.sergeybaus.com) repeats the README's promise and features, the exact install commands, pictures from `docs/demo/`, and the list of releases. The maintainer updates it after each release. When a change alters any of these, say so in the release notes, so the site can be brought up to date.
