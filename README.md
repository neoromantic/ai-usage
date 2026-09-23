# ai-usage

ai-usage shows how much of your Claude Code, Codex, Grok, and Hermes quota you have used, and how many tokens went where. It can also show the same numbers for every machine in your team.

It is one small binary for macOS, Linux, and Windows. The system scheduler runs it every 15 minutes. Each run reads what those tools already record on disk, asks the installed tools for your account and quota, and exits. A run can also publish an encrypted summary for this machine to a small relay, and read the summaries of the other machines in your team.

```
ai-usage v1.4.2 · sam-air (sam) · team q7dm3xk2…               Wed 23 Sep 09:40
✓ collected 1m ago  · no relay  ✓ scheduled  ✓ up to date

ACCOUNTS  5 · 1 critical · 1 warning · 1 fills early · 2 stale · 1 unknown
  ACCOUNT              QUOTA             5h  reset   7d  reset  READ
  claude ──────────────────────────────────────────────────────────── 1 account
● sam@example.com      ██████ 100% !!     ?  reset  67%  3d14h   9h~
  └ also 7d Opus 100% !!, resets in 3d14h
  └ ~ Claude Code updates its usage cache only while it runs
  codex ──────────────────────────────────────────────────────────── 2 accounts
● sam@example.com      ████▋░  78% !    41%  2h10m  78%▲  2d6h    1m
  └ ▲ 7d full in 14h (Thu 00:20) at this pace, 1d15h before it resets
  └ also used by hermes openai-codex on sam-air, assumed the same account
○ sam.old@example.com  ······ unknown     —           —            —
  └ no reading yet
  grok ────────────────────────────────────────────────────────────── 1 account
● 3f6c2a1e…            ██░░░░  35%        —         35%  4d23h  15h~
  └ ~ grok writes its usage log only while it runs
  hermes ──────────────────────────────────────────────────────────── 1 account
● openai-codex         ████▋░  78% !    41%  2h10m  78%▲  2d6h    1m
  └ quota of codex sam@example.com, assumed the same account

THIS DEVICE  sam-air (sam) · tokens in the last 90 days
                                         SESS   INPUT  OUTPUT  CACHE R  CACHE W
claude ● sam@example.com                   20    4.5M   10.1M     603M    48.1M
    ~/src/garden/app                        9    2.1M    5.2M     310M    22.0M
    ~/Notes                                 5    1.2M    2.4M     140M    12.0M
    ~/src/garden/infra                      3    700K    1.5M    90.0M     8.0M
    + 2 more projects · ai-usage --projects
codex  ● sam@example.com                  369    552M   51.1M    16.8G        0
    ~/src/garden/app                      212    301M   28.0M     9.6G        0
    ~/src/garden/api                       88    140M   13.0M     4.3G        0
    ~/.codex/worktrees/5e0b/app            41   70.0M    6.6M     2.1G        0
    + 1 more project · ai-usage --projects
codex  ○ sam.old@example.com                6    9.4M    820K     210M        0
    ~/src/old-job/site                      6    9.4M    820K     210M        0
grok   ● 3f6c2a1e…                          3    3.2M    224K    14.3M        0
    ~/src/garden/app                        2    2.9M    200K    13.0M        0
    ~/Notes                                 1    260K   23.8K     1.3M        0
hermes ● openai-codex  via codex            4    2.1M    180K    31.0M        0
    ~/src/garden/app                        4    2.1M    180K    31.0M        0

● logged in here  ○ used here before  !! ≥90%  ! ≥75%  ▲ fills before reset
~ old: reading 6h+  ? reset since reading  — no window
more: ai-usage --projects  --json · ai-usage status
```

The header says whether collection, the relay, the schedule, and self-update are healthy; anything wrong gets a line of its own under it. ACCOUNTS has one row per account, with the fullest window that has not reset as a bar, the 5-hour and weekly windows with their reset countdowns, and how old the reading is. `!` marks 75% or more and `!!` 90% or more; `▲` says a window fills before it resets at the pace of the last few hours; `~` marks a reading over 6 hours old, and `?` a window that has reset since the reading. A Hermes account shows the quota of the Codex or Grok login it bills through, drawn in gray and assumed to be the same account; its tokens stay its own. Hermes accounts no harness reports a quota for, such as an API key, share one line. THIS DEVICE lists this machine's tokens over the last 90 days, by account and top projects; a Hermes session with no working directory, such as a Telegram chat, is listed under its Hermes folder, so each agent shows up on its own. The legend under the report explains only the marks on screen.

