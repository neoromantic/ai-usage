# JSON output, schema version 4

`ai-usage --json`, `ai-usage collect --json`, and `ai-usage report --json` print one report object. `ai-usage status --json` prints a smaller object, described at the end.

A field changes meaning only with a new `schema_version`. New fields can appear within a version, so ignore the ones you do not know. [Changes from version 3](#changes-from-version-3) lists what version 4 changed, and [Added within version 4](#added-within-version-4) what it added since.

Conventions:

- Times are RFC 3339 strings. A time that never happened is `null`.
- An optional string is `null` when there is nothing to say. An error field is `null` when there was no error.
- Lists are `[]` when empty, never `null`.
- Percentages run from 0 to 100, except a forecast, which can pass 100.
- `tokens` has the four counts the tools record over the last 90 days. `usage`, `days`, and `window_tokens` count input plus output tokens only, cache left out.
- `burn`, `burn_days`, and `window_burn` count weighted tokens: every kind of token, each weighed by what the provider's API charges for it against an input token. Claude weighs output 5, a cache read 0.1, and a cache write 1.25, or 2 when it is kept an hour; Codex weighs output 8 and a cache read 0.1; Grok weighs output 5 and a cache read 0.25. A cache write counts as input where a provider does not price it apart. A Hermes call takes the weights of the provider its account is named after, and Claude's when it names none. Weighted tokens are a proxy for how much quota a session uses, not the provider's quota formula. The console shows them, as `M weighted tokens`. `fresh` counts input, cache writes, and output, unweighted.
- Periods. Team objects count UTC days, as devices send them: a `usage` or a team object's `burn` has four periods, each ending with the report's UTC day: `today`, `7d`, `30d`, and `90d`, the last that many UTC days up to and including it, and `days` are UTC days. This device's own accounts and projects count their `burn`, `fresh`, and `active_sessions` in local periods: `today` is the hours since local midnight, and `7d`, `30d`, and `90d` the last 168, 720, and 2160 hours, ending with the current hour; their `burn_days` are local days. So this device's own numbers can differ from its row in the team's.
- A `state` is one of `out`, `over`, `tight`, `ok`, `under`, and `unknown`; see [states](#states).

## Report

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | number | `4` |
| `generated_at` | time | when the report was made |
| `collector` | object | this collector's health; see [collector](#collector) |
| `attention` | list | what needs attention now, most urgent first; see [attention](#attention) |
| `providers` | list | one entry each for `claude`, `codex`, `grok`, and `hermes`, in that order; see [providers](#providers) |
| `projects` | list | this device's projects over every account, most weighted tokens in the last 7 days first; see [projects](#projects) |
| `team` | object | every device in the team, this one included; see [team](#team) |

### collector

| Field | Type | Meaning |
| --- | --- | --- |
| `version` | string | the collector version, such as `v1.2.3`, or `dev` for a build from source |
| `device` | string | this device's id, `d-` and 24 hex digits, made on the first run |
| `device_label` | string | the name this device goes by in the team: one set with `ai-usage name set` or `AI_USAGE_NAME`, else the host name |
| `os_user` | string | the OS user name |
| `team` | string | the team fingerprint |
| `last_run_at` | time | when the last run happened |
| `last_success_at` | time | the last run that collected without an error |
| `last_error` | string | the last run's error, if it had one. A `state.json` that did not parse is reported here; the collector then starts again from an empty state and keeps the damaged file as `state.json.bad` |
| `last_error_at` | time | when that error happened |
| `relay.url` | string | the relay in use |
| `relay.last_push_at` | time | when this device's snapshot last reached the relay |
| `relay.last_pull_at` | time | when the team was last read from the relay |
| `relay.pending` | bool | the newest snapshot has not reached the relay yet, because the relay could not be reached or it holds a newer snapshot for this device id |
| `relay.last_error` | string | the last relay error |
| `schedule.registered` | bool | the system scheduler, or `ai-usage schedule run`, runs this binary |
| `schedule.foreground` | bool | the last scheduled run came from `ai-usage schedule run`, not the system scheduler |
| `schedule.error` | string | why it is not registered |
| `update.checked_at` | time | the last release check |
| `update.latest` | string | the newest release that check saw |
| `update.staged` | string | a release already installed; the next run uses it |
| `update.error` | string | the last update error |
| `health` | list | how the collection, the relay, and the update are doing, one entry each in that order, as the console's header shows them; see below |

A `health` entry:

| Field | Type | Meaning |
| --- | --- | --- |
| `item` | string | `collection`, `relay`, or `update` |
| `status` | string | the first status in the table below that applies to the item |
| `state` | string | how the item is: `ok`; `warn`, it needs a look; `error`, it fails; `off`, it is not in use or has not run; `info`, news. The console's header colors the item's dot by it: green, yellow, red, gray, and blue |
| `at` | time | for `collection`, `last_success_at`, or with `failed` `last_error_at`, and `null` with `never`; for `relay`, the older of `relay.last_push_at` and `relay.last_pull_at`, and `null` with `none`; for `update`, `update.checked_at` |
| `release` | string | with `staged`, `update.staged`; with `available`, `update.latest`; `null` with any other status |

| `item` | `status` | `state` | When |
| --- | --- | --- | --- |
| `collection` | `never` | `error` | `last_run_at` is `null`: this device has never collected |
| `collection` | `failed` | `error` | the last run failed: `last_error_at` is set, and `last_success_at` is older or `null` |
| `collection` | `unscheduled` | `warn` | `schedule.registered` is `false` |
| `collection` | `ok` | `ok` | otherwise |
| `relay` | `none` | `off` | `relay.url` is `null` |
| `relay` | `failing` | `error` | `relay.last_error` is set |
| `relay` | `pending` | `warn` | `relay.pending` is `true`, or `relay.last_push_at` is `null` |
| `relay` | `stale` | `warn` | the device has a team, and `relay.last_pull_at` is `null` or over an hour old, three of the reads scheduled runs make |
| `relay` | `ok` | `ok` | otherwise |
| `update` | `dev` | `off` | `version` is not a release such as `v1.2.3`, so the collector does not update itself |
| `update` | `staged` | `info` | `update.staged` is set |
| `update` | `failed` | `error` | `update.error` is set |
| `update` | `unchecked` | `off` | `update.checked_at` is `null` |
| `update` | `available` | `warn` | `update.latest` is newer than `version` |
| `update` | `ok` | `ok` | otherwise |

### attention

Each entry is one thing that needs attention, from the team's view of each subscription and device. The report lists them most urgent first, by `kind` in the order of this table; within a kind, the earliest `at` first, which for an `over` without `at` is its `resets_at`, the highest `percent` first for `tight` and `outside`, the lowest forecast first for `under`, and by device for `error`. The fields that apply depend on the kind; the others are absent. A reader should skip a kind it does not know.

| `kind` | When | Fields |
| --- | --- | --- |
| `out` | a window of a subscription is at 100% | `provider`, `account`, `name`, `window`; `at` is when it resets |
| `over` | a window of a subscription will run out by its reset, at its pace | `provider`, `account`, `name`, `window`; `at` is when it runs out, absent when it runs out at its reset, `resets_at` when it resets, `percent` its forecast |
| `tight` | a window of a subscription logged in on this device will end its reset in the `tight` state | `provider`, `account`, `name`, `window`; `resets_at` is when it resets, `percent` its forecast |
| `outside` | a subscription has `outside` set: a window filled while no team device spent on it | `provider`, `account`, `name`; `at` is when it began to rise, `percent` how many points it rose |
| `error` | a device's collector or one of its tools fails; on this device, also a run a bug stopped before it could report, its relay, or its update check; on an `old` device that is not silent, its update check. A quota that could not be read is not an error | `devices` names the device; `message` is the error, which starts with `relay` or `update` when it is theirs |
| `no_reading` | a subscription with weighted tokens in the last 7 days has `no_reading` set | `provider`, `account`, `name`; `message` is the reason, with what to do |
| `silent` | a device has not reported for a day, with or without an error in its last report, and no other device took its place | `devices` names the device; `at` is when it last reported; `message` is the error it last reported, if it had one |
| `duplicate` | a device has `replaced_by` set: it was reinstalled, and its old record stays on the relay | `devices` names the device; `device_id` is its id, for `ai-usage team forget-device`, which takes its first 8 hex digits too; `message` is the label of the device that replaced it; `at` is when it last reported |
| `old` | devices run an older release than the team's newest | `devices` lists every one of them; `message` is the newest release; `at` is the earliest `behind_since` among those that have reported for 7 hours since it and are not `silent`, absent when none has. Devices whose release has `limitations` have an entry of their own, first, with those in `limitations`: they need a reinstall. A `duplicate` device is not counted |
| `under` | past half of a subscription's main window, its forecast is under 50% | `provider`, `account`, `name`; `resets_at` is when it resets, `percent` its forecast |

A window is one with `limits`: the account's main one, or one that limits it more. An `over`, `tight`, or `under` entry has `reading_age_seconds` when its window's reading is stale, and an `out` entry when its reading is over 6 hours old, as the provider can reset a full window early.

| Field | Type | Meaning |
| --- | --- | --- |
| `kind` | string | one of the kinds above |
| `provider` | string | the account's provider |
| `account` | string | the account's label |
| `name` | string | the account's short name |
| `window` | string | the window's name, when it is not the account's main one |
| `devices` | list of strings | the names of the devices it is about |
| `device_id` | string | a `duplicate` device's id |
| `at` | time | as the table says for each kind |
| `resets_at` | time | when the window resets |
| `percent` | number | the window's forecast at its reset, or the points an `outside` window rose |
| `reading_age_seconds` | number | how old the reading is, when it is stale |
| `message` | string | an error's text, the error a silent device last reported, why a subscription has no reading, the device that replaced a duplicate, or the newest release |
| `limitations` | list of strings | what the releases of the devices of an `old` entry cannot do, as a team device's `limitations` says |

### providers

| Field | Type | Meaning |
| --- | --- | --- |
| `provider` | string | `claude`, `codex`, `grok`, or `hermes` |
| `status` | string | `ok`; `partial` when something failed but some data was read; `error` when nothing could be read; `skipped` when the tool is not installed |
| `error` | string | what failed, joined with `; ` |
| `homes` | list of strings | the data folders read |
| `covered_since` | time | the start of the first hour this device's ledger holds the tool's tokens for; `null` when it holds none. Before it the report knows nothing of them, so a `90d` that starts earlier counts less than was spent. Claude Code deletes its transcripts after 30 days unless `cleanupPeriodDays` says otherwise |
| `accounts` | list | every account seen on this device for this tool; see below |

An account is one login. After you switch accounts, the previous one stays, with the tokens it used and the last quota reported for it.

| Field | Type | Meaning |
| --- | --- | --- |
| `label` | string | what the tool calls the account, such as an email address or a user id; `unknown` when it did not say |
| `name` | string | the account's short name: the one the team gave it with `ai-usage alias`, else the part of an email before the `@`, the first 8 characters of an id (a UUID, or 16 or more letters, digits, `-`, and `_` with a digit among them), or else the whole label. When two accounts of one provider would go by the same name, in any case, both use their full label, even when the name is an alias; the team account's `alias` still holds it |
| `current` | bool | logged in right now. It turns `false` when the tool answers that nobody is logged in; a tool that does not answer leaves the last account current |
| `home` | string | the first of `homes` the account is logged in to now; absent when it is not logged in |
| `plan` | string | the plan the tool reports |
| `state` | string | the worst state of the windows that limit the account, those with `limits`; `unknown` without a reading |
| `quota` | object | the last good quota reading, `null` if there never was one; see below |
| `link` | object | `{provider, label}`: the account of another tool this one is assumed to bill through, `null` when there is none. Only Hermes has one: its `openai-codex` account is linked to a Codex account and `xai-oauth` to a Grok one. Each Hermes folder is taken to bill through the account logged in to the folder `ai-usage home add --quota-from` named for it, else to `~/.codex` or `~/.grok`; when its folders bill through several, the link is the one most of the account's tokens in the last 90 days went through. Hermes keeps its own login, so the link is an assumption, not a reading |
| `sessions` | number | sessions in the last 90 days |
| `tokens` | object | tokens in the last 90 days; see below |
| `usage` | object | input plus output tokens on this device in each period |
| `days` | list of numbers | input plus output tokens on this device per UTC day, newest first: the first is the report's UTC day, the next the day before, and so on, for up to 90 days. Trailing zeros are left out |
| `burn` | object | weighted tokens on this device in each local period |
| `fresh` | object | input, cache writes, and output on this device in each local period |
| `burn_days` | list of numbers | weighted tokens on this device per local day, newest first: the first is the report's local day, for up to 90 days. Trailing zeros are left out |
| `transcript_gaps` | object | where a Claude account's transcripts miss tokens over the last 90 days: `null_snapshots`, the replies whose transcript ends without a final token count, and `over_tracked`, the sessions whose Claude Code count has over twice the output of their transcripts. ai-usage uses Claude Code's own count where it wrote one. Absent for other tools, and when both are 0 |
| `linked_usage` | list | `{provider, sessions, tokens}` for each other tool assumed to bill through this account, such as Hermes on this Codex login; `[]` when none. These tokens are that tool's and are not in `tokens` |
| `last_active_at` | time | the newest session activity that used this account |
| `projects` | list | `{path, folders, sessions, tokens, usage, burn, fresh, active_sessions, last_active_at}` per project, this account's part only, most weighted tokens first; see [projects](#projects) |

A Hermes account is named after the billing provider it used, such as `openai-codex`, `xai-oauth`, `anthropic`, or `openrouter`. Its tokens include Hermes' auxiliary calls, such as title generation, compression, and vision, under the provider each call billed. An auxiliary call on a fallback route, which Hermes records with no provider, goes to the `unknown` account.

A session's tokens go to the account logged in to the home it was read from. Codex's `homes` include the per-account homes Orca keeps under its app data folder (`codex-accounts/<id>/home`); one with nobody logged in adds no account. Orca links each rollout file into `~/.codex` and every account's home, so such a file is read once. Its tokens go to the one account, among those logged in to the homes that hold the file, whose current quota reading has its longest window resetting within a minute of the reset time the session last recorded. When no account matches, or several do, they go to the account in the first of those homes, `~/.codex` first. A session from an earlier week cannot be matched this way; a collector running every 15 minutes matches sessions while they run.

Claude's `homes` include the Claude Code home the Claude desktop app keeps for each agent-mode session (`local-agent-mode-sessions/…/local_<id>/.claude` in its data folder). Nobody logs in to such a home, so it is never an account's `home`. A session read there goes to the account the app recorded for it, `unknown` when that record is missing or names none, and its project `path` is the first folder the person gave the session, else `Claude app`.

`tokens` has four counts: `input`, `output`, `cache_read`, and `cache_write`. `input` does not include cache reads.

`quota`:

| Field | Type | Meaning |
| --- | --- | --- |
| `observed_at` | time | when the tool took the reading; on a team account, the newest window's reading |
| `age_seconds` | number | the reading's age when the report was made |
| `stale` | bool | a window's reading is stale; see the window's `stale` |
| `source` | string | `harness` when the tool answered a command, `cache` when it came from the tool's own cache file, `log` when it came from the tool's logs, `rejection` when Claude refused requests because windows were full: those windows alone at 100%, as of the newest refusal, until they reset; only on this device's accounts |
| `from` | string | the tool whose account took the reading, when it is the reading of the account in `link`, unchanged, observation time included; absent otherwise. With no reading for that account, `quota` is `null` |
| `device` | string | the device whose reading is newest, as `host (user)`; only on team accounts |
| `windows` | list | the windows the tool reported; see below |

A window:

| Field | Type | Meaning |
| --- | --- | --- |
| `name` | string | the tool's name for it, such as `5h` or `7d` |
| `percent` | number | how full it was at the reading; kept after `resets_at` passes, as the last value seen |
| `resets_at` | time | when it resets, if the tool said |
| `minutes` | number | its length in minutes; absent when the tool did not say |
| `main` | bool | the account's main window: the one named `7d`, else the first a week long, else the longest. An account with windows has exactly one |
| `limits` | bool | the window limits the account: it is the main window, or it has not reset and is `out`, `over`, or fuller than the main one, or used at all when the main one has reset. The account's `state` is the worst of these, and the console shows each one that is not the main window as a row of its own |
| `observed_at` | time | when this window was read. In the team view the windows of one account can come from different devices and readings |
| `stale` | bool | this window's reading is more than 6 hours old and the window was not full. A full window stays full until it resets, however old the reading |
| `reset` | bool | the window has reset since it was read, so how full it is now is not known; its `state` is `unknown` and `forecast` is `null` |
| `unread` | bool | a Claude 5-hour or weekly window the newest reading does not cover, because a request refused for a full window reads that window alone. Its `percent` is 0, `resets_at` and `forecast` are `null`, and its `state` is `unknown`. Absent when `false` |
| `state` | string | see [states](#states) |
| `forecast` | object | how full the window will be at its reset if it is used from now on at its pace; see below. `null` when the length or the reset time is unknown, when it has reset, and in its first tenth unless it is over already |

`forecast`:

| Field | Type | Meaning |
| --- | --- | --- |
| `percent` | number | the window's `percent` plus `pace` times the hours from the reading to the reset, as a whole percent up to `999`. With the `window` pace from the window's start, that is `percent` divided by `elapsed`. Over 100 means demand is larger than the quota: the window runs out before it resets |
| `elapsed` | number | the share of the window that had passed at the reading, from 0 to 1. The window began at `resets_at` minus its length, which is `minutes`, else the length its name starts with, such as `5h` or `7d` |
| `runs_out_at` | time | when the window reaches 100% at that pace, when the forecast is over 100 before it is rounded and the window is not full yet; `null` otherwise. A forecast of exactly 100 runs out at the reset, and has none |
| `basis` | string | the pace taken: `window`, the average since the window began, or since its last reading at 0% after that; or `recent`, the least-squares slope of its readings of the last 24 hours, when they span an hour or more and it is faster. A report from an earlier release has none: read it as `window` |
| `pace` | number | that pace, in percent of the window an hour |

The readings are this device's samples and the team's reads of the last 2 days, of the same window and reset, and the reading itself; a drop of more than 2 points between two starts them again. The average includes nights and weekends, so one busy hour does not raise an alarm, and the recent pace catches a window that fills faster.

#### States

| State | When |
| --- | --- |
| `out` | the window is at 100% |
| `over` | its forecast is 100% or more |
| `tight` | its forecast is 85% to 99% |
| `ok` | its forecast is 50% to 84% |
| `under` | its forecast is below 50% |
| `unknown` | there is no forecast: no reading, a window that has reset since it was read or that the reading does not cover, an unknown length or reset time, or less than a tenth of the window gone and not over |

### projects

`projects` in the report lists this device's projects, with every account's tokens in each. The same object lists one account's part of each project under that account, without `providers`.

A project is a git repository: sessions in its subfolders and its linked worktrees count under it with its own. A working folder outside any repository is a project of its own. The report finds each folder's project on the file system when it is made, without running git, so a worktree that is gone still counts where its layout says:

- A folder's project is the nearest folder at or above it with a `.git` entry, short of the home folder: a home folder that is a repository is the project of the sessions in it alone. A `.git` file that names a folder in `<common>/worktrees` marks a linked worktree, whose project is the folder that holds the common git folder when that is `.git`, or hidden as `.bare` is; else the folder the common git folder's config names as its worktree, as a submodule's does, or the folder whose `.git` file names it, as `git clone --separate-git-dir` makes; and else the bare repository itself. Any other `.git` file, as a submodule's, makes its folder a project.
- A Claude Code worktree, `<repo>/.claude/worktrees/<name>`, with no `.git` entry left counts as `<repo>` does.
- A Codex worktree, `~/.codex/worktrees/<id>/<name>`, with no `.git` entry left is the project named `<name>` that a `.git` entry found, when there is exactly one. Else the Codex worktrees of that name are one project, whose path is `~/.codex/worktrees/*/<name>`.
- On macOS nothing is read inside Desktop, Documents, Downloads, iCloud Drive, other cloud storage, other apps' data, or other volumes, since the system would ask the person first. A folder there is its own project, but for a Claude Code worktree.

Links are resolved, so a folder reached through one is the same folder. On macOS a link into one of the folders above is not followed.

| Field | Type | Meaning |
| --- | --- | --- |
| `path` | string | the project's folder |
| `folders` | number | how many working folders count under the project, 1 or more |
| `sessions` | number | sessions in the last 90 days |
| `tokens` | object | tokens in the last 90 days |
| `usage` | object | input plus output tokens in each period |
| `burn` | object | weighted tokens in each local period |
| `fresh` | object | input, cache writes, and output in each local period |
| `active_sessions` | object | the sessions last active in each local period, `today`, `7d`, `30d`, and `90d`, by the hours they spent in; `sessions` counts those with tokens in the last 90 days |
| `providers` | list of strings | the tools that used the project, most tokens first; only in the report's `projects` |
| `last_active_at` | time | the newest session activity in the project |

The report's list is sorted by `burn` in `7d`, in whole millions as the console shows it, then by `burn` in `90d`, then by path.

### team

| Field | Type | Meaning |
| --- | --- | --- |
| `pulled_at` | time | when the team was read from the relay; `null` if it has not been, in which case the lists hold this device only |
| `latest_version` | string | the newest collector release any device in the team runs, or the newest this device's update check saw, whichever is newer |
| `estimated` | bool | the weighted tokens of some device are estimated: a release before v0.4.0 sends none, so they are worked out from its input and output, at the ratio its 90 days' tokens give. Such devices have `burn_estimated` |
| `held_back_by` | list of strings | the devices whose release keeps this device from sending its own weighted days: until every device the team knows runs v0.4.0 or later, each device sends the snapshot an older release reads, without them |
| `estimated_reason` | string | why totals are estimated, with what to do, in a line, such as `leo-air runs v0.3.4: it updates itself within 15 minutes`, `3 devices run releases before v0.4.0: …`, a device that `has not reported since …: forget it with ai-usage team forget-device …`, or `the team was last read 4h ago`; `null` when `estimated` is `false` |
| `devices` | list | one entry per device; see below |
| `providers` | list | `{provider, accounts}` with each account summed across devices; see below |
| `matrix` | object | every device against every subscription; see [matrix](#matrix) |

A device:

| Field | Type | Meaning |
| --- | --- | --- |
| `device` | string | its id |
| `label` | string | its name, the host name unless one was set, or `(unreadable)` if it does not decrypt with this team's key |
| `os_user` | string | its OS user, or `(unreadable)` |
| `this_device` | bool | this is the device making the report |
| `collector_version` | string | its collector version |
| `collected_at` | time | when it made its snapshot |
| `age_seconds` | number | the snapshot's age |
| `last_success_at` | time | its last run without an error |
| `last_error` | string | its last error |
| `last_error_at` | time | when that error happened; `null` from a collector before v0.4.0 |
| `update_error` | string | why its last release check failed, without IP addresses, while no newer release waits for its next run. It reaches the team one run after the check, and `null` for this device, whose own is `collector.update.error`, and for a collector before this field |
| `sources` | list | `{provider, status, error}` for each tool on that device |
| `error` | string | what fails on the device now: a tool's error, named after its provider, else the last run's error when it is newer than the last success; `null` when nothing does |
| `silent` | bool | it has not reported for a day |
| `old` | bool | it runs an older release than `latest_version` |
| `behind_since` | time | for an `old` device, its first run on the release it runs that this device's reads of the team found while a newer one was out, made since the read before, so that time it did not run, as a laptop asleep, does not count. The device has not updated itself for at least that long; once it has reported for 7 hours since, it had the runs to. `null` otherwise, until a read finds such a run, and once the device has not reported for a day, until it reports again |
| `not_updating` | bool | an `old` device that does not update itself: it has reported for 7 hours since its `behind_since` and is not `silent`. These are the devices behind the `old` entry's `at` in `attention` |
| `usage` | object | its input plus output tokens in each period, over every account. A device's days count from the UTC day it collected on, so one that last reported three days ago adds nothing to `today` |
| `burn` | object | its weighted tokens in each period, as `usage` counts them |
| `burn_estimated` | bool | `burn` is estimated from its input and output, as its release sends no weighted tokens |
| `gap` | object | `{seconds, at}`: the longest time in the last 2 days this device's reads of the team did not find the device's snapshot growing, when over an hour, and when it began; `null` otherwise. For another device, only time this device was reading the team counts: a stretch ends at this device's last read before that device reported again |
| `replaced_by` | string | the label of the device that reinstalled this one: the same name and OS user, reporting while this one is silent; `null` otherwise |
| `limitations` | list of strings | what its release cannot do: `no_claude_quota` before v0.2.3, and `no_update_errors` before v0.2.5 |

A team account. Each provider's accounts are in the order the report lists them: the worst `state` first (`out`, `over`, `tight`, `ok`, `under`, then `unknown`), ties to the one with less left of its main window, then by label.

| Field | Type | Meaning |
| --- | --- | --- |
| `label` | string | the account label |
| `name` | string | its short name, as for a device's account |
| `alias` | string | the name the team gave it with `ai-usage alias`: the newest one any device set, `null` when there is none or it was cleared |
| `subscription` | bool | an account with a quota of its own: every Claude, Codex, and Grok account. Hermes is a tool, not a subscription: what it spends through a login is that login's use, and its other accounts, such as API keys, have no quota |
| `current` | bool | the account is logged in on this device |
| `devices` | list of strings | the devices that saw it, as `host (user)` |
| `plan` | string | the plan from the newest snapshot, by `collected_at`, that has one; `null` when none has |
| `state`, `quota` | | as for a device's account, from the newest reading of each window any device has; percentages are never added. `quota.from` is set when that reading is the linked account's. Snapshots do not say where a reading came from, so another device's refusal is an ordinary reading here |
| `link` | object | `{provider, label}`, as for a device's account; `null` when there is none. This device's accounts use their own link. For another device's account, whose snapshot carries no link, it is the one account on that snapshot of the tool in `quota.from` with the same reading: the same observation time and windows. When none or several match, `label` is `""` |
| `sessions`, `tokens`, `usage` | | summed across devices; the account's own, without what linked accounts spent through it |
| `users` | number | how many devices have tokens on the account since its main window began, or in the last 7 days when it has none, what linked accounts spent through it included |
| `busiest` | string | the name of the one of those devices with the most tokens; `null` when there is none |
| `last_active_at` | time | the newest activity on the account on any device, what Hermes spent through it included. A device whose Hermes spent through several logins gives each one its newest activity, since its snapshot does not say which login that went through |
| `per_device` | list | `{device, device_id, current, sessions, tokens, usage, burn, last_active_at}` for each device that has the account, `device` as `host (user)`; most weighted tokens in `7d` first, then in `90d`, then by `device` |
| `linked_usage` | list | `{provider, label, devices, sessions, tokens}` for each account of another tool that spent through this one on any device, as each device counted it session by session, summed across devices, with the devices it ran on; `[]` when none. These tokens are that account's and are not in `tokens` |
| `burn` | object | the account's own weighted tokens, summed across devices |
| `burn_estimated` | bool | some of `burn` is estimated, from a device whose release sends no weighted tokens |
| `no_reading` | string | why a subscription has no reading of its main window, with what to do, such as `leo-air runs v0.3.0: reinstall with install.sh`, when that is known; `null` otherwise |
| `outside` | object | `{points, since}`: the main window rose `points` since `since`, in the last day, while the team spent next to nothing on it, so someone outside the team uses it; `null` otherwise |

### matrix

Who spends what: each device against each subscription, and the tokens that have no subscription. A device's Hermes account counts under each login it spent through, with the part of its tokens that went through that login in the last 90 days. Its snapshot does not split its days, or its tokens since a window began, by login, so each login gets that part of them, and so does what it counted through no login. One that spent through no login counts under the login it is linked to on that device, or under no quota when there is none.

| Field | Type | Meaning |
| --- | --- | --- |
| `columns` | list | the subscriptions, grouped by provider, the group with the most weighted tokens in `7d` first, and within it the subscription with the most first; then one column per provider for the tokens with no quota, in the same order. Ties go to `90d`, then to the name; see below |
| `rows` | list | the devices, the most weighted tokens in `7d` first, then in `90d`, then by name; see below |

A column:

| Field | Type | Meaning |
| --- | --- | --- |
| `provider` | string | the provider |
| `label` | string | the subscription's account label; absent in a no-quota column |
| `name` | string | the account's short name, as in the team account's `name`; the provider in a no-quota column |
| `no_quota` | bool | the column holds the provider's tokens that have no subscription, such as Hermes on an API key |
| `state` | string | the subscription's `state`; `unknown` in a no-quota column |
| `percent` | number | how full the subscription's main window is, when that is known and it has not reset since; `null` otherwise |
| `usage` | object | the team's input plus output tokens on it in each period, what Hermes spent through the login included |
| `window_tokens` | number | the team's input plus output tokens since the main window began, what Hermes spent through the login included |
| `burn`, `window_burn` | | the same as weighted tokens |

A row:

| Field | Type | Meaning |
| --- | --- | --- |
| `device` | string | the device's name, as its `label` |
| `device_id` | string | its id |
| `cells` | list | one per column, in the order of `columns` |
| `usage` | object | the device's tokens over every column, in each period |
| `share` | object | the device's part of the team's tokens over every column, in percent, in each period; see the cell's `share`. The rows' shares add up to 100 |
| `burn`, `burn_share` | | the same as weighted tokens |
| `burn_estimated` | bool | the device's `burn` is estimated, as on its team device |

A cell:

| Field | Type | Meaning |
| --- | --- | --- |
| `usage` | object | the device's input plus output tokens on the column in each period |
| `window_tokens` | number | the device's input plus output tokens since the column's main window began |
| `share` | object | the device's part of the column's tokens, in percent, in each period: `today`, `7d`, `30d`, and `90d`, as in `usage`. It is the cell's `usage` over the column's, so a column's shares add up to 100. A period's is `null` when the team spent nothing on the column in it. A device that spent none has `0`. It says how the tokens split, not how much of the quota the device used |
| `burn`, `window_burn`, `burn_share` | | the same as weighted tokens |

## Changes from version 3

- A matrix cell's `share` is an object with a value for each period, the device's part of the column's tokens in it. It was a number, an estimate of how much of the column's main window the device used: its `window_tokens` over the column's, times the column's `percent`, which a reader can still work out from those fields.
- A matrix row has a `share` of the team's tokens in each period.
- `status --json` has the same `schema_version`, and did not change.

## Added within version 4

- `update_error` and `behind_since` on a team device, so that the team can see why a device does not update itself, and for how long it has not. An `error` entry in `attention` for an `old` device whose update check fails, and `at` on the `old` entry. No field changed meaning, so the version stays 4.
- `collector.health`, how the collection, the relay, and the update are doing, and `limits` on a window, which says the window limits the account. A report from an earlier release has neither: read a missing `health` as `[]` and a missing `limits` as `false`, and then only the main windows as limiting. `ai-usage report --from` fills both in for such a report, as this release makes them. No field changed meaning, so the version stays 4.
- `not_updating` on a team device, so that each device the `old` entry in `attention` counts is marked. A report from an earlier release does not have it; `ai-usage report --from` fills it in from `old`, `silent`, `collected_at`, and `behind_since`. No field changed meaning, so the version stays 4.
- `basis` and `pace` on a forecast, which may now follow the last day's readings, the `tight`, `outside`, `no_reading`, and `duplicate` kinds in `attention`, with `device_id` and `limitations` on an entry, `reading_age_seconds` on an `out` entry with an old reading, and the `stale` relay status, whose `at`, like `ok`'s, is now the older of the last push and pull. A reader that skips kinds and statuses it does not know reads the rest as before, so the version stays 4.
- Weighted tokens, in v0.4.0: `burn` beside every `usage`, with `burn_days`, `window_burn`, and `burn_share` beside `days`, `window_tokens`, and `share`; `fresh` and `transcript_gaps` on this device's accounts, `fresh` and `active_sessions` on projects, and `covered_since` on a provider. On the team, `estimated`, `held_back_by`, and `estimated_reason`; `burn_estimated` on devices, accounts, and rows; and on a device `last_error_at`, `gap`, `replaced_by`, and `limitations`, and on an account `no_reading` and `outside`. This device's own `burn` counts local periods, as the conventions at the top say. Lists are now ordered by weighted tokens. No field changed meaning, so the version stays 4. A report from an earlier release has no weighted tokens: `ai-usage report --from` gives each object whose `burn` is all zero and `usage` is not its `usage` as `burn`, and its `days`, `share`, and `window_tokens` as `burn_days`, `burn_share`, and `window_burn`, marks the team devices, team accounts, and matrix rows it filled `burn_estimated` and the team `estimated`, with `estimated_reason` saying the report was saved by an earlier release, gives each project its `sessions` as `active_sessions` in every period, and titles this device's PROJECTS and USAGE in tokens in+out.
- `folders` on a project. A project is now a git repository, with its subfolders and linked worktrees, where each working folder was one; `path` is the project's folder, and `folders` says how many working folders count under it. A report from an earlier release has a project for each working folder and no `folders`: read a missing one as `1`, as `ai-usage report --from` fills it in. Every field keeps its type, and a project's numbers still sum its sessions, so the version stays 4.

## Example

A shortened report from a team of two:

```json
{
  "schema_version": 4,
  "generated_at": "2026-09-01T12:04:00Z",
  "collector": {
    "version": "v1.3.0",
    "device": "d-3f9c2a51b7e04d18a6c90e12",
    "device_label": "ann-mbp",
    "os_user": "ann",
    "team": "472ghuwcctyuxbtc6zu2ht2mbmnz4pqe",
    "last_run_at": "2026-09-01T12:02:00Z",
    "last_success_at": "2026-09-01T12:02:00Z",
    "last_error": null,
    "last_error_at": null,
    "relay": {
      "url": "https://relay.example.com",
      "last_push_at": "2026-09-01T12:02:00Z",
      "last_pull_at": "2026-09-01T12:02:00Z",
      "pending": false,
      "last_error": null
    },
    "schedule": { "registered": true, "foreground": false, "error": null },
    "update": { "checked_at": "2026-09-01T09:13:00Z", "latest": "v1.3.0", "staged": null, "error": null },
    "health": [
      { "item": "collection", "status": "ok", "state": "ok", "at": "2026-09-01T12:02:00Z", "release": null },
      { "item": "relay", "status": "ok", "state": "ok", "at": "2026-09-01T12:02:00Z", "release": null },
      { "item": "update", "status": "ok", "state": "ok", "at": "2026-09-01T09:13:00Z", "release": null }
    ]
  },
  "attention": [
    {
      "kind": "over",
      "provider": "claude",
      "account": "ann@example.com",
      "name": "ann",
      "at": "2026-09-02T12:00:00Z",
      "resets_at": "2026-09-04T12:00:00Z",
      "percent": 140
    },
    { "kind": "old", "devices": ["bo-laptop"], "at": "2026-08-31T16:40:00Z", "message": "v1.3.0" }
  ],
  "providers": [
    {
      "provider": "claude",
      "status": "ok",
      "error": null,
      "homes": ["/Users/ann/.claude"],
      "covered_since": "2026-06-03T09:00:00Z",
      "accounts": [
        {
          "label": "ann@example.com",
          "name": "ann",
          "current": true,
          "home": "/Users/ann/.claude",
          "plan": "max",
          "state": "over",
          "quota": {
            "observed_at": "2026-09-01T12:00:00Z",
            "age_seconds": 240,
            "stale": false,
            "source": "cache",
            "windows": [
              {
                "name": "5h",
                "percent": 42,
                "resets_at": "2026-09-01T14:00:00Z",
                "minutes": 300,
                "main": false,
                "limits": false,
                "observed_at": "2026-09-01T12:00:00Z",
                "stale": false,
                "reset": false,
                "state": "ok",
                "forecast": { "percent": 70, "elapsed": 0.6, "runs_out_at": null }
              },
              {
                "name": "7d",
                "percent": 80,
                "resets_at": "2026-09-04T12:00:00Z",
                "minutes": 10080,
                "main": true,
                "limits": true,
                "observed_at": "2026-09-01T12:00:00Z",
                "stale": false,
                "reset": false,
                "state": "over",
                "forecast": { "percent": 140, "elapsed": 0.5714285714285714, "runs_out_at": "2026-09-02T12:00:00Z" }
              }
            ]
          },
          "link": null,
          "sessions": 12,
          "tokens": { "input": 21000000, "output": 6800000, "cache_read": 610000000, "cache_write": 29000000 },
          "usage": { "today": 2100000, "7d": 25000000, "30d": 27800000, "90d": 27800000 },
          "days": [2100000, 5400000, 3900000, 0, 4200000, 6100000, 3300000, 2800000],
          "burn": { "today": 11500000, "7d": 136900000, "30d": 152250000, "90d": 152250000 },
          "fresh": { "today": 4290000, "7d": 51080000, "30d": 56800000, "90d": 56800000 },
          "burn_days": [11500000, 29573000, 21359000, 0, 23002000, 33407000, 18073000, 15336000],
          "linked_usage": [],
          "last_active_at": "2026-09-01T11:40:00Z",
          "projects": [
            {
              "path": "/Users/ann/src/api",
              "folders": 2,
              "sessions": 3,
              "tokens": { "input": 14000000, "output": 4200000, "cache_read": 480000000, "cache_write": 21000000 },
              "usage": { "today": 1500000, "7d": 16500000, "30d": 18200000, "90d": 18200000 },
              "burn": { "today": 9000000, "7d": 99040000, "30d": 109250000, "90d": 109250000 },
              "fresh": { "today": 3230000, "7d": 35540000, "30d": 39200000, "90d": 39200000 },
              "active_sessions": { "today": 1, "7d": 3, "30d": 3, "90d": 3 },
              "last_active_at": "2026-09-01T11:40:00Z"
            }
          ]
        }
      ]
    },
    { "provider": "codex", "status": "ok", "error": null, "homes": ["/Users/ann/.codex"], "accounts": ["…"] },
    { "provider": "grok", "status": "skipped", "error": null, "homes": [], "accounts": [] },
    { "provider": "hermes", "status": "skipped", "error": null, "homes": [], "accounts": [] }
  ],
  "projects": [
    {
      "path": "/Users/ann/src/api",
      "folders": 3,
      "sessions": 5,
      "tokens": { "input": 17000000, "output": 5100000, "cache_read": 530000000, "cache_write": 21000000 },
      "usage": { "today": 1500000, "7d": 19800000, "30d": 22100000, "90d": 22100000 },
      "burn": { "today": 9000000, "7d": 111900000, "30d": 124450000, "90d": 124450000 },
      "fresh": { "today": 3230000, "7d": 38840000, "30d": 43100000, "90d": 43100000 },
      "active_sessions": { "today": 1, "7d": 5, "30d": 5, "90d": 5 },
      "providers": ["claude", "codex"],
      "last_active_at": "2026-09-01T11:40:00Z"
    }
  ],
  "team": {
    "pulled_at": "2026-09-01T12:02:00Z",
    "latest_version": "v1.3.0",
    "estimated": false,
    "held_back_by": [],
    "estimated_reason": null,
    "devices": [
      {
        "device": "d-8e41d07c5a2b93f6e1d4c7a0",
        "label": "bo-laptop",
        "os_user": "bo",
        "this_device": false,
        "collector_version": "v1.2.3",
        "collected_at": "2026-09-01T11:51:00Z",
        "age_seconds": 780,
        "last_success_at": "2026-09-01T11:51:00Z",
        "last_error": null,
        "last_error_at": null,
        "update_error": null,
        "sources": [
          { "provider": "claude", "status": "ok", "error": null },
          { "provider": "codex", "status": "ok", "error": null }
        ],
        "error": null,
        "silent": false,
        "old": true,
        "behind_since": "2026-08-31T16:40:00Z",
        "not_updating": true,
        "usage": { "today": 900000, "7d": 6100000, "30d": 21000000, "90d": 48000000 },
        "burn": { "today": 4280000, "7d": 29000000, "30d": 99860000, "90d": 228200000 },
        "burn_estimated": false,
        "gap": null,
        "replaced_by": null,
        "limitations": []
      }
    ],
    "providers": [
      {
        "provider": "claude",
        "accounts": [
          {
            "label": "bo@example.com",
            "name": "bo",
            "alias": null,
            "subscription": true,
            "current": false,
            "devices": ["bo-laptop (bo)"],
            "plan": "pro",
            "state": "ok",
            "quota": {
              "observed_at": "2026-09-01T11:51:00Z",
              "age_seconds": 780,
              "stale": false,
              "device": "bo-laptop (bo)",
              "windows": [
                {
                  "name": "7d",
                  "percent": 40,
                  "resets_at": "2026-09-04T12:00:00Z",
                  "minutes": 10080,
                  "main": true,
                  "limits": true,
                  "observed_at": "2026-09-01T11:51:00Z",
                  "stale": false,
                  "reset": false,
                  "state": "ok",
                  "forecast": { "percent": 70, "elapsed": 0.5705357142857143, "runs_out_at": null }
                }
              ]
            },
            "link": null,
            "sessions": 4,
            "tokens": { "input": 3800000, "output": 1100000, "cache_read": 90000000, "cache_write": 4000000 },
            "usage": { "today": 900000, "7d": 4200000, "30d": 4900000, "90d": 4900000 },
            "users": 1,
            "busiest": "bo-laptop",
            "last_active_at": "2026-09-01T11:50:00Z",
            "per_device": [
              {
                "device": "bo-laptop (bo)",
                "device_id": "d-8e41d07c5a2b93f6e1d4c7a0",
                "current": true,
                "sessions": 4,
                "tokens": { "input": 3800000, "output": 1100000, "cache_read": 90000000, "cache_write": 4000000 },
                "usage": { "today": 900000, "7d": 4200000, "30d": 4900000, "90d": 4900000 },
                "burn": { "today": 4280000, "7d": 19970000, "30d": 23300000, "90d": 23300000 },
                "last_active_at": "2026-09-01T11:50:00Z"
              }
            ],
            "linked_usage": [],
            "burn": { "today": 4280000, "7d": 19970000, "30d": 23300000, "90d": 23300000 },
            "burn_estimated": false,
            "no_reading": null,
            "outside": null
          }
        ]
      }
    ],
    "matrix": {
      "columns": [
        {
          "provider": "claude",
          "label": "ann@example.com",
          "name": "ann",
          "no_quota": false,
          "state": "over",
          "percent": 80,
          "usage": { "today": 2100000, "7d": 25000000, "30d": 27800000, "90d": 27800000 },
          "window_tokens": 13500000,
          "burn": { "today": 11500000, "7d": 136900000, "30d": 152250000, "90d": 152250000 },
          "window_burn": 73930000
        },
        {
          "provider": "claude",
          "label": "bo@example.com",
          "name": "bo",
          "no_quota": false,
          "state": "ok",
          "percent": 40,
          "usage": { "today": 900000, "7d": 4200000, "30d": 4900000, "90d": 4900000 },
          "window_tokens": 3600000,
          "burn": { "today": 4280000, "7d": 19970000, "30d": 23300000, "90d": 23300000 },
          "window_burn": 17120000
        }
      ],
      "rows": [
        {
          "device": "ann-mbp",
          "device_id": "d-3f9c2a51b7e04d18a6c90e12",
          "cells": [
            {
              "usage": { "today": 2100000, "7d": 25000000, "30d": 27800000, "90d": 27800000 },
              "window_tokens": 13500000,
              "share": { "today": 100, "7d": 100, "30d": 100, "90d": 100 },
              "burn": { "today": 11500000, "7d": 136900000, "30d": 152250000, "90d": 152250000 },
              "window_burn": 73930000,
              "burn_share": { "today": 100, "7d": 100, "30d": 100, "90d": 100 }
            },
            {
              "usage": { "today": 0, "7d": 0, "30d": 0, "90d": 0 },
              "window_tokens": 0,
              "share": { "today": 0, "7d": 0, "30d": 0, "90d": 0 },
              "burn": { "today": 0, "7d": 0, "30d": 0, "90d": 0 },
              "window_burn": 0,
              "burn_share": { "today": 0, "7d": 0, "30d": 0, "90d": 0 }
            }
          ],
          "usage": { "today": 2100000, "7d": 25000000, "30d": 27800000, "90d": 27800000 },
          "share": { "today": 70, "7d": 85.61643835616438, "30d": 85.01529051987767, "90d": 85.01529051987767 },
          "burn": { "today": 11500000, "7d": 136900000, "30d": 152250000, "90d": 152250000 },
          "burn_share": { "today": 72.87705956907478, "7d": 87.26971377414419, "30d": 86.72742808318143, "90d": 86.72742808318143 },
          "burn_estimated": false
        },
        {
          "device": "bo-laptop",
          "device_id": "d-8e41d07c5a2b93f6e1d4c7a0",
          "cells": [
            {
              "usage": { "today": 0, "7d": 0, "30d": 0, "90d": 0 },
              "window_tokens": 0,
              "share": { "today": 0, "7d": 0, "30d": 0, "90d": 0 },
              "burn": { "today": 0, "7d": 0, "30d": 0, "90d": 0 },
              "window_burn": 0,
              "burn_share": { "today": 0, "7d": 0, "30d": 0, "90d": 0 }
            },
            {
              "usage": { "today": 900000, "7d": 4200000, "30d": 4900000, "90d": 4900000 },
              "window_tokens": 3600000,
              "share": { "today": 100, "7d": 100, "30d": 100, "90d": 100 },
              "burn": { "today": 4280000, "7d": 19970000, "30d": 23300000, "90d": 23300000 },
              "window_burn": 17120000,
              "burn_share": { "today": 100, "7d": 100, "30d": 100, "90d": 100 }
            }
          ],
          "usage": { "today": 900000, "7d": 4200000, "30d": 4900000, "90d": 4900000 },
          "share": { "today": 30, "7d": 14.383561643835616, "30d": 14.984709480122325, "90d": 14.984709480122325 },
          "burn": { "today": 4280000, "7d": 19970000, "30d": 23300000, "90d": 23300000 },
          "burn_share": { "today": 27.122940430925222, "7d": 12.730286225855806, "30d": 13.272571916818571, "90d": 13.272571916818571 },
          "burn_estimated": false
        }
      ]
    }
  }
}
```

## status --json

`ai-usage status --json` reads the saved state without collecting:

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | number | `4` |
| `collector` | object | as in the report |
| `sources` | list | `{provider, status, error, homes}` for each tool, as in the report's `providers` without the accounts |
