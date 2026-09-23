# ai-usage

ai-usage shows how much of your Claude Code, Codex, Grok, and Hermes quota you have used, and how many tokens went where. It can also show the same numbers for every machine in your team.

It is one small binary for macOS, Linux, and Windows. The system scheduler runs it every 15 minutes. Each run reads what those tools already record on disk, asks the installed tools for your account and quota, and exits. A run can also publish an encrypted summary for this machine to a small relay, and read the summaries of the other machines in your team.

```
ai-usage v1.2.3 · ann-mbp (ann) · team 472ghuwcctyu…
last run 2m ago · last success 2m ago
relay: pushed 2m ago · pulled 2m ago

CLAUDE  ok
  ann@example.com  max  (logged in)
    quota 78% ! (cache, 4m ago)
      5h             42%  resets in 2h 10m
      7d             78% !  resets in 3d 4h  · at this pace full in 10h 56m, before reset
    90 days: 2 sessions · in 1.6M out 405K cache read 60.0M write 2.9M
    last active 20m ago
      /Users/ann/src/api  1 session · in 1.2M out 310K cache read 48.0M write 2.1M
      /Users/ann/src/web  1 session · in 400K out 95.0K cache read 12.0M write 800K

CODEX  ok
  ann@example.com  plus  (logged in)
    quota 35% (harness, 2m ago)
      5h             12%  resets in 4h
      7d             35%  resets in 5d
    90 days: 1 session · in 2.5M out 180K cache read 9.0M write 0
    last active 3h ago
      /Users/ann/src/api  1 session · in 2.5M out 180K cache read 9.0M write 0

GROK  skipped
  not installed

HERMES  skipped
  not installed

TEAM  read 2m ago
  ann-mbp (ann)  (this device)  collected 2m ago · v1.2.3 · ok
  bo-laptop (bo)  collected 9m ago · v1.2.3 · ok
  claude
    ann@example.com  quota 78% ! (from ann-mbp (ann), 4m ago) · in 1.6M out 405K cache read 60.0M write 2.9M on 1 device
    bo@example.com  quota 91% !! (from bo-laptop (bo), 9m ago) · in 800K out 150K cache read 20.0M write 900K on 1 device
  codex
    ann@example.com  quota 35% (from ann-mbp (ann), 2m ago) · in 2.5M out 180K cache read 9.0M write 0 on 1 device
```

`!` marks a window at 75% or more, and `!!` one at 90% or more. The quota line shows the fullest window that has not reset since the reading, and how old the reading is. When every window has reset since, it says quota unknown. A window that has reset keeps its last percent, with no mark, and says how long ago it reset. The pace comes from the readings stored over the last few hours.

## What it does, and what it does not

Each run:

1. Finds the tools' data folders: `~/.claude`, `~/.codex`, `~/.grok`, and `~/.hermes`, plus any folder named by `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `GROK_HOME`, or `HERMES_HOME`. Hermes profiles under `<home>/profiles/<name>` that have a `state.db` are found too, including the one `hermes profile use` selects.
2. Adds up sessions and tokens from the last 90 days, by account and by project folder. A sub-agent's tokens count toward the session that started it.
3. Asks each installed tool which account is logged in and how full its quota windows are.
4. Saves a sample. Samples are kept for 90 days.
5. If a relay is set, publishes this machine's snapshot and reads the rest of the team.
6. Prints the report, unless the scheduler started it.

A tool that is not installed is skipped. One tool failing does not stop the others. `ai-usage status` shows each tool's state and the last error.

ai-usage does not:

- call Anthropic, OpenAI, xAI, or any other provider itself
- log in, log out, refresh a token, or switch accounts
- open credential files, keychain items, or cookies
- start a session, send a prompt, or spend quota
- keep or send prompts, replies, tool arguments, or file contents
- estimate cost in dollars

### What it reads and runs

`<home>` is the tool's data folder.

| Tool | Files it reads | Command it runs |
| --- | --- | --- |
| Claude Code | `<home>/projects/*/*.jsonl` and the sub-agent logs under `<home>/projects/*/<session>/subagents/`. For the usage Claude Code cached and the id of the logged-in account: `~/.claude.json` for the default home, or `<home>/.claude.json` whenever `CLAUDE_CONFIG_DIR` names the home, `~/.claude` included. A legacy `<home>/.config.json` comes first. The cached usage is used only for a claude.ai login. | `claude auth status --json` |
| Codex | `<home>/sessions/**/*.jsonl` and `<home>/archived_sessions/**/*.jsonl` | `codex app-server`, asked only for `account/read` (with `refreshToken: false`) and `account/rateLimits/read` after the handshake |
| Grok | `<home>/sessions/*/*/updates.jsonl` and `summary.json`. From `<home>/logs/unified*.jsonl`, only the lines that name the signed-in user and the credit usage. | none |
| Hermes | a private copy of `<home>/state.db`, from which it reads the session ids, working folders, billing provider, and token columns | none |

