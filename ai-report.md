# AI usage reporter

Requirements for a small open-source collector. Not an implementation plan.

The collector is a binary. A person runs it, or the system scheduler runs it. It is not a resident daemon. Each run reads local usage, asks the installed harnesses for quota, publishes this device's encrypted snapshot to one public store, reads the other devices in the team, and exits.

It covers Codex, Claude, Grok, and Hermes. It does not call a provider itself and it does not touch logins.

Source notes are from [PitStop](https://github.com/Livin21/pitstop). Account switching, credential snapshots, token refresh, session warming, and any direct provider call are not adopted.

## Product rules

- Keep it small. No extra service to deploy, no database, no plugin system.
- One codebase and one binary for Windows, macOS, and Linux.
- Install with one command. The first run registers itself with the system scheduler: cron on macOS and Linux, Task Scheduler on Windows. Later runs are just the binary.
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

## Implementation status

Status as of 2026-09-23. The Go code in this repository implements version 1. See `README.md` for use and `docs/` for the JSON schema, the relay, and releasing. Everything above is covered except the items listed below. After it was built, the code went through an adversarial review against this document, and the confirmed findings were fixed. A real end-to-end run with two devices and a local relay passed: token sums, a quota that is not summed, the offline backlog, and `forget-device`.

Done, in short:

- **Binary and scheduling**
  - One pure-Go binary (`cmd/ai-usage`) for Windows, macOS, and Linux on amd64 and arm64. `install.sh` and `install.ps1` install it with one command. `install.sh` refuses `sudo`, so that it never registers root's crontab.
  - The first run of a release build registers `*/15` in cron, or a 15-minute Task Scheduler task. The Windows task comes from a task definition that also runs on battery.
  - The scheduler entry names this device's state folder (`collect --quiet --home DIR`), so scheduled runs never fork the device, the key, or the ledger.
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
  - Codex: `codex app-server`, with only `initialize`, `account/read` (no refresh), and `account/rateLimits/read`.
  - Grok: its local files.
  - An explicit "not logged in" clears the logged-in mark.
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
  - Clients verify every document they pull and keep one per device.
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
- **The first run attributes all local history to the account logged in at that moment.** Older logs do not say which account wrote them. A forward clock jump of more than 90 days prunes the ledger, and history is attributed again after it.
- **The device cap can be exceeded briefly when two new devices write at once.** Counting and then writing is not atomic on the REST store. A Lua script or `SET NX` per slot would fix it.
- **Pace uses only this device's samples,** not the team's.
- **Two collectors that read the same harness home count it twice in the team view.** Examples are two OS users sharing a home, or a `CODEX_HOME` inherited by a second collector. Each device publishes its own totals.
- **The Claude desktop app's agent-mode sessions (`local-agent-mode-sessions`) are not discovered.**
- **Panics in probe goroutines.** A panic inside a goroutine a probe starts still ends the process. The command-level rescue records it and still runs the release check.
- **Rare miscounts in logs:**
  - A sub-agent transcript whose parent session aged out of the 90-day window is counted as a session of its own.
  - A Claude sub-agent file in a home that lacks its parent's transcript is not counted.
  - Two different Codex events in a forked session family with identical `(total, last)` usage are counted once.
  - Claude side calls (compaction, titles, classifiers) are counted only once Claude Code exits and writes its cost state.
  - Codex compactions in rollouts written before Codex added `token_usage_record` lines are not counted.