With a relay and more than one machine in the team, ACCOUNTS adds a USED BY column, and a DEVICES section lists every machine with its version, when it last reported, and each tool's state. Past 12 machines, the healthy ones fold into one line.

A terminal 100 columns or wider also gets a PLAN column in ACCOUNTS and a LAST column, each account's last activity, in THIS DEVICE. When one tool has several data folders on the machine that someone logs in to, such as the per-account Codex homes Orca keeps, each account in THIS DEVICE names the folder it is logged in to, such as `~/.codex` or `orca 5b21e0c4`.

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
3. installs the binary into a folder on your `PATH` (see below), or to `%LOCALAPPDATA%\Programs\ai-usage\ai-usage.exe` on Windows, where it also adds that folder to your user `PATH`
4. saves the relay and joins the team, if you gave them
5. runs `ai-usage` once, which registers it with the scheduler and prints the report with a short guide under it

On macOS and Linux, an upgrade replaces the binary where it is: `~/.local/bin/ai-usage`, or the `ai-usage` found on `PATH`, so there is never a second copy. A new install goes into the first of `~/.local/bin`, `~/bin`, `/opt/homebrew/bin`, and `/usr/local/bin` that is on your `PATH` and that you can write to, and never into a folder that belongs to another tool, such as `~/.cargo/bin`.

When none of them is, the binary goes into `~/.local/bin`, and the installer adds that folder to `PATH` in the profile of your login shell, the one `$SHELL` names:

| Shell | Profile |
| --- | --- |
| zsh | `~/.zshrc`, or `$ZDOTDIR/.zshrc` |
| bash on macOS | the first of `~/.bash_profile`, `~/.bash_login`, and `~/.profile` that exists, else a new `~/.bash_profile`; a terminal on macOS opens a login shell, which reads only that one |
| bash on Linux | `~/.bashrc` |
| fish | `~/.config/fish/conf.d/ai-usage.fish`, or the same under `$XDG_CONFIG_HOME` |
| another shell | `~/.profile` |

It adds a block that starts with `# Added by the ai-usage installer`, once: running the installer again adds nothing. A new terminal finds `ai-usage` by name; in the terminal you installed from, use the full path the installer prints. With `AI_USAGE_NO_MODIFY_PATH=1` it leaves profiles alone and prints the line to add.

The installer asks no questions. Run it again to upgrade in place. It reads these variables:

| Variable | Meaning |
| --- | --- |
| `AI_USAGE_BIN_DIR` | where the binary goes, instead of the folder the installer picks; it edits no profile then |
| `AI_USAGE_NO_MODIFY_PATH` | leave shell profiles alone |
| `AI_USAGE_NAME` | this machine's name in the team, instead of its host name |
| `AI_USAGE_RELAY` | relay URL to save before the first run |
| `AI_USAGE_TEAM_KEY` | team key to join before the first run |
| `AI_USAGE_DOWNLOAD_URL` | where to download release files from, instead of the latest GitHub release |
| `AI_USAGE_ALLOW_ROOT` | install for root even though the installer runs under `sudo` |

To join a team while installing, which is how a teammate's machine joins yours (`ai-usage team key` prints the key and `ai-usage relay show` the relay):

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