Session logs hold your conversations. ai-usage reads them for token counts and the working folder, and keeps nothing else. It never opens `auth.json`, `credentials.json`, `.credentials.json`, `.env` files, or any file whose name contains `credential` or `cookie`.

A tool's command runs with that tool's own login, so the tool may contact its own service while it answers, as it would when you run it. Commands start in your home directory, so a project's settings do not change which account answers. The `CLAUDE_CONFIG_DIR` or `CODEX_HOME` variable is set for the command when the home is not the default. `CLAUDE_CONFIG_DIR` is also kept when your environment sets it to the default `~/.claude`, and it is passed exactly as you wrote it, trailing slash included, since Claude Code names its login after that string. A command that does not answer within 20 seconds is stopped; on macOS and Linux, together with everything it started. After its answers, `codex app-server` gets up to 3 seconds to exit on its own before it is stopped.

ai-usage looks for the `claude` and `codex` binaries on `PATH`, then in `~/.local/bin`, `~/bin`, `~/.bun/bin`, `~/.npm-global/bin`, `~/.claude/bin` or `~/.codex/bin`, `/opt/homebrew/bin`, `/usr/local/bin`, and `/usr/bin`. On Windows it tries `%USERPROFILE%\.local\bin`, `%USERPROFILE%\.claude\bin` or `%USERPROFILE%\.codex\bin`, and `%APPDATA%\npm` after `PATH`.

The scheduler does not see your shell's variables. When a run sees a home through `CODEX_HOME` or the like, it remembers that home for later runs, and for `CLAUDE_CONFIG_DIR` the exact value too, so scheduled runs ask Claude Code about the same login. After you set or change one of those variables, run `ai-usage` once in that shell.

### Network

ai-usage itself connects to two places:

- the relay, if one is set: it sends this machine's snapshot and reads the team's
- GitHub, in release builds: it checks for a new release at most every 6 hours and downloads it

## Install

macOS and Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh | sh
```

Windows, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.ps1 | iex
```

Releases are built for amd64 and arm64 on each OS. The installer:

1. downloads the release file for your OS and CPU, and `checksums.txt` from the same release
2. checks the SHA-256 checksum
3. installs the binary to `~/.local/bin/ai-usage`, or `%LOCALAPPDATA%\Programs\ai-usage\ai-usage.exe` on Windows, where it also adds that folder to your user `PATH`
4. saves the relay and joins the team, if you gave them
5. runs `ai-usage` once, which registers it with the scheduler

The installer asks no questions. Run it again to upgrade in place. It reads these variables:

| Variable | Meaning |
| --- | --- |
| `AI_USAGE_BIN_DIR` | where the binary goes |
| `AI_USAGE_RELAY` | relay URL to save before the first run |
| `AI_USAGE_TEAM_KEY` | team key to join before the first run |
| `AI_USAGE_DOWNLOAD_URL` | where to download release files from, instead of the latest GitHub release |
| `AI_USAGE_ALLOW_ROOT` | install for root even though the installer runs under `sudo` |

To join a team while installing:

```sh
curl -fsSL https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh |
  AI_USAGE_RELAY=https://relay.example.com AI_USAGE_TEAM_KEY='aiu-team-1:…' sh
```

```powershell
$env:AI_USAGE_RELAY = 'https://relay.example.com'
$env:AI_USAGE_TEAM_KEY = 'aiu-team-1:…'
irm https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.ps1 | iex
```

A key typed on the command line stays in your shell history. To avoid that, install first, then run `ai-usage team join` and paste the key.

Run the installer as the person whose usage you want to collect. Under `sudo` it stops, because the collector would register root's crontab and read root's tools, and could leave root-owned files in your home.

On Linux the schedule needs `crontab`, which some minimal systems lack. The installer needs `curl` or `wget`, and `sha256sum`, `shasum`, or `openssl`.

## First run

The first run:

- creates the state folder
- generates a new team key, unless the installer joined a team, so an install starts as a team of one
- registers with the scheduler: a line in your crontab on macOS and Linux, a task named `ai-usage` in Task Scheduler on Windows
- collects and prints the report

The crontab line looks like this. It keeps the `PATH` of the shell that installed it, so that scheduled runs find the tools, and it names the state folder, so that scheduled runs use the same device, team key, and history as your own runs:

```
*/15 * * * * PATH='/opt/homebrew/bin:/usr/bin:/bin' '/Users/ann/.local/bin/ai-usage' collect --quiet --home '/Users/ann/Library/Application Support/ai-usage' >/dev/null 2>&1 # ai-usage
```

