# JSON output, schema version 2

`ai-usage --json`, `ai-usage collect --json`, and `ai-usage report --json` print one report object. `ai-usage status --json` prints a smaller object, described at the end.

A field changes meaning only with a new `schema_version`.

Conventions:

- Times are RFC 3339 strings. A time that never happened is `null`.
- An optional string is `null` when there is nothing to say. An error field is `null` when there was no error.
- Lists are `[]` when empty, never `null`.
- Percentages run from 0 to 100.
- `level` is `ok` below 75%, `warning` from 75%, `critical` from 90%, and `unknown` when there is no reading. A window whose `resets_at` has passed since the reading has no reading for its new period, so its `level` is `unknown`.

## Report

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | number | `2` |
| `generated_at` | time | when the report was made |
| `collector` | object | this collector's health; see [collector](#collector) |
| `providers` | list | one entry each for `claude`, `codex`, `grok`, and `hermes`, in that order; see [providers](#providers) |
| `team` | object | every device in the team, this one included; see [team](#team) |

### collector

| Field | Type | Meaning |
| --- | --- | --- |
| `version` | string | the collector version, such as `v1.2.3`, or `dev` for a build from source |
| `device` | string | this device's id, `d-` and 24 hex digits, made on the first run |
| `device_label` | string | the host name |
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
| `schedule.registered` | bool | the system scheduler runs this binary |
| `schedule.error` | string | why it is not registered |
| `update.checked_at` | time | the last release check |
| `update.latest` | string | the newest release that check saw |
| `update.staged` | string | a release already installed; the next run uses it |
| `update.error` | string | the last update error |

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
| `current` | bool | logged in right now. It turns `false` when the tool answers that nobody is logged in; a tool that does not answer leaves the last account current |
| `plan` | string | the plan the tool reports |
| `headline_percent` | number | the fullest window's percentage, leaving out windows that have reset since the reading; `null` without a reading, or when every window has reset |
| `level` | string | the level of `headline_percent` |
| `quota` | object | the last good quota reading, `null` if there never was one; see below |
| `sessions` | number | sessions in the last 90 days |
| `tokens` | object | tokens in the last 90 days; see below |
| `last_active_at` | time | the newest session activity that used this account |
| `projects` | list | `{path, sessions, tokens}` per working folder, most tokens first |

`tokens` has four counts: `input`, `output`, `cache_read`, and `cache_write`. `input` does not include cache reads.

`quota`:

| Field | Type | Meaning |
| --- | --- | --- |
| `observed_at` | time | when the tool took the reading |
| `age_seconds` | number | the reading's age when the report was made |
| `stale` | bool | the reading is more than 6 hours old |
| `source` | string | `harness` when the tool answered a command, `cache` when it came from the tool's own cache file, `log` when it came from the tool's logs; only on this device's accounts |
| `device` | string | the device that took the reading, as `host (user)`; only on team accounts |
| `windows` | list | the windows the tool reported; see below |

A window:

| Field | Type | Meaning |
| --- | --- | --- |
| `name` | string | the tool's name for it, such as `5h` or `7d` |
| `percent` | number | how full it was at the reading; kept after `resets_at` passes, as the last value seen |
| `level` | string | the level of `percent`; `unknown` once `resets_at` has passed |
| `resets_at` | time | when it resets, if the tool said |
| `minutes` | number | its length in minutes; absent when the tool did not say |
| `pace` | object | how fast it is filling; `null` without enough readings, or once `resets_at` has passed |
| `pace.percent_per_hour` | number | the rate, fitted to the readings of the last 6 hours within the current window, at least 10 minutes apart |
| `pace.fills_at` | time | when it reaches 100% at that rate; `null` unless that happens before it resets |

### team

| Field | Type | Meaning |
| --- | --- | --- |
| `pulled_at` | time | when the team was read from the relay; `null` if it has not been, in which case the lists hold this device only |
| `devices` | list | one entry per device; see below |
| `providers` | list | `{provider, accounts}` with each account summed across devices; see below |

A device:

| Field | Type | Meaning |
| --- | --- | --- |
| `device` | string | its id |
| `label` | string | its host name, or `(unreadable)` if it does not decrypt with this team's key |
| `os_user` | string | its OS user, or `(unreadable)` |
| `this_device` | bool | this is the device making the report |
| `collector_version` | string | its collector version |
| `collected_at` | time | when it made its snapshot |
| `age_seconds` | number | the snapshot's age |
| `last_success_at` | time | its last run without an error |
| `last_error` | string | its last error |
| `sources` | list | `{provider, status, error}` for each tool on that device |

A team account:

| Field | Type | Meaning |
| --- | --- | --- |
| `label` | string | the account label |
| `devices` | list of strings | the devices that saw it, as `host (user)` |
| `headline_percent`, `level`, `quota` | | as for a device's account, from the newest reading any device has; percentages are never added |
| `sessions`, `tokens` | | summed across devices |

## Example

A shortened report from a team of two:

```json
{
  "schema_version": 2,
  "generated_at": "2026-09-01T12:00:00Z",
  "collector": {
    "version": "v1.2.3",
    "device": "d-3f9c2a51b7e04d18a6c90e12",
    "device_label": "ann-mbp",
    "os_user": "ann",
    "team": "472ghuwcctyuxbtc6zu2ht2mbmnz4pqe",
    "last_run_at": "2026-09-01T11:58:00Z",
    "last_success_at": "2026-09-01T11:58:00Z",
    "last_error": null,
    "last_error_at": null,
    "relay": {
      "url": "https://relay.example.com",
      "last_push_at": "2026-09-01T11:58:00Z",
      "last_pull_at": "2026-09-01T11:58:00Z",
      "pending": false,
      "last_error": null
    },
    "schedule": { "registered": true, "error": null },
    "update": { "checked_at": "2026-09-01T09:13:00Z", "latest": "v1.2.3", "staged": null, "error": null }
  },
  "providers": [
    {
      "provider": "claude",
      "status": "ok",
      "error": null,
      "homes": ["/Users/ann/.claude"],
      "accounts": [
        {
          "label": "ann@example.com",
          "current": true,
          "plan": "max",
          "headline_percent": 78,
          "level": "warning",
          "quota": {
            "observed_at": "2026-09-01T11:56:00Z",
            "age_seconds": 240,
            "stale": false,
            "source": "cache",
            "windows": [
              {
                "name": "5h",
                "percent": 42,
                "level": "ok",
                "resets_at": "2026-09-01T14:10:00Z",
                "minutes": 300,
                "pace": { "percent_per_hour": 12, "fills_at": null }
              },
              {
                "name": "7d",
                "percent": 78,
                "level": "warning",
                "resets_at": "2026-09-04T16:00:00Z",
                "minutes": 10080,
                "pace": { "percent_per_hour": 2, "fills_at": "2026-09-01T22:56:00Z" }
              }
            ]
          },
          "sessions": 2,
          "tokens": { "input": 1600000, "output": 405000, "cache_read": 60000000, "cache_write": 2900000 },
          "last_active_at": "2026-09-01T11:40:00Z",
          "projects": [
            {
              "path": "/Users/ann/src/api",
              "sessions": 1,
              "tokens": { "input": 1200000, "output": 310000, "cache_read": 48000000, "cache_write": 2100000 }
            }
          ]
        }
      ]
    },
    { "provider": "codex", "status": "ok", "error": null, "homes": ["/Users/ann/.codex"], "accounts": ["…"] },
    { "provider": "grok", "status": "skipped", "error": null, "homes": [], "accounts": [] },
    { "provider": "hermes", "status": "skipped", "error": null, "homes": [], "accounts": [] }
  ],
  "team": {
    "pulled_at": "2026-09-01T11:58:00Z",
    "devices": [
      {
        "device": "d-8e41d07c5a2b93f6e1d4c7a0",
        "label": "bo-laptop",
        "os_user": "bo",
        "this_device": false,
        "collector_version": "v1.2.3",
        "collected_at": "2026-09-01T11:51:00Z",
        "age_seconds": 540,
        "last_success_at": "2026-09-01T11:51:00Z",
        "last_error": null,
        "sources": [
          { "provider": "claude", "status": "ok", "error": null },
          { "provider": "codex", "status": "ok", "error": null }
        ]
      }
    ],
    "providers": [
      {
        "provider": "claude",
        "accounts": [
          {
            "label": "bo@example.com",
            "devices": ["bo-laptop (bo)"],
            "headline_percent": 91,
            "level": "critical",
            "quota": {
              "observed_at": "2026-09-01T11:51:00Z",
              "age_seconds": 540,
              "stale": false,
              "device": "bo-laptop (bo)",
              "windows": [
                { "name": "5h", "percent": 91, "level": "critical", "resets_at": "2026-09-01T12:40:00Z", "minutes": 300, "pace": null }
              ]
            },
            "sessions": 1,
            "tokens": { "input": 800000, "output": 150000, "cache_read": 20000000, "cache_write": 900000 }
          }
        ]
      }
    ]
  }
}
```

## status --json

`ai-usage status --json` reads the saved state without collecting:

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | number | `2` |
| `collector` | object | as in the report |
| `sources` | list | `{provider, status, error, homes}` for each tool, as in the report's `providers` without the accounts |