Run the installer as the person whose usage you want to collect. Under `sudo` it stops, because the collector would register root's schedule and read root's tools, and could leave root-owned files in your home. On a server whose bots run as root, install it as root itself, with `AI_USAGE_ALLOW_ROOT=1` if you got there through `sudo`. For a container, see [In a container](#in-a-container).

On Linux the schedule needs `crontab`, which containers and some minimal systems lack; `ai-usage schedule run` takes its place there. The installer needs `curl` or `wget`, and `sha256sum`, `shasum`, or `openssl`.

## First run

The first run:

- creates the state folder
- generates a new team key, unless the installer joined a team, so an install starts as a team of one
- registers with the scheduler: a line in your crontab on Linux, a launch agent on macOS, a task named `ai-usage` in Task Scheduler on Windows
- collects and prints the report
- prints a short guide under the report: which scheduler now collects every 15 minutes, or what to do when none could be registered; what the team sees; and the commands worth knowing, with how to invite a colleague, how to pause the system scheduler's runs, and how to uninstall

The guide is printed once. When the scheduler collected first, or the first run you started used `--json`, the guide comes with the next `ai-usage` that prints text; `ai-usage report` never prints it. A machine that collected with a version before the guide never prints it.

The crontab line looks like this. It keeps the `PATH` of the shell that installed it, so that scheduled runs find the tools, and it names the state folder, so that scheduled runs use the same device, team key, and history as your own runs:

```
*/15 * * * * PATH='/home/ann/.local/bin:/usr/bin:/bin' '/home/ann/.local/bin/ai-usage' collect --quiet --home '/home/ann/.config/ai-usage' >/dev/null 2>&1 # ai-usage
```

On macOS the same command and `PATH` go into `~/Library/LaunchAgents/io.github.neoromantic.ai-usage.plist`, which launchd runs at every quarter hour in your login session, where the collector can read the keychain. Registering it needs no prompt, though macOS may show a notification that ai-usage added a background item. If the Mac missed runs while asleep, launchd runs it once on wake. With no one logged in at the screen, as over SSH before the first login, there is no session to register in, and `ai-usage status` says so. A crontab line an earlier version wrote on macOS is removed by the next run you start yourself, which may bring up the prompt asking to let your terminal administer the computer once; until then `ai-usage status` says the line is still there.

The Windows task runs `ai-usage.exe collect --quiet --home <state folder>` every 15 minutes. Unlike Task Scheduler's defaults, it also runs on battery, runs once after the computer wakes if it missed a run, never runs twice at once, and is stopped after 10 minutes.

To pause the collector, comment out the crontab line, disable the task in Task Scheduler, or on macOS run:

```
launchctl disable gui/$(id -u)/io.github.neoromantic.ai-usage
launchctl bootout gui/$(id -u)/io.github.neoromantic.ai-usage
```

Runs leave it that way, and `ai-usage status` says it was disabled by hand. `ai-usage schedule install` turns it back on.

The state folder is `~/Library/Application Support/ai-usage` on macOS, `$XDG_CONFIG_HOME/ai-usage` or `~/.config/ai-usage` on Linux, and `%LOCALAPPDATA%\ai-usage` on Windows (not the roaming profile, so the device id stays on this machine). `AI_USAGE_HOME` moves it. The scheduler entry follows the folder of the last run that registered it, so after moving the folder, run `ai-usage` once with the new `AI_USAGE_HOME`. It holds:

| File | Contents |
| --- | --- |
| `config.json` | this device's id, the relay, remembered and added homes, the `CLAUDE_CONFIG_DIR` value seen for them, which login each added Hermes home bills through, whether the schedule is off |
| `team.key` | the team's private key; this is the secret |
| `state.json` | the last good readings, sessions, and health, and whether the guide is still to be printed |
| `team-cache.json` | the team's snapshots from the last read |
| `samples/` | one file per day of samples, kept for 90 days |
| `run.lock`, `config.lock` | keep two runs from overlapping, and two commands from changing `config.json` at once; held with the system's file lock, which ends with its process |

## Teams

A team is one key pair. Every machine in the team stores the same private key. The team's name is the fingerprint of its public key, like `472ghuwcctyu…`, which is not secret.

To add a machine to your team:

1. On a machine in the team, run `ai-usage team key`. It prints the private key, `aiu-team-1:…`. Anyone who has it can read the team's snapshots and publish into the team, so share it the way you would share a password.
2. On the new machine, install with `AI_USAGE_TEAM_KEY` as shown above, or run `ai-usage team join` and paste the key.
3. Make sure both machines use the same relay: `ai-usage relay show`.

Joining saves the previous key as `team.key.previous`, drops the cached team, and asks the relay, if one is set, to remove this machine from the old team. `ai-usage team` lists the team's machines. `ai-usage team forget-device ID` removes a retired machine's snapshot from the relay and from this machine's team view. A snapshot that is not updated expires on its own: after 90 days, or sooner for a machine that has reported for less time than that, but never sooner than 7 days after its last update.

A key cannot be revoked. To shut someone out, start a new team and join the remaining machines to it: on one machine, move `team.key` out of the state folder, run `ai-usage` to generate a new key, and give that key to the others.

In the team view, token counts add up across machines. Quota percentages do not: an account's quota is the newest reading any machine has for it.

A machine goes by its host name; a Mac goes by the local host name in Sharing settings, which does not change with the network. `ai-usage name set NAME` gives it another, and the team sees it after the machine's next run.

## Relay

The relay is a small HTTP API that keeps one snapshot per machine. It is needed only for the team view; without one, ai-usage reports on this machine alone.

```sh
ai-usage relay show                          # the relay in use, or "no relay configured"
ai-usage relay set https://relay.example.com
ai-usage relay clear
```

A release build may carry a default relay, chosen when the release was built. `relay set` overrides it and `relay clear` goes back to it. `AI_USAGE_RELAY` overrides both for one run.

The relay accepts only a small, fixed-shape usage snapshot signed by the team key. It checks the signature and the shape and stores nothing else. It limits requests per IP address, writes per team, and machines per team (100). Each IP address can also add only 5 new teams and 100 new machines a day.

To run your own relay on Vercel with Upstash for Redis (formerly Vercel KV), use the button, which copies this repository into your Git account and deploys it with a new database:

[![Deploy with Vercel](https://vercel.com/button)](https://vercel.com/new/clone?repository-url=https%3A%2F%2Fgithub.com%2Fneoromantic%2Fai-usage&project-name=ai-usage-relay&repository-name=ai-usage-relay&stores=%5B%7B%22type%22%3A%22integration%22%2C%22integrationSlug%22%3A%22upstash%22%2C%22productSlug%22%3A%22upstash-kv%22%2C%22protocol%22%3A%22storage%22%7D%5D)

Or, from a clone with the Vercel CLI logged in, run `sh scripts/deploy-relay.sh`. On any other machine, `ai-usage relay serve` runs the same relay, and so does the official Go image in Docker. Then point every machine in the team at it with `ai-usage relay set URL`. [docs/relay.md](docs/relay.md) covers each way, the limits, and updates; [docs/relay-protocol.md](docs/relay-protocol.md) specifies the protocol for other clients and relays.

## Commands

| Command | What it does |
| --- | --- |
| `ai-usage [--json] [--offline] [VIEW] [DISPLAY]` | collect now and print the report; `--offline` skips the relay |
| `ai-usage collect [--quiet] [--json] [--offline] [--home DIR] [VIEW] [DISPLAY]` | the same; the scheduler runs `collect --quiet --home DIR`, and `--home` overrides `AI_USAGE_HOME` |
| `ai-usage report [--json] [VIEW] [DISPLAY]` | print the last collected report without collecting |
| `ai-usage status [--json] [DISPLAY]` | version, last success, last error, relay, schedule, update, and each tool's state, with full error texts |
| `ai-usage team` | the team fingerprint and its machines |
| `ai-usage team key` | print the team's private key |
| `ai-usage team join [KEY]` | join a team; the key is read from standard input when omitted |
| `ai-usage team forget-device ID` | remove a machine's snapshot from the relay |
| `ai-usage home` | list every data folder this machine reads, per tool |
| `ai-usage home add PROVIDER DIR... [--quota-from PROVIDER:DIR]` | read more data folders; see [Other data folders](#other-data-folders) |
| `ai-usage home remove PROVIDER DIR... [--forget]` | stop reading folders added before; `--forget` also drops the sessions counted from them, for folders another collector reads now |
| `ai-usage relay show` | print the relay in use |
| `ai-usage relay set URL` | save a relay URL, `http://` or `https://` |
| `ai-usage relay clear` | forget the saved relay |
| `ai-usage relay serve [--addr :8080] [--client-ip-header NAME]` | run a relay; behind a reverse proxy, name the header it sets to the client's address |
| `ai-usage name show` | print this machine's name in the team |
| `ai-usage name set NAME` | name this machine in the team, instead of its host name; at most 64 characters |
| `ai-usage name clear` | go by the host name again |
| `ai-usage schedule install` | register with the scheduler, and let later runs keep it registered |
| `ai-usage schedule remove` | unregister, and stop later runs from registering again |
| `ai-usage schedule status` | whether the scheduler runs this binary with this state folder, or was disabled by hand |
| `ai-usage schedule run` | be the scheduler where there is none, as in a container: collect now and every 15 minutes until stopped |
| `ai-usage update` | check for a release now |
| `ai-usage version` | print the version |
| `ai-usage help` | print usage |

`VIEW` is at most one of these:

| Flag | View |
| --- | --- |
| `--projects` | every project on this machine, with its tokens |
| `--tokens` | every team account's tokens, machine by machine |
| `--devices` | every machine in the team, with none folded away |

`DISPLAY` flags change how the console looks. `--json` ignores them.

| Flag | Meaning |
| --- | --- |
| `--color=auto\|always\|never` | `auto`, the default, colors a terminal unless `NO_COLOR` is set or `TERM` is `dumb` |
| `--ascii` | draw with ASCII only; the default when the locale (`LC_ALL`, `LC_CTYPE`, `LANG`) is not UTF-8, except in Windows Terminal |
| `--width N` | lay out for N columns, 80 to 160; the default is the terminal's width, else `COLUMNS`, else 80 |

| Variable | Meaning |
| --- | --- |
| `AI_USAGE_HOME` | the state folder |
| `AI_USAGE_RELAY` | the relay URL, overriding the saved one |
| `AI_USAGE_NAME` | this machine's name in the team, overriding the saved one in the runs that see it; launchd and cron do not pass on a shell's variables, so name a computer with `ai-usage name set` |
| `AI_USAGE_NO_SCHEDULE` | when set, this run does not register with the scheduler |
| `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `GROK_HOME`, `HERMES_HOME` | another data folder for that tool |

## Other data folders

Each tool's default folder (`~/.claude`, `~/.codex`, `~/.grok`, `~/.hermes`) is always read, and so are the profiles inside a Hermes folder, the per-account Codex folders Orca keeps, and the Claude Code folder the Claude desktop app keeps for each agent-mode (Cowork) session. A folder named by `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `GROK_HOME`, or `HERMES_HOME` in a run you start is remembered for scheduled runs. Folders nothing names, such as bots running under other users, are added by hand:

```
ai-usage home add hermes /srv/bots/alpha/.hermes /srv/bots/beta/.hermes --quota-from codex:/srv/bots/.codex
```

Hermes keeps its own login for a Codex or Grok subscription, so the collector cannot read which account it uses. By default a Hermes folder is taken to use the account logged in to `~/.codex` or `~/.grok`. `--quota-from` names the folder whose login it uses instead, one per tool, which is then read too; it covers the profiles inside each Hermes folder. `ai-usage home` shows every folder a run reads and what each bills through, and marks one that is gone as missing. `ai-usage home remove` will not remove a folder Hermes folders take their quota from until they are removed or named another. Name a folder whose login is kept fresh, by Codex itself or by whatever refreshes it for the bots. A copy that only seeded another login store goes stale: it still says whose it is, but its quota reads fail with HTTP 401.

Each Hermes session's tokens go to the login of the folder it was read from. When Hermes folders bill one route through different logins, as bots on a shared login beside your own Hermes, the Hermes account shows the quota of the login most of its tokens in the last 90 days went through.

Accounts and quota come from the tools themselves. Codex is asked through the `codex` on `PATH`; when that one is missing or too old to answer, through the copy the ChatGPT app on macOS or OpenAI's extension for VS Code, Cursor, or Windsurf bundles. Claude Code's quota comes from its usage cache, which it updates only while it runs. When Claude has refused a request because a window was full, and the cache is older than the refusal, the account the session belongs to shows that window alone, at 100% as of the refusal, until it resets. Usage counted in a folder while its tool never answered goes to the first account it names there. Usage counted while it said nobody is logged in stays unknown.

The Claude desktop app's agent-mode sessions are the exception. The app keeps a folder for each under `local-agent-mode-sessions` in its data folder (`~/Library/Application Support/Claude` on macOS, `%APPDATA%\Claude` on Windows), and every run finds them there again, so they are never remembered. Nobody logs in to them, and nothing is asked about them: each session's tokens go to the account the app recorded for it, or to `unknown` when that record is missing or names none. From that record the collector takes only the account and the folders, never the session's title, first message, or system prompt. The project is the first folder you gave the session, or `Claude app` when you gave none, since its working directory is inside the app's virtual machine. These sessions have no quota of their own; when Claude Code reports a quota for the same account, that reading applies.

On a server, run the collector as a user that can read those folders. Hermes databases are read in place, read-only; a database Hermes has open is read the way any SQLite reader reads it, and one nobody has open is read without taking a lock.

## In a container

The collector runs in a container as on any Linux machine: install it inside, as the user whose tools it should read, and each container, such as each bot, is a machine in the team. Three things differ.

- The state folder holds the device id and the team key, so it must outlive the container. Keep that user's home, or `AI_USAGE_HOME`, on a volume or a bind mount. A binary installed there, in `~/.local/bin`, keeps its updates too. As root, a new install goes into `/usr/local/bin` instead, which the container does not keep, so add `AI_USAGE_BIN_DIR="$HOME/.local/bin"` to the install command.
- A container's host name is random. Name the machine when installing with `AI_USAGE_NAME`, later with `ai-usage name set`, or with `AI_USAGE_NAME` in the container's environment.
- Containers rarely have cron. `ai-usage schedule run` is the scheduler there: it collects at once and then every 15 minutes, until it is stopped. Run it beside the container's main process, under its service manager if it has one, or from its entrypoint.

To install into a running container, with the team key on standard input rather than in the command:

```sh
ai-usage team key | docker exec -i -u app mybot sh -c 'key=$(cat)
  url=https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh
  script=$(curl -fsSL "$url" || wget -qO- "$url") &&
    printf "%s\n" "$script" | AI_USAGE_NAME=mybot AI_USAGE_TEAM_KEY="$key" sh'
```

Add `AI_USAGE_RELAY` if the team uses its own relay.

Then start `ai-usage schedule run` in it as that user. The first run's report and guide say the schedule is not registered, since there is no crontab; after `schedule run` starts, `ai-usage status` says it collects every 15 minutes. [docs/containers.md](docs/containers.md) has an entrypoint, a Dockerfile, and a service for s6-overlay.

## JSON for agents

`ai-usage --json` and `ai-usage report --json` print the report as JSON with `"schema_version": 2`. A field changes meaning only with a new schema version.

```
schema_version, generated_at
collector   version, device, team, last run, last success, last error,
            relay, schedule, and update state
providers[] claude, codex, grok, hermes: status (ok, partial, error, skipped), error, homes,
            accounts[]: label, home, plan, headline_percent, level (ok, warning, critical, unknown),
                        quota with from and windows[] (percent, level, resets_at, pace), link,
                        sessions, tokens, linked_usage[], last_active_at, projects[]
team        pulled_at, devices[], and providers[] with accounts summed across devices:
            plan, quota, link, tokens, per_device[], linked_usage[]
```

`ai-usage status --json` prints `schema_version`, `collector`, and `sources[]`. Every field is described in [docs/json-schema.md](docs/json-schema.md).

## Privacy

A snapshot is what leaves the machine. It is at most 32 KB, and the relay rejects any field it does not know.

Sealed with the team key, so only the team can read them:

- the machine's name (its host name, unless `ai-usage name set` gave it another) and OS user name
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
rm "$(command -v ai-usage)"
rm -r ~/Library/Application\ Support/ai-usage
```

Linux:

```sh
ai-usage schedule remove
ai-usage team forget-device d-…
rm "$(command -v ai-usage)"
rm -r "${XDG_CONFIG_HOME:-$HOME/.config}/ai-usage"
```

Windows, in PowerShell:

```powershell
ai-usage schedule remove
ai-usage team forget-device d-…
Remove-Item -Recurse "$env:LOCALAPPDATA\Programs\ai-usage", "$env:LOCALAPPDATA\ai-usage"
```

Then open *Edit environment variables for your account* from the Start menu and remove the `…\AppData\Local\Programs\ai-usage` entry from `Path`.

On macOS and Linux, if the installer added `~/.local/bin` to your shell profile, remove the block that starts with `# Added by the ai-usage installer`, or `conf.d/ai-usage.fish` for fish.

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
