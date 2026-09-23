# AI usage reporter

Requirements for a small open-source collector. Not an implementation plan.

The collector is a binary. A person runs it, or the system scheduler runs it. It is not a resident daemon. Each run reads local usage, asks the installed harnesses for quota, publishes this device's encrypted snapshot to one public store, reads the other devices in the team, and exits.

It covers Codex, Claude, Grok, and Hermes. It does not call a provider itself and it does not touch logins.

Source notes are from [PitStop](https://github.com/Livin21/pitstop). Account switching, credential snapshots, token refresh, session warming, and any direct provider call are not adopted.

## Product rules

- Keep it small. No extra service to deploy, no database, no plugin system.
- One codebase and one binary for Windows, macOS, and Linux.
- Install with one command. The first run registers itself with the system scheduler: cron on Linux, launchd on macOS, Task Scheduler on Windows. Later runs are just the binary.
- The binary updates itself from GitHub releases. The run that notices a new release finishes on the old binary and leaves the new binary in place, so the next run is the update. Self-update cannot be turned off.
- Reading never writes. It must not change, refresh, or replace Codex, Claude, Grok, or Hermes credentials, keychain items, or cookies. It must not run a login, logout, or token-refresh command.
- Account switching is deferred. Version 1 only monitors.
- A person may log out of one account and log into another in the same Codex, Claude, Grok, or Hermes setup. Those are two accounts. Both stay on the same device and the same OS user.

## Functional

- Discover Codex, Claude, Grok, and Hermes, including non-default data directories. Skip a tool that is not installed. One missing tool does not fail the others.
- Do not open credential files and do not read account emails out of them. Account identity is whatever stable id the installed harness reports when asked. If that report contains an email, store the string only as the harness's label.
- Ask the installed harness, not the provider. A one-shot command that prints quota and account identity is allowed, including through tmux when the tool has no other interface. A command that starts a session, sends a prompt, or spends quota is not allowed. `codex exec` and `claude -p` are examples of commands that are not allowed.
- Record each quota window the harness or the local files already contain: how full it is, and when it resets. The headline number is the fullest window. If this account has no reading, say unknown. Do not invent a percent or a reset time.
- When the logged-in account changes, keep the previous account. New token growth is attributed to the account that was logged in for that sample. The previous account keeps the quota last reported for it and the tokens attributed while it was active. A switch in the middle of a 15-minute gap can attach that gap to the wrong account. That inaccuracy is accepted.
- Do not add quota percentages across devices. Add token counts. One account has one quota reading, taken from the newest sample for that account.
- Keep the last good reading and show how old it is.
- Sample every 15 minutes. Keep samples for 90 days.
- From stored samples, say when the current pace would fill a window before its reset.
- Record usage already present locally: sessions, input and output tokens, and cache reads and writes. Attribute a session to the project or working directory stored in the log. Roll a sub-agent's tokens into its parent session.
- A reading is identified by team, device, OS user, provider, and account label. The team is the public key fingerprint. It is not the only identity of a row.
- Each team has one keypair, in the style of a public and private key, not a GnuPG installation. Every collector in the team stores both halves. The first run generates the keypair. Joining a team means copying the private key. The public fingerprint is the folder name and is not secret. The private key is the only secret.
- One small HTTP API stores every install. The backing store is a key-value store that overwrites one document per device, such as Vercel KV. It is not a git history.
- The API accepts only a usage snapshot. The body is JSON with a fixed set of fields: counts, percents, timestamps, and short labels. Unknown fields, nested files, and free-form text are rejected. The whole document must stay small, on the order of 32 KB. A store that accepts an opaque encrypted blob cannot tell a usage report from something else, so version 1 does not accept one.
- A write is accepted only when that JSON is signed by the team private key for that public fingerprint. The server checks the signature, checks the shape, and stores the document. A holder of the private key can publish and can read that team back. They cannot overwrite another team.
- A second keypair hidden in the official binary is not used. The program is open source, so any key shipped inside it can be copied. After that, the server cannot tell the official build from any other client. The shape check is what remains: a copied client can submit only the same small usage record, which is not a useful place to keep other material.
- Someone can still generate their own key and store their own usage-shaped team. The API limits request rate and how many devices one team may have, so junk teams cannot run away with the store.
- A device keeps collecting when the relay is unreachable, and sends the backlog when it returns.
- What is sent is aggregates only: counts, percents, timestamps, account labels, project or working directory, and collector health. Never prompts, tool arguments, file contents, or Hermes memory.
- Offer two views of the same readings:
  - console text for a person, including the other devices in the team
  - versioned JSON for an agent, with a schema that can change only by version
- No web dashboard in version 1.
- Show provider sections separately. The same label on two providers is two records. Two accounts on one provider are two records, still tagged with the same device and OS user.
- Mark high utilization. PitStop's menu uses a warning at 75% and a critical mark at 90%.

## Non-functional

- A failed parser, a stale file, a harness that will not answer, or an unreachable relay is visible in the status and does not stop the other sources.
- Status includes the collector version, the last successful collection, and the last error.
- The collector stores no provider secrets.
- Last good readings survive a restart.

## External

- The project is open-source software, under the MIT license.
- It lives at `github.com/neoromantic/ai-usage`.
- It is meant to be installed by people outside this team, so setup and upgrade stay documented and non-interactive where the host allows it.

## Not in version 1

These PitStop behaviors are out, even though the project does them:

- Switching the live account, writing `auth.json`, or swapping a keychain credential.
- Refreshing OAuth tokens, including for inactive accounts.
- Calling Anthropic, OpenAI, xAI, Google, or any other provider endpoint from this program.
- Session warming, or any action that spends a token.
- Auto-switch when a limit is crossed.
- A web dashboard.
- API-equivalent dollar cost. It is not what the subscription charges.
- Turning self-update off.
- A second, app-wide keypair whose only job is to pretend the caller is the official binary.
- An opaque ciphertext blob as the stored document. The server must be able to see that the body is a usage snapshot.
- GnuPG, keyrings, and trust prompts.
- Publishing the 15-minute series as git commits.
- A database or document store that we administer by hand. The API and the key-value store are the relay.

## Later

- A separate unencrypted feed of anonymous totals, with no team, device, or account label. The team file stays encrypted.
- Opt-out telemetry back to the project.
- A notifier when a window crosses a threshold. Version 1 only marks 75% and 90% in the console output.
- Account switching, after monitoring works.
- A web report on the relay's site. The page gets the team key from the URL fragment or from what the person pastes, never from the server. It verifies and unseals the snapshots in the browser. The relay keeps serving only sealed, signed snapshots.
- A better console report, where it is quick to see which accounts are near their limits, who is spending them, and what changed since the last look.
- A matrix view of who uses what: devices (hosts and bot containers) against accounts, with tokens in the cells, in the console and in JSON.
- An interactive console view, in the manner of btop: pick an account, a host, or a bot and a period, and drill down.
- Managing subscriptions on the relay: price, renewal date, and owner per account, so that cost per consumer and idle subscriptions show. It needs more thought: this is data people enter, not what collectors see, and the relay stores only sealed, signed snapshots today.

## Next: usage over time

Decided on 2026-09-23 with the owner:

- A consumer is a host: a person and their machine are the same thing here. A bot in its own container runs its own collector, so it is a host too. There is no separate "person".
- 90 days of history is enough.
- The report needs periods: today, 7 days, and 30 days. It also needs a breakdown by day and by quota week.
- Utilization is how much of the quota the team uses: for each account and window cycle, the fullest reading before the window reset. For example, "the weekly window was used 100%, 100%, and 60% in the last three weeks".
- Subscriptions are the accounts the collectors see. A subscription registry is later (see above).
- The console is enough for now. The web report is a later version.
- Different use cases may need different views; that is decided when building them.

Sketch:

- The snapshot gains day buckets: tokens per day per account, for up to 90 days. The team view adds them up per device and per account. The 32 KB snapshot cap and the relay's read cap grow to hold them. Sessions already keep when each account's share last grew; per-day growth needs the samples, which are already written every run and kept 90 days.
- The snapshot gains quota cycles per account: each window's reset time and the fullest percentage seen before it. Readings come from every device, so the team view takes the fullest per cycle.
- New views: `--days` and `--weeks` tables, and a utilization view per account. JSON carries the same data.
- An account switch is placed at the run that first saw the new login, which is within 15 minutes. Placing it more exactly, from the quota jump in Codex's rollout, is not worth it.

## Backlog

Recorded on 2026-09-23 with the owner. Not started.

- **A generated device name.** A device gets a readable name from one small, free LLM call instead of the bare host name. `Mac.localdomain` is the kind of name this replaces.
  - Input: host name, OS user, OS and machine model, whether it runs in a container, and the account labels the harnesses report, emails included. The owner accepts that for this one call these reach the relay and the model in the clear. Everywhere else the labels stay sealed. The product rules say so, and that exception belongs in them.
  - With the official relay, the call goes through a relay endpoint that holds the project's Vercel AI Gateway token, so no client carries a key. The endpoint is rate-limited like the rest of the relay. A relay without that endpoint, no relay, or a failed call leaves the host name, as today.
  - A new device asks at its first run. A device that was never named by hand asks once, at its first run after the update. The answer is saved and is not regenerated when the host name or accounts change later.
  - A name set by hand, with `name set` or `AI_USAGE_NAME`, always wins and is never replaced.
  - Open: the model, the prompt, the 64-character limit, and how to avoid two devices in a team getting one name, since the relay cannot see the others' names.
- **The installer puts `ai-usage` on `PATH`.** On a Mac with bash, `install.sh` left the binary off `PATH` and only printed the line to add.
  - It installs into a writable folder that is already on `PATH`, preferring one in the user's home.
  - When there is none, it installs into `~/.local/bin` and adds one marked line to the profile of the shell the person uses: `~/.zshrc`, `~/.bash_profile` on macOS or `~/.bashrc` on Linux, and fish's `conf.d`. Running it again does not add the line twice. It also says that a new terminal is needed, or prints the full path for this one.
  - Self-update and the scheduler entry already follow the binary wherever it is.
- **A short guide after install.** After the first run, the person sees a few lines, not the full help:
  - what happens now: collection runs by itself every 15 minutes, and the team sees sealed totals;
  - the commands worth knowing: the report (`ai-usage`), `status`, `name set`, and inviting a colleague with the team key;
  - how to pause or remove it.

  The binary prints it at its first interactive run, so `install.ps1` and container installs get it too.
- **Prune the tests.** Remove the tests that pin incidental detail rather than behavior:
  - tests that repeat one another;
  - exact message wording, where only the meaning matters;
  - the data of one real machine, where a general case already covers it;
  - internal helpers whose callers are already tested.

  Each behavior in this document keeps a test, and the console keeps its golden files. The relay's checks and the ledger's attribution keep their full coverage.
- **No team specifics.** Remove what only fits this team from code, tests, comments, and docs, this file included: its machines, people, paths such as `orbit`, and one-off stories about one machine or one database. Support for Orca, Hermes, and the ChatGPT app stays, since they are products other people use too.
- **A refactoring review for short, expressive code.** Find and fix:
  - repeated logic;
  - long functions;
  - layers, options, and branches nothing needs;
  - dead code;
  - comments that restate the code.

  The CLI, the snapshot (`schema_version` 2), and the relay protocol stay compatible, and behavior does not change. Run it with the current tests, before they are pruned, so they check the refactor.

## Implementation status

Status as of 2026-09-23. The Go code in this repository implements version 1. See `README.md` for use and `docs/` for the JSON schema, the relay, and releasing. Everything above is covered except the items listed below. After it was built, the code went through an adversarial review against this document, and the confirmed findings were fixed. A real end-to-end run with two devices and a local relay passed: token sums, a quota that is not summed, the offline backlog, and `forget-device`.

Done, in short:

- **Binary and scheduling**
  - One pure-Go binary (`cmd/ai-usage`) for Windows, macOS, and Linux on amd64 and arm64. `install.sh` and `install.ps1` install it with one command. `install.sh` refuses `sudo`, so that it never registers root's crontab.
  - The first run of a release build registers `*/15` in cron on Linux, a launch agent with a `StartCalendarInterval` at minutes 0, 15, 30, and 45 on macOS (unlike `StartInterval`, it runs once on wake for runs missed during sleep), or a 15-minute Task Scheduler task on Windows. The Windows task comes from a task definition that also runs on battery.
  - macOS uses launchd because writing the crontab there waits on a prompt to let the terminal administer the computer: an install that nobody answered was killed after 30 seconds and left no schedule. The launch agent runs in the login session, so the collector can read the keychain. It is loaded with `launchctl bootstrap gui/<uid>`; a Mac with no one logged in at the screen gets no schedule until someone logs in, and `status` says so. A run the agent itself started never boots the agent out, since that would stop the run before it loaded the agent again; it only writes the plist back. A crontab line an earlier version wrote is removed by the next run the person starts, since writing the crontab can prompt; until then `status` reports it, so cron and launchd never both run the collector unnoticed. `launchctl disable` alone does not unload a loaded agent, so the README pauses with `disable` and `bootout`.
  - The scheduler entry names this device's state folder (`collect --quiet --home DIR`), so scheduled runs never fork the device, the key, or the ledger.
  - A collection holds the run lock from start to end, because it reads and rewrites the state, samples, team cache, and, when it updates itself, the binary. Two collections at once would each redo the probes and log scans, and the one that saved last would drop the other's result. The lock is the system's file lock (`flock`, `LockFileEx` on Windows) on `run.lock`. The system releases it when its process ends, however that happens, so a run that was killed never holds the others off, and two runs can never both take over a lock left behind. A run that hangs holds the lock until it is killed. A release from before the file lock held `run.lock` by creating it. A lock in that format is honored while its process runs, for up to 10 minutes, so upgrading during a scheduled run does not start a second collection.
  - Reading never waits: `report` and `status` read the saved files, which are replaced atomically. `ai-usage` started while another run is collecting waits for that run and shows its result instead of collecting twice. It collects itself after the wait when that run saved nothing new, as when it was `update`, or collected with other inputs: another release, another relay, or other homes, such as a folder added in the meantime or named by this run's environment. The scheduler's `collect --quiet` skips. `config.json` has a short lock of its own, so `home add/remove` and `relay set/clear` never wait for a collection, except `home remove --forget`, which changes the ledger. A run records only the folders it found beyond the remembered ones, so it never brings back one removed meanwhile. `team join`, `team forget-device`, `update`, and `schedule install/remove` change what a collection also writes or acts on: the team key, the team cache, the binary, and the scheduler entry that a run registers again. So they wait up to 3 minutes for it.
- **Self-update**
  - Updates come from GitHub releases, with SHA-256 checksums, inside the run lock. The downloaded binary must start and report the release's version before it replaces the old one.
  - The new binary replaces the old one after the run has read everything, so the next run is the update.
  - It cannot be turned off. It still runs when a collection fails or panics, so a release with a parser bug cannot pin a device to itself. Only dev builds skip it.
- **Discovery**
  - Default homes are found, plus `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `GROK_HOME`, `HERMES_HOME`, and Hermes profiles (`<home>/profiles/<name>`).
  - Homes named by the environment are remembered, with the exact variable value, so scheduler runs still find them.
  - Orca's per-account Codex homes (`<app data>/orca/codex-accounts/<id>/home`, marked `.orca-managed-home`) are found by path and probed in parallel, each with its own `CODEX_HOME`, so every Orca account shows its own quota. Orca hard-links rollouts into every home; one inode is read once.
  - `ai-usage home add PROVIDER DIR...` adds homes nothing names, such as bots running under other users on a server. `--quota-from codex:DIR` names the Codex or Grok home whose login a Hermes home bills through; that home is read too.
- **Probes** only ask the harnesses, from the user's home directory:
  - Claude: `claude auth status --json`, and Claude Code's own config and cached usage. Never the keychain or `.credentials.json`.
  - Codex: `codex app-server`, with only `initialize`, `account/read` (no refresh), and `account/rateLimits/read`. When a `codex` cannot start app-server or exits without answering, as one too old to have it does, the probe tries the copies the ChatGPT app (and the older Codex app) on macOS and OpenAI's extension for VS Code, Cursor, and Windsurf bundle, newest first; someone who uses only the app has no `codex` on `PATH` at all. When none answers, the error ends with the last line the first one printed, so the report says why the account is `unknown`.
  - A harness runs with its own folder and the system's install folders added to `PATH` where `PATH` lacks them, keeping the order of what it has. launchd and cron give a short `PATH`, and a `codex` or `claude` installed with npm is a script that runs `env node`. Without this, such a `codex` exited before it answered, and its account read as `unknown` in scheduled runs only.
  - Usage counted in a home whose harness never answered goes to the account that home first names, as the first run's history does. Each session read that run is claimed through the home it was read from, so a home that still fails keeps its own history `unknown`. A home that names its account in a run that cannot read all its logs claims at the first run that can. The state records every home that has answered, even to say nobody is logged in, and never prunes that record. So usage from a logged-out spell is not claimed by a later login, even after the earlier account ages out of the ledger. A ledger from before that record claims nothing once any account of the harness is named.
  - Grok: its local files.
  - An explicit "not logged in" clears the logged-in mark. It is a problem only for a home that holds usage in the 90-day window. A home with no login and no usage is a tool that is installed but not used. Claude Code, for example, creates `~/.claude` as soon as it is asked who is logged in.
  - One source that panics becomes that source's error, and the others still run.
- **Logs**
  - Sessions, tokens, and cache reads and writes are read per project. That includes Claude side calls recorded in its cost state and Codex compaction records.
  - Sub-agents roll up into their parents. All homes of one provider are read together, so forked, resumed, or duplicated transcripts count once, even across homes.
  - A line too long to read is skipped, not fatal.
  - Hermes: `state.db` is read in place, read-only (a database Hermes has open through its `-wal` and `-shm`, one nobody has open as immutable), never copied except for an odd leftover. One read is one transaction, so a commit between its queries cannot split a session wrongly; a database that a writer opens during an immutable read is read again live. A symlinked `state.db` is read where it points. Tokens come from `session_model_usage` split by billing provider, auxiliary calls included. A time stored as an ISO string instead of REAL seconds is read too (one real database had one). A gateway session with no working directory is listed under its Hermes home, so each agent on a server is its own project.
- **Accounts and quota**
  - Accounts are tracked across switches. Token growth goes to the account logged in at that sample. Each account keeps its last good quota and shows how old it is.
  - Samples are taken every 15 minutes and kept for 90 days. Pace predicts when a window fills before its reset.
  - The headline is the fullest window that has not reset since the reading. If every window has reset, or there is no reading, it shows "unknown". Marks appear at 75% and 90%.
- **Names and containers**
  - A machine goes by its host name unless it is named. On macOS that is the local host name from Sharing settings, since the network's name for a Mac changes from one network to the next, and a Mac without one reads as `Mac.localdomain`. Naming: `ai-usage name set NAME` (at most 64 characters), `AI_USAGE_NAME` at install, or `AI_USAGE_NAME` in the environment, which overrides the saved name in the runs that see it. A container's `schedule run` sees the container's environment. launchd and cron do not see a shell's, so a variable set only in a shell profile names the device in the runs started by hand but not in the scheduled ones, and `ai-usage name` says so. The name is the sealed device label, so the team sees a rename after the machine's next run.
  - `ai-usage schedule run` is the scheduler where there is none, as in a container: it collects at once and then at every quarter hour, each time in a new process of the binary on disk, so a self-update takes effect at the next collection. It holds a lock file (`schedule.lock`) while it runs. A run that finds it held records the schedule as foreground and never registers with cron or launchd, and `status` and the report call the schedule stopped once the lock is free. A missing `crontab` now says to use `schedule run`.
  - `docs/containers.md` covers the rest: keep the user's home on a volume, so the device, key, ledger, and binary last; install as the bot's user; run `schedule run` from the entrypoint or as an s6-overlay service.
- **Hermes on a subscription** (`openai-codex`, `xai-oauth`) is linked to the Codex or Grok account it is assumed to bill through: the one logged in to the home `--quota-from` names for that Hermes home (one per harness, paths matched after resolving symlinks), else to `~/.codex` or `~/.grok`. The Hermes row shows that account's own reading, marked as borrowed, and the snapshot says so in `quota_from`. The linked account shows what Hermes spent on it, session by session, without adding it to its own tokens; the snapshot carries that in `linked`, so the team view credits each login with what went through it.
- **Views**: console text and versioned JSON (`schema_version` 2). Both include the other devices in the team. The console was redesigned for teams of a dozen accounts and a couple of dozen machines: accounts grouped by provider with a bar, both windows and the reading's age, a USED BY column, a DEVICES section that folds healthy machines past 12, and a legend that lists only the marks on screen. It fits 80 columns and uses more from 100. Golden files cover 80, 100, 120, and 140 columns, with and without color.
- **Team key**: Ed25519, and its fingerprint names the team. Joining means `ai-usage team join` with the exported private key.
- **Snapshot**: fixed-shape, strict JSON of 32 KB or less.
  - Counts, percents, timestamps, and provider and window names are plain.
  - Device label, OS user, account label, project paths, and error text are sealed with the team key.
  - Every write is signed.
- **Relay**: `ai-usage relay serve`, or the Vercel function in `api/`, backed by Vercel KV (Upstash for Redis from the Vercel Marketplace, through the `KV_REST_API_*` variables).
  - It checks the signature and the exact shape, and rejects stale writes.
  - Rate limits apply per IP, and per day to new teams and new devices from one IP (IPv6 per /48). A forwarding header is trusted only when the operator names it and the request comes through their proxy.
  - Snapshots expire 7 to 90 days after their last update.
  - Clients verify every document they pull and keep one per device. Text opened from a snapshot is shown without control characters.
  - A team read lists at most the device cap, the devices stored first, so racing first writes cannot make it too large to answer. The team's device set lives as long as its longest-lived record.
  - A scheduled run publishes every time and reads the team once an hour; a run someone starts reads it every time. Reads carry the whole team, so this cuts the square term of the traffic by four.
- **Resilience**
  - An unreachable relay, or one that holds a newer snapshot for this device id, leaves the newest snapshot pending and is shown. The next run that reaches the relay sends it.
  - A damaged `state.json` is set aside as `state.json.bad` rather than stopping every run.
- **Status**: `ai-usage status` shows the version, the last success, the last error, and the relay, schedule, and update health.

Deferred or not done. These are cumbersome, or they need an action outside this repository:

- **Relay deployment (done).** The relay runs at https://ai-usage-relay.vercel.app. It is the Vercel project `acmeworks/ai-usage`, with Vercel KV (Upstash for Redis from the Vercel Marketplace) on Pay-As-You-Go. The project deploys on every push to `main`. The repository variable `AI_USAGE_RELAY_URL` bakes this URL into release builds as the default relay.
- **CI runs on GitHub.** Tests run on Linux and macOS. Windows only cross-compiles and runs the installer smoke test: Windows is not a supported collector host for now.
- **Windows is not supported yet. Nothing has run on real Windows.** Task Scheduler registration from XML (the task has no explicit user, so it relies on `schtasks /Create /XML` using the caller), the `.old` rename during self-update, and `install.ps1` are covered only by unit tests and the CI definitions.
  - `schtasks` starts a console program, so a console window can flash every 15 minutes. Fixing that needs a GUI-subsystem launcher, or `conhost --headless`, which only newer Windows builds have.
  - `schtasks /Query` errors are localized, so any query failure is treated as "no task".
- **Claude quota freshness.** Claude Code writes its usage cache only when it runs, so an idle machine shows an old reading with its age. A fresh reading without a session would mean driving `claude` in tmux and scraping `/usage`. That is fragile and deferred. Claude transcripts also carry `quotaLimits` on rejected requests; they are not used yet.
- **Hermes' quota is borrowed, and its account is assumed.** Hermes keeps its own Codex or Grok login and records no quota, so its row shows the quota of the account it is taken to bill through (see above). Where that is wrong, `home add --quota-from` corrects it per Hermes home. Hermes accounts are keyed by billing provider, so Hermes homes on one machine that bill through different logins share one row. It shows the quota of the login most of its 90-day tokens went through; the tokens themselves go to each login exactly. Hermes' `codex_app_server` runtime also writes Codex rollouts; if its `CODEX_HOME` is a read home, those tokens show under both Hermes and the Codex account. The team view pairs a Hermes row with a source reading by provider and reading time only. Grok labels an account read from its logs by its `user_id` UUID when the harness does not report a better one.
- **Orca and past sessions.** Orca hard-links every rollout into every account home, so a session is attributed by the weekly reset its rollout recorded: the account whose current weekly window resets within a minute of it. A session from before the current week cannot be matched and goes to the first home's login. An Orca account can show quota used and no sessions. `ORCA_USER_DATA_PATH` is not honored.
- **Deploy order for `quota_from` and `linked`.** The relay accepts each from the build that added it. Collectors built from this tree get HTTP 422 from an older relay and keep the snapshot pending, and after a 4xx other than 409 a run does not read the team either. The relay deploys on push to `main`, so it is always deployed before a release is tagged.
- **Account identity is the harness's label,** usually an email. One email in two workspaces or organizations is one account.
- **The first run attributes all local history to the account logged in at that moment.** Older logs do not say which account wrote them. The same goes for usage counted while the harness named nobody, until it first names an account. A forward clock jump of more than 90 days prunes the ledger, and history is attributed again after it. Samples written while the account was unknown keep it unknown; only pace reads them.
- **The device cap can be exceeded briefly when new devices write at once.** Counting and then writing is not atomic on the REST store. A new device counts the team again after its write, and one that is not among the devices that joined first deletes its record and gets 403. Joining is ranked by each handler's clock, so a device told it was stored can be pushed out by a slower racer; while the team is over the cap, every write counts it (one SCARD), and a device past the cap gets 403 and loses its record at its next write. So a device is left out of team reads for one run at most, and then says so. A Lua script or `SET NX` per slot would make the cap exact.
- **Pace uses only this device's samples,** not the team's.
- **Two collectors that read the same harness home count it twice in the team view.** Examples are two OS users sharing a home, or a `CODEX_HOME` inherited by a second collector. Each device publishes its own totals.
- **A Hermes profile is not a device of its own.** A collector counts every profile inside the homes it reads as its own usage. So two gateways that share one Hermes home, each running its own profile, show as one device.
- **Moving a home to another collector needs `--forget`, while the home can still be read.** The container's first run counts the home's last 90 days again. `ai-usage home remove PROVIDER DIR --forget` reads the home's sessions under the run lock, once no run reads the home, and drops them from the host's ledger, except those a home the host still reads also holds. It needs the home to be there, since the ledger does not record which home each session came from, and it refuses a home runs would still find: a profile of a kept home, an app's account home, or one an environment variable names. Without `--forget`, the host keeps the sessions until they are 90 days old.
- **The Claude desktop app's agent-mode sessions (`local-agent-mode-sessions`) are not discovered.**
- **Panics in probe goroutines.** A panic inside a goroutine a probe starts still ends the process. The command-level rescue records it and still runs the release check.
- **Rare miscounts in logs:**
  - A sub-agent transcript whose parent session aged out of the 90-day window is counted as a session of its own.
  - A Claude sub-agent file in a home that lacks its parent's transcript is not counted.
  - Two different Codex events in a forked session family with identical `(total, last)` usage are counted once.
  - Claude side calls (compaction, titles, classifiers) are counted only once Claude Code exits and writes its cost state.
  - Codex compactions in rollouts written before Codex added `token_usage_record` lines are not counted.
