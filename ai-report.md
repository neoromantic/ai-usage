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
- A snapshot is encrypted to the team's public key before it is stored. Anyone can download the file. Only a collector that holds the private key can read it. The program does not shell out to `gpg`.
- One small HTTP API stores every install. The body is ciphertext. The backing store is a key-value store that overwrites one document per device, such as Vercel KV. It is not a git history.
- A write is accepted only when it is signed by the team private key for that public fingerprint. The server checks the signature and stores the bytes. It does not decrypt them. A holder of the private key can publish and can read the team back.
- A second keypair hidden in the official binary is not used. The program is open source, so any key shipped inside it can be copied and is then not a secret. That cannot prove a caller is the unmodified official build.
- Someone can still generate their own key and store their own team. They cannot overwrite another team. The API limits body size and request rate so that junk teams cannot run away with the store.
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
- GnuPG, keyrings, and trust prompts.
- Publishing the 15-minute series as git commits.
- A database or document store that we administer by hand. The API and the key-value store are the relay.

## Later

- A separate unencrypted feed of anonymous totals, with no team, device, or account label. The team file stays encrypted.
- Opt-out telemetry back to the project.
- A notifier when a window crosses a threshold. Version 1 only marks 75% and 90% in the console output.
- Account switching, after monitoring works.
