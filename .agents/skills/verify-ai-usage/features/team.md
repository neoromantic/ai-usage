# Teams and the relay

## Sub-features

- `team key` prints the team's private key; `team join` reads one from stdin
- Each collect publishes a signed, partly sealed snapshot: `PUT /v1/teams/{team}/devices/{device}`
- Each report pulls the team: `GET /v1/teams/{team}`; `team forget-device ID` deletes one
- `name set` and `AI_USAGE_NAME`, the device's name, which the report shows after its OS user, as `ann@mira-mbp`; `alias`, an account's short name
- `relay show|set|clear`, `AI_USAGE_RELAY`; `relay serve`, a relay of one's own
- The relay as a Vercel function in `api/`, with Upstash, limits, and caps; `GET /v1/prices`, the price list, even without a store
- The snapshot's `costs`, what people typed with `ai-usage cost`, sealed, sent once every device runs v0.10.0

## How to get to it (user POV)

A colleague installs with `AI_USAGE_TEAM_KEY=…`, or runs `ai-usage team
join`. The page then shows DEVICES × SUBSCRIPTIONS and the header's
`team …` and `relay …`.

## Driving it

`scripts/verify.sh stack`: a memory relay on 127.0.0.1 and two devices,
`dev-a` and `dev-b`, in homes under `.verify/latest/stack/`. By hand, the
same commands, as [SKILL.md](../SKILL.md#driving-it-by-hand) shows.

## Proof

`relay/` tests (protocol, limits, the KV store) and
`cmd/ai-usage/team_test.go` (`TestTwoDevicesShareATeam`) pass, and
`stack/a.json` lists both devices, `stack/a.txt` shows `dev-b`. The relay
must never see a name, an email, or a path: `relay.log` and the snapshot
(`go run ./scripts/demo snapshot`) show only sealed values for them.

## Try

- `dev b name set srv1`, collect, then `dev-a`'s report: is the new name there and the old gone?
- `team forget-device` on `dev-b`, then `dev b collect`: does it come back, as it should?
- Stop the relay between collects: does the header say the relay is behind, and does collect still exit 0?

## Gotchas

A relay change the release needs is deployed before the release; see
`docs/relay.md`. The deployed relay is real: never point a test at it.