The Windows task runs `ai-usage.exe collect --quiet --home <state folder>` every 15 minutes. Unlike Task Scheduler's defaults, it also runs on battery, runs once after the computer wakes if it missed a run, never runs twice at once, and is stopped after 10 minutes.

To pause the collector, comment out the crontab line, or disable the task in Task Scheduler. Runs leave it that way, and `ai-usage status` says it was disabled by hand. `ai-usage schedule install` turns it back on.

On macOS, changing the crontab may bring up a prompt asking to let your terminal administer the computer. If you decline, the schedule is not registered, and `ai-usage status` says why. Later runs try to register again until `ai-usage schedule remove` turns that off.

The state folder is `~/Library/Application Support/ai-usage` on macOS, `$XDG_CONFIG_HOME/ai-usage` or `~/.config/ai-usage` on Linux, and `%LOCALAPPDATA%\ai-usage` on Windows (not the roaming profile, so the device id stays on this machine). `AI_USAGE_HOME` moves it. The scheduler entry follows the folder of the last run that registered it, so after moving the folder, run `ai-usage` once with the new `AI_USAGE_HOME`. It holds:

| File | Contents |
| --- | --- |
| `config.json` | this device's id, the relay, remembered homes and the `CLAUDE_CONFIG_DIR` value seen for them, whether the schedule is off |
| `team.key` | the team's private key; this is the secret |
| `state.json` | the last good readings, sessions, and health |
| `team-cache.json` | the team's snapshots from the last read |
| `samples/` | one file per day of samples, kept for 90 days |
| `run.lock` | keeps two runs from overlapping |

## Teams

A team is one key pair. Every machine in the team stores the same private key. The team's name is the fingerprint of its public key, like `472ghuwcctyu…`, which is not secret.

To add a machine to your team:

1. On a machine in the team, run `ai-usage team key`. It prints the private key, `aiu-team-1:…`. Anyone who has it can read the team's snapshots and publish into the team, so share it the way you would share a password.
2. On the new machine, install with `AI_USAGE_TEAM_KEY` as shown above, or run `ai-usage team join` and paste the key.
3. Make sure both machines use the same relay: `ai-usage relay show`.

Joining saves the previous key as `team.key.previous`, drops the cached team, and asks the relay, if one is set, to remove this machine from the old team. `ai-usage team` lists the team's machines. `ai-usage team forget-device ID` removes a retired machine's snapshot from the relay and from this machine's team view. A snapshot that is not updated expires on its own: after 90 days, or sooner for a machine that has reported for less time than that, but never sooner than 7 days after its last update.

A key cannot be revoked. To shut someone out, start a new team and join the remaining machines to it: on one machine, move `team.key` out of the state folder, run `ai-usage` to generate a new key, and give that key to the others.

In the team view, token counts add up across machines. Quota percentages do not: an account's quota is the newest reading any machine has for it.

## Relay

The relay is a small HTTP API that keeps one snapshot per machine. It is needed only for the team view; without one, ai-usage reports on this machine alone.

```sh
ai-usage relay show                          # the relay in use, or "no relay configured"
ai-usage relay set https://relay.example.com
ai-usage relay clear
```

A release build may carry a default relay, chosen when the release was built. `relay set` overrides it and `relay clear` goes back to it. `AI_USAGE_RELAY` overrides both for one run.

The relay accepts only a small, fixed-shape usage snapshot signed by the team key. It checks the signature and the shape and stores nothing else. It limits requests per IP address, writes per team, and machines per team (32). Each IP address can also add only 5 new teams and 32 new machines a day.

To run your own relay on Vercel with Upstash Redis, or with `ai-usage relay serve`, see [docs/relay.md](docs/relay.md).

## Commands

| Command | What it does |
| --- | --- |
| `ai-usage [--json] [--offline]` | collect now and print the report; `--offline` skips the relay |
| `ai-usage collect [--quiet] [--json] [--offline] [--home DIR]` | the same; the scheduler runs `collect --quiet --home DIR`, and `--home` overrides `AI_USAGE_HOME` |
| `ai-usage report [--json]` | print the last collected report without collecting |
| `ai-usage status [--json]` | version, last success, last error, relay, schedule, update, and each tool's state |
| `ai-usage team` | the team fingerprint and its machines |
| `ai-usage team key` | print the team's private key |
| `ai-usage team join [KEY]` | join a team; the key is read from standard input when omitted |
| `ai-usage team forget-device ID` | remove a machine's snapshot from the relay |
| `ai-usage relay show` | print the relay in use |
| `ai-usage relay set URL` | save a relay URL, `http://` or `https://` |
| `ai-usage relay clear` | forget the saved relay |
| `ai-usage relay serve [--addr :8080] [--client-ip-header NAME]` | run a relay; behind a reverse proxy, name the header it sets to the client's address |
| `ai-usage schedule install` | register with the scheduler, and let later runs keep it registered |
| `ai-usage schedule remove` | unregister, and stop later runs from registering again |
| `ai-usage schedule status` | whether the scheduler runs this binary with this state folder, or was disabled by hand |
| `ai-usage update` | check for a release now |
| `ai-usage version` | print the version |
| `ai-usage help` | print usage |

