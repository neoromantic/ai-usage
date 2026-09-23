# Relay protocol, version 1

This document specifies how ai-usage collectors publish usage snapshots to a relay and read their team back. It is meant to be enough to write a relay or a client in another language. The Go code in this repository is the reference implementation: `internal/team` holds keys, sealing, and signatures; `internal/snapshot` the document; `relay/` the HTTP API, the store, and the client. How to run the reference relay is in [relay.md](relay.md).

The key words MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY are to be read as described in RFC 2119 and RFC 8174 when they appear in capitals.

Contents:

1. [Roles](#1-roles)
2. [Encodings](#2-encodings)
3. [Team key](#3-team-key)
4. [Sealed strings](#4-sealed-strings)
5. [Signatures](#5-signatures)
6. [HTTP API](#6-http-api)
7. [Snapshot document](#7-snapshot-document)
8. [Storage](#8-storage)
9. [Rate limits](#9-rate-limits)
10. [Readers](#10-readers)
11. [Compatibility](#11-compatibility)
12. [Security notes](#12-security-notes)
13. [Test vectors](#13-test-vectors)

## 1. Roles

| Role | What it does |
| --- | --- |
| collector | builds a snapshot of its device, seals the private strings in it, signs it, and sends it with `PUT` |
| relay | checks each snapshot and keeps the latest one per device; returns all of a team's snapshots to a signed read |
| reader | reads the team with a signed `GET`, verifies every snapshot, and opens the sealed strings |

The reference `ai-usage` binary is both a collector and a reader: each run publishes its own snapshot, then reads the team. A scheduled run reads it only when its last read is an hour old.

Every member of a team holds the same private key. The relay never holds it. The relay sees the public key, the signatures, the plain fields of each snapshot, and the IP addresses that connect. It cannot read sealed strings, and it cannot forge or change a snapshot without readers noticing. It can withhold or delete a snapshot, or keep serving an older one.

A device is one collector with its own state: one device id, one snapshot.

## 2. Encodings

| Name | Definition | Used for |
| --- | --- | --- |
| base64url | RFC 4648 §5 alphabet (`A–Z a–z 0–9 - _`), without `=` padding | keys, signatures, sealed strings, bodies in the team read |
| base32 | RFC 4648 §6 alphabet, without padding, in lowercase | the team fingerprint |
| Unix time | seconds since 1970-01-01T00:00:00Z, as a decimal integer | `X-Aiu-Time` |

Encoders MUST NOT add padding. The reference decoders reject padded input.

## 3. Team key

A team is one Ed25519 key pair (RFC 8032).

### 3.1 Private key

The private key is a 32-byte seed read from a cryptographically secure random source. Everything else derives from it: the Ed25519 key pair by RFC 8032 key generation (the 32-byte private key of RFC 8032 §5.1.5 is the seed), and the seal key (§4).

Whoever holds the seed can read the team's snapshots and publish into the team. It cannot be revoked; to shut someone out, a team makes a new key and moves to it.

### 3.2 Exported form

The seed is exchanged as one line:

```
aiu-team-1:<base64url(seed)>
```

That is `aiu-team-1:` followed by 43 characters. To import a key, an implementation:

1. removes leading and trailing characters that are Unicode white space (Go's `unicode.IsSpace`), U+FEFF (byte order mark), or U+200B (zero width space);
2. requires the result to start with `aiu-team-1:`;
3. decodes the rest as base64url and requires exactly 32 bytes.

Implementations SHOULD trim the same characters. Windows PowerShell puts a byte order mark in front of text it pipes to a program.

### 3.3 Public key and fingerprint

The public key is the 32-byte Ed25519 public key. It travels in `X-Aiu-Key` as base64url, 43 characters.

The fingerprint names the team in URLs and in every snapshot. It is not secret:

```
fingerprint = lowercase(base32(SHA-256(public key)[0:20]))
```

It is 32 characters and matches `^[a-z2-7]{32}$`.

## 4. Sealed strings

Strings that name a person, a machine, or a path are sealed with a key only the team has.

```
seal_key = HKDF-SHA256(IKM = seed, salt = none, info = "ai-usage seal v1", L = 32)
```

HKDF is RFC 5869. With no salt, HKDF-Extract uses 32 zero bytes. The info string is ASCII, without a terminator.

To seal a non-empty plaintext `P`, its UTF-8 bytes:

1. Take a fresh 12-byte nonce from a secure random source. A nonce MUST NOT be reused with the same key.
2. Encrypt with AES-256-GCM: key `seal_key`, that nonce, plaintext `P`, and as additional authenticated data the 32 ASCII bytes of the team fingerprint. The tag is 16 bytes.
3. The sealed string is `base64url(nonce || ciphertext || tag)`.

The empty string seals to the empty string. Nothing is encrypted, and an optional sealed field is then left out (§7.1).

A sealed string for an `n`-byte plaintext is `ceil(4 × (28 + n) / 3)` characters long. A snapshot allows at most 512 characters, so a plaintext MUST be at most 356 bytes. The reference collector shortens longer text to 300 bytes, keeping the end and putting `…` in front.

To open a sealed string, decode it, take the first 12 bytes as the nonce, and decrypt the rest with the same additional data. A reader that cannot open a string SHOULD show a placeholder rather than drop the document; the reference reader shows `(unreadable)`.

The relay cannot tell a sealed string from any other base64url text. It checks only the characters and the length.

## 5. Signatures

Signatures are Ed25519 (RFC 8032, pure, not prehashed), 64 bytes, sent in `X-Aiu-Signature` as base64url, 86 characters. Each request signs one of these messages:

| Request | Message |
| --- | --- |
| `PUT` a snapshot | `ai-usage snapshot v1` `\n` then the request body, byte for byte |
| `GET` the team | `ai-usage request v1` `\n` `GET` `\n` *team* `\n` `\n` *time* |
| `DELETE` a device | `ai-usage request v1` `\n` `DELETE` `\n` *team* `\n` *device* `\n` *time* |

`\n` is the single byte 0x0A. *team* is the fingerprint, *device* the device id, and *time* the value of `X-Aiu-Time` in decimal without sign or leading zeros. The device part of a team read is empty, so its message has two newlines in a row. The messages name the method, team, and device rather than the URL, so a proxy that rewrites the path does not break a signature.

The reference relay parses `X-Aiu-Time` as a signed decimal integer and builds the message from the parsed number. Clients MUST send the plain form, such as `1760000000`.

A relay MUST reject a signed `GET` or `DELETE` whose time is more than 300 seconds before or after its own clock. Exactly 300 seconds is accepted. A `PUT` carries no time; the snapshot inside it has `collected_at` (§7.3), and replays are handled in §8.2.

## 6. HTTP API

### 6.1 Base URL

A relay is named by a base URL: `http` or `https`, a host, and an optional path, with no query, no fragment, and no trailing slash. Clients append `/v1/…` to it. With the base `https://relay.example.com/aiu`, the health check is `https://relay.example.com/aiu/v1/health`.

Clients SHOULD use `https`. Over plain HTTP, snapshots are still sealed and signed, but anyone on the path sees the team fingerprint, the device ids, and the plain fields.

### 6.2 Common rules

- Every response SHOULD carry `Cache-Control: no-store`. The reference relay sends it on every response.
- A JSON response has `Content-Type: application/json`. Its body is one JSON object; the reference relay ends it with a newline.
- An error is `{"error": "<text>"}`. The text is for people. Clients MUST act on the status code, not the text.
- `{team}` MUST match the fingerprint shape and `{device}` the device id shape (§7.3). A relay answers `404` to any other value.
- A path the relay does not serve is `404`, and a served path with another method is `405`. The reference relay answers these in plain text, not JSON. It serves `HEAD` wherever it serves `GET`; a signed `HEAD` is signed as the `GET` it stands for, with `GET` in the message.

### 6.3 Headers

| Header | Sent with | Value |
| --- | --- | --- |
| `X-Aiu-Key` | `PUT`, `GET` team, `DELETE` | the team's public key in base64url; its fingerprint MUST equal `{team}` |
| `X-Aiu-Signature` | `PUT`, `GET` team, `DELETE` | the signature of the message in §5, in base64url |
| `X-Aiu-Time` | `GET` team, `DELETE` | the Unix time in seconds that the signature covers |
| `Content-Type` | `PUT` | `application/json`; the relay ignores it |

### 6.4 `GET /v1/health`

Unsigned. Answers `200`:

```json
{"ok":true,"snapshot_version":1}
```

`snapshot_version` is the document version the relay accepts.

### 6.5 `PUT /v1/teams/{team}/devices/{device}`

Stores the device's snapshot. The body is a snapshot document (§7) of at most 32768 bytes, signed as in §5. The reference relay checks in this order and answers the first failure:

1. the per-IP request limit (`429`), counted for every request the relay routes;
2. the shapes of `{team}` and `{device}` (`404`);
3. `X-Aiu-Key`: present, 32 bytes, and its fingerprint equal to `{team}` (`403`);
4. the body: read within 30 seconds (`400`) and at most 32768 bytes (`413`);
5. the signature over `ai-usage snapshot v1\n` and the body (`401`);
6. the document: strict decoding, every rule of §7, and `collected_at` at most 10 minutes ahead of the relay's clock (`422`);
7. `team` and `device` in the document equal to `{team}` and `{device}` (`422`);
8. the team's write limit (`429`);
9. the rules for replacing the stored snapshot (§8.2): the device cap (`403`), `409`, and for a new device the new-team and new-device limits (`429`).

On success it answers `200`:

```json
{"stored":true}
```

It gives the same answer when the body equals the stored one byte for byte, without writing anything.

### 6.6 `GET /v1/teams/{team}`

Reads every live snapshot of the team. Signed as in §5, with an empty device. Answers `200`:

```json
{"devices":[{"device":"d-000102030405060708090a0b","body":"eyJ2Ijox…","sig":"JeCd6_i3…"}]}
```

| Member | Value |
| --- | --- |
| `devices` | one entry per live record, in no particular order; `[]` when the team has none |
| `device` | the device id the record is stored under |
| `body` | the stored snapshot, byte for byte as it was sent, in base64url |
| `sig` | the signature it was sent with, in base64url |

Every entry is 4/3 the size of its snapshot plus about 200 bytes, so a full team of 100 devices with 32 KB snapshots is about 4.4 MB. Clients MUST accept a response of at least 5 MB; the reference client reads up to 8 MiB.

### 6.7 `DELETE /v1/teams/{team}/devices/{device}`

Removes one device's snapshot. Signed as in §5. Answers `200` whether or not a snapshot was stored:

```json
{"deleted":true}
```

### 6.8 Status codes

| Status | Requests | Meaning |
| --- | --- | --- |
| `200` | all | success |
| `400` | `PUT` | the body could not be read; the reference relay allows 30 seconds for it |
| `401` | `PUT`, `GET` team, `DELETE` | the signature does not verify, or `X-Aiu-Time` is missing, is not a number, or is more than 300 seconds from the relay's clock |
| `403` | `PUT`, `GET` team, `DELETE` | `X-Aiu-Key` is missing, is malformed, or does not match `{team}`; or the team already has the most devices allowed and this device is not among them |
| `404` | all | `{team}` or `{device}` is not a valid id, or the relay does not serve the path |
| `405` | all | the relay serves the path, but not with this method |
| `409` | `PUT` | the stored snapshot for this device has a later `collected_at` |
| `413` | `PUT` | the body is larger than 32768 bytes |
| `422` | `PUT` | the body is not a valid snapshot in canonical form, or it names another team or device |
| `429` | all | a rate limit; `Retry-After` gives the seconds until the window ends |
| `503` | all | the store is unavailable; the reference Vercel function also answers `503` with `{"error":"relay store not configured"}` to every request when it has no store |

A `409` means the relay holds a newer snapshot for this device id: the device's clock went back, or another machine uses the same id. The reference collector reports it and still reads the team. After any other error it keeps its newest snapshot pending, sends it on a later run, and skips the team read for that run. A snapshot holds running totals, so the newest one also covers the runs that could not reach the relay.

## 7. Snapshot document

A snapshot is a JSON object that describes one device. Numbers, provider names, plan names, and window names are plain, so the relay can check the shape. Names of people, machines, and paths are sealed.

### 7.1 Canonical form

The body MUST be in exactly the form Go's `encoding/json` writes the reference types. The reference relay decodes the body strictly, validates it, encodes the result again, and answers `422` unless the two are equal byte for byte. The form is:

1. No white space outside strings, no trailing newline, no byte order mark.
2. Members in exactly the order of the tables in §7.3 and §7.4, each at most once, with names in the exact case shown.
3. A member marked *omitted when empty* is left out when it has its empty value: `""`, `0`, no time, or no entries. It is never written as `""`, `0`, `null`, or `[]`.
4. Every other member is always present. Required arrays are `[]` when empty, never `null`.
5. Strings contain no escapes. Every character a field allows is printable ASCII other than `"` and `\`, so none is needed.
6. Integers in plain decimal: no `+`, no leading zeros, no fraction, no exponent.
7. `percent`, the only non-integer, is the shortest decimal that reads back as the same IEEE 754 double, formatted as ECMAScript's `Number::toString` does: `41`, `78.5`, `33.333333333333336`, and the exponent form only below 0.000001, such as `1e-7`. JavaScript's `JSON.stringify` writes numbers this way.
8. Times in RFC 3339 as Go's `time.RFC3339Nano` layout writes them: seconds always; a fraction only when it is not zero, with trailing zeros removed, up to 9 digits; `Z` for a zero offset, `+hh:mm` or `-hh:mm` otherwise. `2025-10-09T08:50:00Z` and `2025-10-09T08:50:00.5Z` are canonical; `2025-10-09T08:50:00.500Z` and `2025-10-09T08:50:00+00:00` are not.

Collectors SHOULD write times in UTC with `Z`. The reference relay also accepts another offset in the form above. JavaScript's `Date.prototype.toISOString` always writes three fraction digits, which is canonical only when the milliseconds do not end in 0.

The rule exists because the relay stores the signed bytes, not what it parsed. Go's decoder matches member names without regard to case and lets a repeated member overwrite an earlier one, so any other body could carry text the validator never saw.

A relay implementation MAY skip the canonical-form check. It MUST still verify the signature, check that the document names the team and device of the path, enforce the 32768-byte limit, reject members it does not know, and check every rule in §7.2 to §7.4, so that nothing but snapshots reaches its store.

### 7.2 Value types

| Type | JSON | Rule |
| --- | --- | --- |
| sealed | string | a sealed string (§4): `^[A-Za-z0-9_-]+$`, at most 512 characters |
| plain | string | `^[A-Za-z0-9 ._:+()/-]*$`, at most 40 bytes |
| provider | string | one of `claude`, `codex`, `grok`, `hermes` |
| time | string | RFC 3339, in the form of §7.1 rule 8 |
| session count | integer | 0 to 16777216 (2^24) |
| token count | integer | 0 to 1125899906842624 (2^50) |

Every integer the document allows fits exactly in an IEEE 754 double, so readers in JavaScript lose nothing.

### 7.3 Document

| Member | Type | Presence | Rule and meaning |
| --- | --- | --- | --- |
| `v` | integer | required | `1`, the document version |
| `team` | string | required | the team fingerprint, `^[a-z2-7]{32}$`; MUST equal `{team}` |
| `device` | string | required | the device id, `^[a-z0-9][a-z0-9-]{7,63}$`; MUST equal `{device}`. The reference collector uses `d-` and 24 random hex digits, fixed for its state folder |
| `device_label` | sealed | required | the name the device goes by in the team: one set with `ai-usage name set` or `AI_USAGE_NAME`, else the host name |
| `os_user` | sealed | required | the OS user the collector runs as |
| `collector_version` | plain | required, not empty | the collector's version, such as `v1.4.2` or `dev` |
| `collected_at` | time | required | when the snapshot was built. MUST NOT be `0001-01-01T00:00:00Z`; the relay rejects a time more than 10 minutes ahead of its clock |
| `last_success_at` | time | required | the last collection that succeeded; `0001-01-01T00:00:00Z` when none has |
| `last_error` | sealed | omitted when empty | the error of the last collection that failed |
| `accounts` | array of Account | required | at most 24 |
| `sources` | array of Source | required | at most 12 |

The body as a whole MUST be at most 32768 bytes. The reference collector drops projects, then accounts until it fits.

### 7.4 Nested objects

**Account**: one login on one provider, as seen from this device and OS user.

| Member | Type | Presence | Rule and meaning |
| --- | --- | --- | --- |
| `provider` | provider | required | |
| `label` | sealed | required | the account's name, such as an email address |
| `current` | boolean | required | whether it is logged in on this device now |
| `plan` | plain | omitted when empty | the plan name |
| `quota_at` | time | omitted when empty | when the windows were read; required when `windows` is not empty |
| `quota_from` | provider | omitted when empty | the provider whose account the windows belong to, when this account is assumed to bill through it (Hermes on a Codex login). MUST differ from `provider`, and MUST come only with windows |
| `windows` | array of Window | required | at most 8 |
| `sessions` | session count | required | sessions in the collector's ledger, which keeps 90 days in the reference collector |
| `tokens` | Tokens | required | tokens of those sessions |
| `last_active_at` | time | omitted when empty | the account's last activity on this device |
| `projects` | array of Project | required | at most 12, the largest first |
| `linked` | array of Linked | omitted when empty | at most 4 |

**Window**: one quota window as the tool reported it.

| Member | Type | Presence | Rule and meaning |
| --- | --- | --- | --- |
| `name` | plain | required, not empty | such as `5h`, `7d`, or `opus 7d` |
| `percent` | number | required | percent used, 0 to 1000; it may pass 100 |
| `resets_at` | time | omitted when empty | when the window resets |
| `minutes` | integer | omitted when 0 | the window's length, at most 89280 (62 days) |

**Project**: usage in one working directory.

| Member | Type | Presence | Rule and meaning |
| --- | --- | --- | --- |
| `path` | sealed | required | the directory |
| `sessions` | session count | required | |
| `tokens` | Tokens | required | |

**Linked**: what an account of another provider on this device spent through this account, and is assumed to have billed to it, such as Hermes on this Codex login. It is counted in the other account's tokens, not in this account's.

| Member | Type | Presence | Rule and meaning |
| --- | --- | --- | --- |
| `provider` | provider | required | MUST differ from the account's `provider` |
| `label` | sealed | required | the other account's name |
| `sessions` | session count | required | |
| `tokens` | Tokens | required | |

**Source**: the health of one tool's reader on this device.

| Member | Type | Presence | Rule and meaning |
| --- | --- | --- | --- |
| `provider` | provider | required | |
| `status` | string | required | one of `ok`, `partial`, `error`, `skipped` |
| `error` | sealed | omitted when empty | the reader's error |

**Tokens**: counts a tool's logs already recorded. All four members are always present.

| Member | Type | Meaning |
| --- | --- | --- |
| `input` | token count | input tokens, not counting cache reads |
| `output` | token count | output tokens |
| `cache_read` | token count | tokens read from the cache |
| `cache_write` | token count | tokens written to the cache |

The relay does not check that providers or accounts are unique, or that the counts add up.

### 7.5 Example

A snapshot signed with the test key of §13. Its sealed strings use the fixed nonces 12 × `01`, 12 × `02`, 12 × `03`, and 12 × `04`, in document order, so that the example can be reproduced. A real collector MUST use random nonces. Indented for reading:

```json
{
  "v": 1,
  "team": "kzdvvj2umnduyauf35o36k6kw462mujv",
  "device": "d-000102030405060708090a0b",
  "device_label": "AQEBAQEBAQEBAQEBP5kbN5fasitxl9v4Xe9ioihsTqSGEvQ",
  "os_user": "AgICAgICAgICAgICd91O1uRndQ00ItS5z7aTyprSFA",
  "collector_version": "v1.4.2",
  "collected_at": "2025-10-09T08:50:00Z",
  "last_success_at": "2025-10-09T08:50:00Z",
  "accounts": [
    {
      "provider": "codex",
      "label": "AwMDAwMDAwMDAwMDSWbUxyIYwLxNem8GfO1ERleExJJSLs74N5wYzIE81w",
      "current": true,
      "plan": "plus",
      "quota_at": "2025-10-09T08:49:00Z",
      "windows": [
        {
          "name": "5h",
          "percent": 41,
          "resets_at": "2025-10-09T11:00:00Z",
          "minutes": 300
        },
        {
          "name": "7d",
          "percent": 78.5,
          "resets_at": "2025-10-11T15:00:00Z",
          "minutes": 10080
        }
      ],
      "sessions": 369,
      "tokens": {
        "input": 552000000,
        "output": 51100000,
        "cache_read": 16800000000,
        "cache_write": 0
      },
      "last_active_at": "2025-10-09T08:41:07Z",
      "projects": [
        {
          "path": "BAQEBAQEBAQEBAQEimfg7XVTY5QtTS7RChUlsXeXWFuDHpTBGypCDNK507A",
          "sessions": 212,
          "tokens": {
            "input": 301000000,
            "output": 28000000,
            "cache_read": 9600000000,
            "cache_write": 0
          }
        }
      ]
    }
  ],
  "sources": [
    {
      "provider": "claude",
      "status": "skipped"
    },
    {
      "provider": "codex",
      "status": "ok"
    }
  ]
}
```

The body as sent, 1066 bytes:

```
{"v":1,"team":"kzdvvj2umnduyauf35o36k6kw462mujv","device":"d-000102030405060708090a0b","device_label":"AQEBAQEBAQEBAQEBP5kbN5fasitxl9v4Xe9ioihsTqSGEvQ","os_user":"AgICAgICAgICAgICd91O1uRndQ00ItS5z7aTyprSFA","collector_version":"v1.4.2","collected_at":"2025-10-09T08:50:00Z","last_success_at":"2025-10-09T08:50:00Z","accounts":[{"provider":"codex","label":"AwMDAwMDAwMDAwMDSWbUxyIYwLxNem8GfO1ERleExJJSLs74N5wYzIE81w","current":true,"plan":"plus","quota_at":"2025-10-09T08:49:00Z","windows":[{"name":"5h","percent":41,"resets_at":"2025-10-09T11:00:00Z","minutes":300},{"name":"7d","percent":78.5,"resets_at":"2025-10-11T15:00:00Z","minutes":10080}],"sessions":369,"tokens":{"input":552000000,"output":51100000,"cache_read":16800000000,"cache_write":0},"last_active_at":"2025-10-09T08:41:07Z","projects":[{"path":"BAQEBAQEBAQEBAQEimfg7XVTY5QtTS7RChUlsXeXWFuDHpTBGypCDNK507A","sessions":212,"tokens":{"input":301000000,"output":28000000,"cache_read":9600000000,"cache_write":0}}]}],"sources":[{"provider":"claude","status":"skipped"},{"provider":"codex","status":"ok"}]}
```

Its `X-Aiu-Signature`:

```
JeCd6_i3EjJs6R0_pbvL3jAGaq7qcl7dKZYI1z73bgkCW_7lQcSRoYLzep6UH5IGFiKFmZUx3Ceh-Z3L5-95Bg
```

The sealed strings open to `sam-air`, `sam`, `sam@example.com`, and `~/src/garden/app`.

## 8. Storage

### 8.1 Records

A relay keeps at most one record per team and device. A record holds:

- the body, byte for byte as it was sent;
- its signature;
- *since*, when the relay first stored this device. It is kept when the record is replaced.

### 8.2 The latest snapshot per device

When a valid `PUT` arrives:

- With no live record for the device, the relay stores a new one with *since* set to now. Before that it checks the device cap and the new-team and new-device limits (§9).
- With a live record whose body equals the new body byte for byte, it answers `200` and writes nothing.
- With a live record whose `collected_at` is later than the new `collected_at`, compared as instants, it answers `409` and keeps the record. So `collected_at` never goes backwards for a device while its record lives.
- Otherwise, with the new `collected_at` equal or later, it replaces the record and keeps its *since*.

Once a record expires or is deleted, the next valid snapshot for the device is accepted whatever its time, and counts as a new device.

The reference relay counts the team's devices and then writes, which is not atomic. First writes from new devices racing each other can pass the cap by a few. Its team read still lists at most the cap, the devices with the earliest *since*, so that the read fits in one response. A device past them is turned away with `403` and its record deleted: at once after its first write when it can tell, else at its next write, which checks whenever the team is over the cap. When a first write cannot tell, because the store failed, the relay deletes the record and answers `503`.

### 8.3 Lifetime

The reference relay keeps a record, after each write, for as long as its device has been writing, but at least 7 days and at most 90:

```
ttl = min(max(now − since, 7 days), 90 days)
```

A record stored before the relay kept *since* keeps the full 90 days. A `PUT` of an identical body writes nothing and does not extend it. A key made only to fill the store leaves its snapshots for a week, and a device that reported for a month and then went quiet is kept for a month.

A relay SHOULD keep records at least 7 days after their last write. Readers MUST NOT count on any record being there.

## 9. Rate limits

A relay SHOULD limit requests, and SHOULD answer `429` with `Retry-After` in seconds. The reference limits, from `DefaultLimits` in `relay/server.go`:

| Limit | Value | Counted |
| --- | --- | --- |
| devices per team | 100 | live records, checked on a new device's first write, and on every write while the team is over the cap |
| requests per IP address | 2000 an hour | every request the relay routes, before any other check; IPv6 per /64 |
| writes per team | 1000 an hour | `PUT`s that pass the signature and document checks |
| new teams per IP address | 5 a day | first writes to a team with no live record; IPv6 per /48 |
| new devices per IP address | 100 a day | first writes of a device, in any team; IPv6 per /48 |

Windows are fixed and aligned to Unix time: an hour starts on the hour, and a day at 00:00 UTC. The reference collector writes 4 times an hour on schedule and reads the team once an hour, so a team of 100 behind one address makes 500 requests an hour. The device cap follows from the team read: at 44 KB per device, 100 fill most of the 4.5 MB a Vercel Function may return. [relay.md](relay.md#limits) explains the reasoning for operators.

## 10. Readers

For each entry of a team read, a reader MUST:

1. decode `body` and `sig` from base64url;
2. verify `sig` over `ai-usage snapshot v1\n` and the body with the team's own public key;
3. decode the body strictly and check the rules of §7.2 to §7.4 (the reference reader skips only the check that `collected_at` is not in the future);
4. check that the document's `team` is its own fingerprint and its `device` is the entry's `device`.

A reader MUST drop an entry that fails any of these. When the relay lists a device more than once, a reader MUST keep only the entry with the latest `collected_at`, so that a device's tokens are not counted twice. The reference reader counts dropped entries and reports them. It also uses its own fresh snapshot in place of the relay's copy of its own device.

From a single read a reader cannot tell that the relay withheld a snapshot, deleted one, or served an older one.

## 11. Compatibility

The document version `v`, the `/v1` paths, and the signed-message prefixes are all version 1. A relay answers `422` to any other `v`.

A relay rejects a member it does not know with `422`, and the reference reader drops a document it cannot decode strictly. New members are therefore optional and omitted when empty, and they reach a team in this order:

1. The relay is updated to accept the new member.
2. Collectors start sending it.

A collector that sends a member its relay does not know gets `422`. The reference collector then keeps the snapshot pending and does not read the team either, so the device sees only itself until the relay is updated. A reader older than the member drops documents that carry it, so those devices drop out of its team view until it is updated too. A document without the member stays valid everywhere.

Optional members added to version 1 so far:

| Member | In | Meaning |
| --- | --- | --- |
| `quota_from` | Account | whose account the windows belong to |
| `linked` | Account | what accounts of other providers spent through this one |

## 12. Security notes

- The relay's operator sees the team fingerprint, device ids, the public key, every plain field, and the IP addresses that connect. The reference relay keeps IP addresses in rate-limit counters for up to a day; in memory, as `ai-usage relay serve` runs without KV, an expired counter is dropped within an hour after, at the next request. A function instance also remembers, in memory, the addresses it saw go over a limit, each until a minute after its window ends, at the next request.
- A captured `PUT` can be sent again. While the record lives, an identical body changes nothing and an older one gets `409`. After the record expires or is deleted, a replayed old snapshot is accepted until the device's next write.
- A captured `GET` or `DELETE` stays valid for 300 seconds either side of its time, for the same team and device. A replayed `DELETE` removes the device until its next write.
- The additional data of a sealed string binds it to the team, not to a field. Any member of the team can move a sealed string between fields; the relay cannot, since documents are signed.
- Nonces are 96 random bits, taken fresh for every sealed string.

## 13. Test vectors

These vectors come from a fixed, published seed. The key is made up; never use it for a real team. Every value was produced by the reference implementation and checked with the Python `cryptography` package.

| Item | Value |
| --- | --- |
| seed (hex) | `000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f` |
| exported key | `aiu-team-1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8` |
| public key (hex) | `03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8` |
| public key (base64url), `X-Aiu-Key` | `A6EHv_POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg` |
| SHA-256 of the public key (hex) | `56475aa75463474c0285df5dbf2bcab73da651358839e9b77481b2eab107708c` |
| fingerprint | `kzdvvj2umnduyauf35o36k6kw462mujv` |
| seal key (hex) | `4d5309a98ca65d2a801b1025424ec2b1e7aacd870a061e1b82e49653e2eae8e3` |

Sealing `sam` with the nonce `000102030405060708090a0b`:

| Item | Value |
| --- | --- |
| nonce, ciphertext, tag (hex) | `000102030405060708090a0b` `6095a9` `041f2830abdf7c875333db498f1ef5e9` |
| sealed string | `AAECAwQFBgcICQoLYJWpBB8oMKvffIdTM9tJjx716Q` |

Opening that string with the same key gives `sam`. Opening it with other additional data fails.

A team read at `X-Aiu-Time: 1760000000`. The message, with `\n` for 0x0A:

```
ai-usage request v1\nGET\nkzdvvj2umnduyauf35o36k6kw462mujv\n\n1760000000
```

In hex:

```
61692d757361676520726571756573742076310a4745540a6b7a6476766a32756d6e64757961756633356f33366b366b773436326d756a760a0a31373630303030303030
```

Its `X-Aiu-Signature`:

```
-wmRgGEME2TU3wuXzjaoYRX-KSjcB5f1kShasgxwWmYrb1f4zFRE1SLcXaPtHQb32D_uAtEVKpyR9tbEmyOsBA
```

A delete of the device `d-000102030405060708090a0b` at the same time signs:

```
ai-usage request v1\nDELETE\nkzdvvj2umnduyauf35o36k6kw462mujv\nd-000102030405060708090a0b\n1760000000
```

with the signature:

```
HfmtjWSA5WBJTx3wSB0uYgTpxOBASmFC-gYwqO1mo51nmZf2jCgz7DMlbkbRdtT4vqu11NRsfpG29WCqMuPRCQ
```

The snapshot in §7.5 and its signature complete the set. A relay whose clock reads 1760000000 accepts that snapshot with `200`, and then answers the team read above with it.
