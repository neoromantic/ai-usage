# JSON output, schema version 3

`ai-usage --json`, `ai-usage collect --json`, and `ai-usage report --json` print one report object. `ai-usage status --json` prints a smaller object, described at the end.

A field changes meaning only with a new `schema_version`. New fields can appear within a version, so ignore the ones you do not know. [Changes from version 2](#changes-from-version-2) lists what version 3 added and removed.

Conventions:

- Times are RFC 3339 strings. A time that never happened is `null`.
- An optional string is `null` when there is nothing to say. An error field is `null` when there was no error.
- Lists are `[]` when empty, never `null`.
- Percentages run from 0 to 100, except a forecast, which can pass 100.
- `tokens` has the four counts the tools record over the last 90 days. `usage`, `days`, and `window_tokens` count input plus output tokens only, cache left out.
- Days are UTC days. A `usage` object has four periods, each ending with the report's UTC day: `today`, `7d`, `30d`, and `90d`, the last that many UTC days up to and including it.
- A `state` is one of `out`, `over`, `tight`, `ok`, `under`, and `unknown`; see [states](#states).

## Report

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | number | `3` |
| `generated_at` | time | when the report was made |
| `collector` | object | this collector's health; see [collector](#collector) |
| `attention` | list | what needs attention now, most urgent first; see [attention](#attention) |
| `providers` | list | one entry each for `claude`, `codex`, `grok`, and `hermes`, in that order; see [providers](#providers) |
| `projects` | list | this device's projects over every account, most tokens in the last 7 days first; see [projects](#projects) |
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

### attention

Each entry is one thing that needs attention, from the team's view of each subscription and device. The report lists them most urgent first, by `kind` in the order of this table; within a kind, the earliest `at` first, the lowest forecast first for `under`, and by device for `error`. The fields that apply depend on the kind; the others are absent.

| `kind` | When | Fields |
| --- | --- | --- |
| `out` | a window of a subscription is at 100% | `provider`, `account`, `name`, `window`; `at` is when it resets |
| `over` | a window of a subscription will run out before it resets, at its pace so far | `provider`, `account`, `name`, `window`; `at` is when it runs out, `resets_at` when it resets, `percent` its forecast |
| `error` | a device's collector or one of its tools fails | `devices` names the device; `message` is the error |
| `silent` | a device has not reported for a day, and nothing fails on it | `devices` names the device; `at` is when it last reported |
| `old` | devices run an older release than the team's newest | `devices` lists every one of them; `message` is the newest release |
| `under` | past half of a subscription's main window, its forecast is under 50% | `provider`, `account`, `name`; `resets_at` is when it resets, `percent` its forecast |

A window is the account's main one or one that limits it more; see the account's `state`. An `out`, `over`, or `under` entry has `reading_age_seconds` when its window's reading is stale.

| Field | Type | Meaning |
| --- | --- | --- |
| `kind` | string | one of the kinds above |
| `provider` | string | the account's provider |
| `account` | string | the account's label |
| `name` | string | the account's short name |
| `window` | string | the window's name, when it is not the account's main one |
| `devices` | list of strings | the names of the devices it is about |
| `at` | time | as the table says for each kind |
| `resets_at` | time | when the window resets |
| `percent` | number | the window's forecast at its reset |
| `reading_age_seconds` | number | how old the reading is, when it is stale |
| `message` | string | an error's text, or the newest release |

### providers

| Field | Type | Meaning |
| --- | --- | --- |
| `provider` | string | `claude`, `codex`, `grok`, or `hermes` |
| `status` | string | `ok`; `partial` when something failed but some data was read; `error` when nothing could be read; `skipped` when the tool is not installed |
| `error` | string | what failed, joined with `; ` |
| `homes` | list of strings | the data folders read |
| `accounts` | list | every account seen on this device for this tool; see below |

An account is one login. After you switch accounts, the previous one stays, with the tokens it used and the last quota reported for it.

| Field | Type | Meaning |
| --- | --- | --- |
| `label` | string | what the tool calls the account, such as an email address or a user id; `unknown` when it did not say |
| `name` | string | the account's short name: the one the team gave it with `ai-usage alias`, else the part of an email before the `@`, the first 8 characters of an id (a UUID, or 16 or more letters, digits, `-`, and `_` with a digit among them), or else the whole label. When two accounts of one provider would go by the same name, in any case, both use their full label, even when the name is an alias; the team account's `alias` still holds it |
| `current` | bool | logged in right now. It turns `false` when the tool answers that nobody is logged in; a tool that does not answer leaves the last account current |
| `home` | string | the first of `homes` the account is logged in to now; absent when it is not logged in |
| `plan` | string | the plan the tool reports |
| `state` | string | the worst state of the windows that limit the account: its main window, and each other window that has not reset and is `out`, `over`, or fuller than the main one; `unknown` without a reading |
| `quota` | object | the last good quota reading, `null` if there never was one; see below |
| `link` | object | `{provider, label}`: the account of another tool this one is assumed to bill through, `null` when there is none. Only Hermes has one: its `openai-codex` account is linked to a Codex account and `xai-oauth` to a Grok one. Each Hermes folder is taken to bill through the account logged in to the folder `ai-usage home add --quota-from` named for it, else to `~/.codex` or `~/.grok`; when its folders bill through several, the link is the one most of the account's tokens in the last 90 days went through. Hermes keeps its own login, so the link is an assumption, not a reading |
| `sessions` | number | sessions in the last 90 days |
| `tokens` | object | tokens in the last 90 days; see below |
| `usage` | object | input plus output tokens on this device in each period |
| `days` | list of numbers | input plus output tokens on this device per UTC day, newest first: the first is the report's UTC day, the next the day before, and so on, for up to 90 days. Trailing zeros are left out |
| `linked_usage` | list | `{provider, sessions, tokens}` for each other tool assumed to bill through this account, such as Hermes on this Codex login; `[]` when none. These tokens are that tool's and are not in `tokens` |
| `last_active_at` | time | the newest session activity that used this account |
| `projects` | list | `{path, sessions, tokens, usage, last_active_at}` per working folder, this account's part only, most tokens first |

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
| `observed_at` | time | when this window was read. In the team view the windows of one account can come from different devices and readings |
| `stale` | bool | this window's reading is more than 6 hours old and the window was not full. A full window stays full until it resets, however old the reading |
| `reset` | bool | the window has reset since it was read, so how full it is now is not known; its `state` is `unknown` and `forecast` is `null` |
| `unread` | bool | a Claude 5-hour or weekly window the newest reading does not cover, because a request refused for a full window reads that window alone. Its `percent` is 0, `resets_at` and `forecast` are `null`, and its `state` is `unknown`. Absent when `false` |
| `state` | string | see [states](#states) |
| `forecast` | object | how full the window will be at its reset if it is used from now on at its average pace so far; see below. `null` when the length or the reset time is unknown, when it has reset, and in its first tenth unless it is over already |

`forecast`:

| Field | Type | Meaning |
| --- | --- | --- |
| `percent` | number | `percent` divided by `elapsed`, as a whole percent up to `999`. Over 100 means demand is larger than the quota: the window runs out before it resets |
| `elapsed` | number | the share of the window that had passed at the reading, from 0 to 1. The window began at `resets_at` minus its length, which is `minutes`, else the length its name starts with, such as `5h` or `7d` |
| `runs_out_at` | time | when the window reaches 100% at that pace, when `percent` is over 100 and the window is not full yet; `null` otherwise |

The pace is the average since the window began, nights and weekends included, so one busy hour does not raise an alarm, and it follows a change of pace slowly.

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

`projects` in the report lists this device's working folders, with every account's tokens in each. The same object lists one account's part of each folder under that account, without `providers`.

| Field | Type | Meaning |
| --- | --- | --- |
| `path` | string | the working folder |
| `sessions` | number | sessions in the last 90 days |
| `tokens` | object | tokens in the last 90 days |
| `usage` | object | input plus output tokens in each period |
| `providers` | list of strings | the tools that used the folder, most tokens first; only in the report's `projects` |
| `last_active_at` | time | the newest session activity in the folder |

The report's list is sorted by `usage` in `7d`, then `90d`, then by path.

### team

| Field | Type | Meaning |
| --- | --- | --- |
| `pulled_at` | time | when the team was read from the relay; `null` if it has not been, in which case the lists hold this device only |
| `latest_version` | string | the newest collector release any device in the team runs, or the newest this device's update check saw, whichever is newer |
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
| `sources` | list | `{provider, status, error}` for each tool on that device |
| `error` | string | what fails on the device now: a tool's error, named after its provider, else the last run's error when it is newer than the last success; `null` when nothing does |
| `silent` | bool | it has not reported for a day |
| `old` | bool | it runs an older release than `latest_version` |
| `usage` | object | its input plus output tokens in each period, over every account. A device's days count from the UTC day it collected on, so one that last reported three days ago adds nothing to `today` |

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
| `per_device` | list | `{device, device_id, current, sessions, tokens, usage, last_active_at}` for each device that has the account, `device` as `host (user)`; most tokens first, then by `device` |
| `linked_usage` | list | `{provider, label, devices, sessions, tokens}` for each account of another tool that spent through this one on any device, as each device counted it session by session, summed across devices, with the devices it ran on; `[]` when none. These tokens are that account's and are not in `tokens` |

### matrix

Who spends what: each device against each subscription, and the tokens that have no subscription. A device's Hermes account counts under each login it spent through, with the part of its tokens that went through that login in the last 90 days. Its snapshot does not split its days, or its tokens since a window began, by login, so each login gets that part of them, and so does what it counted through no login. One that spent through no login counts under the login it is linked to on that device, or under no quota when there is none.

| Field | Type | Meaning |
| --- | --- | --- |
| `columns` | list | the subscriptions, grouped by provider in the order of `team.providers` and their accounts, then one column per provider for the tokens with no quota; see below |
| `rows` | list | the devices, the most tokens in the last 7 days first, then by name; see below |

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

A row:

| Field | Type | Meaning |
| --- | --- | --- |
| `device` | string | the device's name, as its `label` |
| `device_id` | string | its id |
| `cells` | list | one per column, in the order of `columns` |
| `usage` | object | the device's tokens over every column, in each period |

A cell:

| Field | Type | Meaning |
| --- | --- | --- |
| `usage` | object | the device's input plus output tokens on the column in each period |
| `window_tokens` | number | the device's input plus output tokens since the column's main window began |
| `share` | number | an estimate of how much of the column's window the device used, in percent: its `window_tokens` over the column's, times the column's `percent`. A column's shares add up to its `percent`. `null` when the column has no `percent`, or the team spent nothing on it since the window began |

## Changes from version 2

Added:

- `attention` and `projects` in the report, and `team.latest_version` and `team.matrix`.
- On an account: `name`, `state`, `usage`, and `days`. On a project: `usage`, `last_active_at`, and in the report's list `providers`.
- On a window: `main`, `observed_at`, `stale`, `reset`, `unread`, `state`, and `forecast`.
- On a team device: `error`, `silent`, `old`, and `usage`.
- On a team account: `name`, `alias`, `subscription`, `current`, `state`, `usage`, `users`, `busiest`, and `last_active_at`; on its `per_device` entries, `usage`.

Removed:

- `headline_percent` and `level` on accounts and team accounts, and `level` and `pace` on windows. The levels `ok`, `warning`, and `critical`, at 75% and 90%, and the pace over the last 6 hours are gone. A window's `state` and `forecast` take their place: how full the window will be at its reset, from its average pace since it began.

Changed:

- `quota.stale` says a window's reading is stale, and a full window's reading no longer goes stale.
- A team account's `quota` takes the newest reading of each window, so `quota.device` is the device whose reading is newest.

## Example

A shortened report from a team of two:

```json
{
  "schema_version": 3,
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
    "update": { "checked_at": "2026-09-01T09:13:00Z", "latest": "v1.3.0", "staged": null, "error": null }
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
    { "kind": "old", "devices": ["bo-laptop"], "message": "v1.3.0" }
  ],
  "providers": [
    {
      "provider": "claude",
      "status": "ok",
      "error": null,
      "homes": ["/Users/ann/.claude"],
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
          "linked_usage": [],
          "last_active_at": "2026-09-01T11:40:00Z",
          "projects": [
            {
              "path": "/Users/ann/src/api",
              "sessions": 3,
              "tokens": { "input": 14000000, "output": 4200000, "cache_read": 480000000, "cache_write": 21000000 },
              "usage": { "today": 1500000, "7d": 16500000, "30d": 18200000, "90d": 18200000 },
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
      "sessions": 5,
      "tokens": { "input": 17000000, "output": 5100000, "cache_read": 530000000, "cache_write": 21000000 },
      "usage": { "today": 1500000, "7d": 19800000, "30d": 22100000, "90d": 22100000 },
      "providers": ["claude", "codex"],
      "last_active_at": "2026-09-01T11:40:00Z"
    }
  ],
  "team": {
    "pulled_at": "2026-09-01T12:02:00Z",
    "latest_version": "v1.3.0",
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
        "sources": [
          { "provider": "claude", "status": "ok", "error": null },
          { "provider": "codex", "status": "ok", "error": null }
        ],
        "error": null,
        "silent": false,
        "old": true,
        "usage": { "today": 900000, "7d": 6100000, "30d": 21000000, "90d": 48000000 }
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
                "last_active_at": "2026-09-01T11:50:00Z"
              }
            ],
            "linked_usage": []
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
          "window_tokens": 13500000
        },
        {
          "provider": "claude",
          "label": "bo@example.com",
          "name": "bo",
          "no_quota": false,
          "state": "ok",
          "percent": 40,
          "usage": { "today": 900000, "7d": 4200000, "30d": 4900000, "90d": 4900000 },
          "window_tokens": 3600000
        }
      ],
      "rows": [
        {
          "device": "ann-mbp",
          "device_id": "d-3f9c2a51b7e04d18a6c90e12",
          "cells": [
            { "usage": { "today": 2100000, "7d": 25000000, "30d": 27800000, "90d": 27800000 }, "window_tokens": 13500000, "share": 80 },
            { "usage": { "today": 0, "7d": 0, "30d": 0, "90d": 0 }, "window_tokens": 0, "share": 0 }
          ],
          "usage": { "today": 2100000, "7d": 25000000, "30d": 27800000, "90d": 27800000 }
        },
        {
          "device": "bo-laptop",
          "device_id": "d-8e41d07c5a2b93f6e1d4c7a0",
          "cells": [
            { "usage": { "today": 0, "7d": 0, "30d": 0, "90d": 0 }, "window_tokens": 0, "share": 0 },
            { "usage": { "today": 900000, "7d": 4200000, "30d": 4900000, "90d": 4900000 }, "window_tokens": 3600000, "share": 40 }
          ],
          "usage": { "today": 900000, "7d": 4200000, "30d": 4900000, "90d": 4900000 }
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
| `schema_version` | number | `3` |
| `collector` | object | as in the report |
| `sources` | list | `{provider, status, error, homes}` for each tool, as in the report's `providers` without the accounts |