| Variable | Meaning |
| --- | --- |
| `AI_USAGE_HOME` | the state folder |
| `AI_USAGE_RELAY` | the relay URL, overriding the saved one |
| `AI_USAGE_NO_SCHEDULE` | when set, this run does not register with the scheduler |
| `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `GROK_HOME`, `HERMES_HOME` | another data folder for that tool |

## JSON for agents

`ai-usage --json` and `ai-usage report --json` print the report as JSON with `"schema_version": 2`. A field changes meaning only with a new schema version.

```
schema_version, generated_at
collector   version, device, team, last run, last success, last error,
            relay, schedule, and update state
providers[] claude, codex, grok, hermes: status (ok, partial, error, skipped), error, homes,
            accounts[]: label, plan, headline_percent, level (ok, warning, critical, unknown),
                        quota with windows[] (percent, level, resets_at, pace), sessions,
                        tokens, last_active_at, projects[]
team        pulled_at, devices[], and providers[] with accounts summed across devices
```

`ai-usage status --json` prints `schema_version`, `collector`, and `sources[]`. Every field is described in [docs/json-schema.md](docs/json-schema.md).

## Privacy

A snapshot is what leaves the machine. It is at most 32 KB, and the relay rejects any field it does not know.

Sealed with the team key, so only the team can read them:

- the machine's host name and OS user name
- account labels, such as an email address
- project folder paths
- error messages

In plain text, so the relay can check the shape:

- the team fingerprint, the device id (random), and the collector version
- provider names, plan names, and whether an account is logged in
- quota window names, percentages, and reset times
- session and token counts, and timestamps
- each tool's status: ok, partial, error, or skipped

So the relay's operator can see how many machines a team has, which tools and plans they use, how much, and when, but not who, on which machine, or in which project. The operator also sees the IP addresses that connect, which the relay keeps in rate-limit counters for up to a day.

Every snapshot is signed with the team key, and each machine checks the signatures of what it reads. The relay cannot forge or change a snapshot without being noticed. It can withhold one, delete one, or keep serving an older one.

The state folder on your machine keeps account labels and project paths in plain text.

## Updates

A release build checks GitHub for a new release at most every 6 hours, as part of a normal run. When there is a newer `vX.Y.Z` release, it downloads the file for this OS and CPU, checks it against `checksums.txt` from the same release, runs it once with `version`, and replaces its own binary only if the new one starts and reports that release. That run finishes on the old binary; the next run is the update. `ai-usage update` checks right away.

The check also runs when a collection fails, for example on a state file this version cannot read, or on a bug that stops the run. A release that breaks collection can then still be replaced by the one that fixes it. A download may take several minutes on a slow link; it is dropped only when no data arrives for a minute.

Self-update cannot be turned off. It skips prereleases, such as `v1.3.0-rc.1`. A binary in a folder you cannot write to cannot update itself, and `ai-usage status` shows that error. Rerunning the installer also upgrades.

The checksum guards against a broken download. It does not protect against a compromised release, since both files come from the same place.

A binary built from source reports version `dev`. It never updates itself and never registers with the scheduler on its own; run `ai-usage schedule install` for that.

## Uninstall

Run these while the binary is still installed. `forget-device` is optional: it removes this machine's snapshot from the relay now instead of letting it expire. `ai-usage status` shows this machine's device id.

macOS:

```sh
ai-usage schedule remove
ai-usage team forget-device d-…
rm ~/.local/bin/ai-usage
rm -r ~/Library/Application\ Support/ai-usage
```

Linux:

```sh
ai-usage schedule remove
ai-usage team forget-device d-…
rm ~/.local/bin/ai-usage
rm -r "${XDG_CONFIG_HOME:-$HOME/.config}/ai-usage"
```

Windows, in PowerShell:

```powershell
ai-usage schedule remove
ai-usage team forget-device d-…
Remove-Item -Recurse "$env:LOCALAPPDATA\Programs\ai-usage", "$env:LOCALAPPDATA\ai-usage"
```

Then open *Edit environment variables for your account* from the Start menu and remove the `…\AppData\Local\Programs\ai-usage` entry from `Path`.

If you set `AI_USAGE_BIN_DIR` or `AI_USAGE_HOME`, remove those folders instead.

## Build from source

Go 1.25 or newer:

```sh
go build -o ai-usage ./cmd/ai-usage
go test ./...
```

How releases are built and published: [docs/releasing.md](docs/releasing.md). The requirements this project follows: [ai-report.md](ai-report.md).

## License

MIT. See [LICENSE](LICENSE).
