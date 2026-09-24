# Running a relay

The relay is the one shared piece of a team's setup. It is a small HTTP API that keeps the latest snapshot of each device, and it is the same code whether it runs as a Vercel function or as `ai-usage relay serve`.

It stores only what it can check: a snapshot of a fixed shape, at most 64 KB, signed by the key whose fingerprint names the team. Names, paths, and errors inside it are sealed with the team key, so the relay cannot read them. See [Privacy](../README.md#privacy) for what it can see.

Collectors find the relay by its base URL, without `/v1`; see [Connect collectors to your relay](#connect-collectors-to-your-relay). The protocol itself, enough to write a relay or a client in another language, is specified with test vectors in [relay-protocol.md](relay-protocol.md).

## On Vercel, with Upstash for Redis (formerly Vercel KV)

[![Deploy with Vercel](https://vercel.com/button)](https://vercel.com/new/clone?repository-url=https%3A%2F%2Fgithub.com%2Fneoromantic%2Fai-usage&project-name=ai-usage-relay&repository-name=ai-usage-relay&stores=%5B%7B%22type%22%3A%22integration%22%2C%22integrationSlug%22%3A%22upstash%22%2C%22productSlug%22%3A%22upstash-kv%22%2C%22protocol%22%3A%22storage%22%7D%5D)

The button copies this repository into your Git account as `ai-usage-relay`, creates a Vercel project of the same name, adds an Upstash for Redis database to it, and deploys. When it is done, check the relay as in step 4 below and [connect the collectors](#connect-collectors-to-your-relay). The copy does not follow this repository: to update the relay, pull this repository's changes into it and push, and Vercel deploys them.

### From a clone, with the Vercel CLI

With the [Vercel CLI](https://vercel.com/docs/cli) installed and logged in:

```sh
git clone https://github.com/neoromantic/ai-usage
cd ai-usage
sh scripts/deploy-relay.sh
```

The script links the clone to a Vercel project named `ai-usage-relay`, creating it if needed; adds an Upstash for Redis database unless the project already has `KV_REST_API_URL`; deploys to production; checks `/v1/health`; and prints the commands that point collectors at the relay. `AI_USAGE_RELAY_PROJECT` names another project. The project goes under your own Vercel account, not whichever team the CLI last used. An account whose Hobby projects Vercel keeps in a team cannot be named as a scope, so for one the project goes under the team the CLI uses now. `VERCEL_SCOPE` names a team instead. The first Upstash install on a Vercel team may ask you to accept Upstash's terms, so run the script in a terminal. It deploys the files in the clone as they are; to update the relay later, `git pull` and run it again.

### By hand

The repository deploys as it is. `vercel.json` selects the *Other* framework preset and sends `/v1/*` to the Go function in `api/relay.go`, which Vercel builds with the Go version from `go.mod`. There is no build command. `.vercelignore` uploads only `api/`, `relay/`, `internal/`, `go.mod`, `go.sum`, and `vercel.json`.

1. Import the repository into a new Vercel project, from your fork on GitHub or with `vercel link` in a clean clone.
2. Add a Redis store from the Vercel Marketplace: Upstash for Redis, the product that replaced Vercel KV. In the project's Storage tab, create it and connect it to the project; Vercel bills it with the project, so there is no other account to open and no client to install. Connecting it sets `KV_REST_API_URL` and `KV_REST_API_TOKEN`, which the relay reads; it speaks the store's REST API directly.
3. Deploy to production. Variables apply to new deployments, so redeploy after adding them.
4. Check it:

   ```sh
   curl https://your-project.vercel.app/v1/health
   {"ok":true,"snapshot_version":1}
   ```

   A `503` with `{"error":"relay store not configured"}` means the function did not find those two variables. It refuses to run without a store, because Vercel starts and stops function instances as it likes, and a store in memory would lose snapshots at random. `vercel dev` runs one long-lived process, so there the function falls back to memory.
5. [Connect the collectors](#connect-collectors-to-your-relay) to `https://your-project.vercel.app`.

Use the production domain or a custom domain. Vercel's Deployment Protection can put preview and per-deployment URLs behind a Vercel login, which collectors cannot pass.

With the *Other* preset, Vercel also serves the repository's other files as static files. They are public in the repository anyway. Deploy from Git or from a clean clone, so that nothing else in a working copy, such as a `.env` file, is uploaded with them.

What the relay keeps in KV:

| Key | Contents |
| --- | --- |
| `aiu:t:{team}:d:{device}` | one device's latest snapshot, its signature, and when the relay first stored the device; expires as described under [Limits](#limits) |
| `aiu:t:{team}:devices` | the team's device ids; lives as long as the longest-lived record, and ids whose records are gone are dropped when the team is read |
| `aiu:rl:{counter}` | rate-limit counters, which expire with their window: an hour, or a day for new teams and devices; the per-IP counters have the IP address in the name |

### Traffic and free plans

A device runs 96 times a day. Each run writes its snapshot; a run you start also reads the team, and a scheduled run reads it once an hour. The write costs 9 store commands and the read 4, so a device on its schedule costs about 29,000 commands a month.

Every read returns the whole team, so traffic grows with the square of the team size. At about 15 KB a snapshot with its daily counts, a team of N devices reads about N² × 15 MB a month, once out of the store and once more out of the function. Upstash's free plan allows 500,000 commands a month, enough for about 17 devices, and 10 GB of bandwidth, enough for a team of about 25. Vercel's Hobby plan includes 10 GB a month of Fast Origin Transfer, which function responses count against. A larger team needs a paid plan, or a relay of your own with `ai-usage relay serve`. Bots that one collector reads are part of its device's snapshot, so a server running many bots costs as much as one device; a bot with a collector of its own in its container is a device of its own.

A request to a path the relay does not serve costs no KV command. Once a function instance has seen a client go over a limit, it answers that client's further requests with `429` without a KV command until the window ends. Every request still costs a function invocation, and a signed team read returns up to about 4.4 MB. On a public deployment, also add a rate-limit rule for `/v1/` in the project's Vercel Firewall, so a flood is dropped before it reaches the function.

## With `ai-usage relay serve`

Any machine that can run ai-usage can run the relay:

```sh
ai-usage relay serve --addr :8080
```

It uses the same store when the same variables are set, and memory otherwise. It prints the store it chose. A memory relay loses its snapshots when it stops. Each collector run publishes its device's whole snapshot, so the team is complete again within 15 minutes of a restart, except for devices that are switched off.

Put it behind a reverse proxy that terminates TLS, such as Caddy or nginx. The relay limits requests by the client's address. By default that is the connection's own address, which behind a proxy is the proxy's, so every client would share one limit. Tell the relay which header your proxy sets to the client's address:

```sh
ai-usage relay serve --addr 127.0.0.1:8080 --client-ip-header X-Real-Ip
```

The relay reads the last address in that header, and only when the connection comes from a loopback or private address, that is from a proxy on the same host or network. A client that reaches the relay directly still cannot choose its rate-limit key.

The proxy must set the header itself, replacing any value the client sent. Otherwise a client picks a new key for each request and gets around the per-IP and new-team limits. Caddy, for one, passes a client's own `X-Real-Ip` through unless told to set it. With Caddy:

```caddyfile
relay.example.com {
    reverse_proxy 127.0.0.1:8080 {
        header_up X-Real-IP {remote_host}
    }
}
```

With nginx:

```nginx
location /v1/ {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header X-Real-IP $remote_addr;
}
```

`--client-ip-header X-Forwarded-For` also works, because the relay takes the last entry, the one the nearest proxy added. Use it only with a proxy that adds one, such as nginx with `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for`. A proxy that passes the header through unchanged leaves the last entry to the client.

Collectors accept an `http://` relay URL, which is fine on a trusted network. Snapshots are sealed and signed either way, but plain HTTP shows the team fingerprint and the plain fields to anyone on the path.

Ctrl-C stops the relay gracefully.

### In Docker

A host with Docker and no Go can build and run the newest release in the official Go image:

```sh
docker run -d --name ai-usage-relay --restart unless-stopped -p 127.0.0.1:8080:8080 golang:1.25 go run github.com/neoromantic/ai-usage/cmd/ai-usage@latest relay serve --addr :8080 --client-ip-header X-Real-Ip
```

The first start downloads and builds the release, which takes a minute or two; `docker logs ai-usage-relay` shows when it listens. `@latest` is looked up again on every start, so `docker restart ai-usage-relay` moves the relay to the newest release. The image must have at least the Go version in `go.mod`, which is 1.25.

The port is published on 127.0.0.1 only, for a reverse proxy on the same host set up as above. The proxy's connections reach the container from Docker's private network, so the relay reads the proxy's header.

Snapshots stay in the container's memory, and a restart drops them. To keep them in Upstash for Redis instead, set `KV_REST_API_URL` and `KV_REST_API_TOKEN` in the shell and add `-e KV_REST_API_URL -e KV_REST_API_TOKEN` before `golang:1.25`, which passes them in without writing the token on the command line.

## Connect collectors to your relay

A collector takes the relay's base URL, without `/v1`: `https://your-project.vercel.app`, or `https://relay.example.com` behind a proxy. On a machine that already runs ai-usage:

```sh
ai-usage relay set https://relay.example.com
ai-usage relay show
```

To install a new collector with the relay already set, give the installer `AI_USAGE_RELAY`:

```sh
curl -fsSL https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh | AI_USAGE_RELAY=https://relay.example.com sh
```

In PowerShell on Windows:

```powershell
$env:AI_USAGE_RELAY = 'https://relay.example.com'; irm https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.ps1 | iex
```

Every machine in a team must use the same relay, since each relay keeps only the snapshots written to it. `AI_USAGE_RELAY` set for a command also overrides the saved relay for that run only. `ai-usage relay clear` goes back to the relay built into the release, if there is one. To build your relay into your own releases, so that new collectors need no setting, see [Default relay](releasing.md#default-relay).

Keep the relay at least as new as the collectors. A relay rejects a snapshot with fields it does not know, as described under [API](#api), and release builds install a release at their next scheduled run, within about 15 minutes of its publication, and run it from the run after. That leaves no time to catch up afterwards, so deploy the relay's update before the release is published; the relay code a release needs is on `main` before the release is tagged. A collector that updates first gets HTTP 422, keeps its snapshot pending, and sees only itself until the relay catches up. On Vercel, pull the upstream repository into your copy and push, or rerun `scripts/deploy-relay.sh` from an updated clone.

A relay run with `ai-usage relay serve` has the code it was started with. One started from an installed release keeps it after that binary updates itself, until it is restarted. One built from `@latest`, as [in Docker](#in-docker), gets a release's code at a restart once `@latest` resolves to its tag, which can take longer than the collectors take to install it. To run it ahead of them, either build it from `main` (`go run github.com/neoromantic/ai-usage/cmd/ai-usage@main relay serve …` in place of `@latest`) and restart it once the change is on `main`, before the tag; or restart it with the exact tag, `@vX.Y.Z`, as soon as the tag is pushed, which resolves before the release workflow publishes the release.

## Limits

These are fixed in the code, in `DefaultLimits` in `relay/server.go`.

| Limit | Value |
| --- | --- |
| devices per team | 50 |
| writes per team | 1000 an hour |
| requests per IP address | 2000 an hour; IPv6 addresses count per /64 |
| new teams per IP address | 5 a day; IPv6 addresses count per /48 |
| new devices per IP address | 100 a day, in any teams; IPv6 addresses count per /48 |
| snapshot size | 64 KB |
| snapshot lifetime | after its last write, as long as the device has been writing: at least 7 days, at most 90 |
| clock difference for signed reads and deletes | 5 minutes |

A device is one collector: one machine, one user account on it, or one container. A team has at most 50 devices because every run reads the whole team in one response, up to about 88 KB a device, and a Vercel Function may return at most 4.5 MB.

A device writes 4 times an hour and reads the team once an hour when only the scheduler runs it, so a team of 50 behind one address makes about 250 requests an hour. Day windows follow UTC days.

The new-device and new-team limits bound what one address can add to the store: two full teams' worth of snapshots a day, about 8.8 MB as stored (6.4 MB of snapshots and the rest base64 and JSON). What an address writes once and drops expires within a week, so a script that only makes keys keeps about 62 MB. The limits do not bound one that also writes its devices again: it keeps them, and adds about 8.8 MB a day for as long as it runs. The store's own size limit is the backstop, so watch the store's size on a public relay. A device is new when the relay holds no snapshot for it, so its first write, and the first after its snapshot expired, count. Writes from devices the relay already holds do not. Rolling out more than 100 devices from one address in a day, such as an office behind one NAT, leaves the rest waiting: their writes answer `429` until the next UTC day, and each collector keeps its newest snapshot to send then.

The lifetime rule means a key made only to fill the store leaves its snapshots for a week, not 90 days. A device that has reported for a month and then goes quiet is kept for a month.

## API

All paths are under `/v1`. Every response has `Cache-Control: no-store`. Errors are JSON, `{"error": "…"}`. This is a summary; [relay-protocol.md](relay-protocol.md) specifies every field and rule, with test vectors.

| Request | Signed over | Success |
| --- | --- | --- |
| `GET /v1/health` | nothing | `{"ok":true,"snapshot_version":1}` |
| `PUT /v1/teams/{team}/devices/{device}` | `ai-usage snapshot v1\n` followed by the body | `{"stored":true}` |
| `GET /v1/teams/{team}` | `ai-usage request v1\nGET\n{team}\n\n{time}` | `{"devices":[{"device","body","sig"}]}` |
| `DELETE /v1/teams/{team}/devices/{device}` | `ai-usage request v1\nDELETE\n{team}\n{device}\n{time}` | `{"deleted":true}` |

Headers, with binary values in unpadded base64url:

- `X-Aiu-Key`: the team's Ed25519 public key. Its fingerprint must be `{team}`: the first 20 bytes of its SHA-256, in lowercase unpadded base32.
- `X-Aiu-Signature`: the Ed25519 signature of the message in the table.
- `X-Aiu-Time`: for reads and deletes, the Unix time in seconds that the signature covers.

A `PUT` body must be a valid snapshot in exactly the form `encoding/json` writes it, naming the same team and device as the path. The team read returns each stored body and signature as they were written, so every reader verifies them again.

An account in a snapshot may carry `quota_from`, a provider name in plain text. It says that the account's quota windows are the reading of another provider's account on the same device, which this account is assumed to bill through: Hermes on a Codex or SuperGrok subscription shows the quota of the Codex or Grok account it is taken to use. It must name a known provider other than the account's own, and it comes only with windows. A snapshot without it is valid as before.

An account may also carry `linked`, at most 4 entries of `{provider, label, sessions, tokens}`: what an account of another provider on the same device spent through this one, such as Hermes on this Codex login. `label` is sealed like every label, `provider` must be another known provider, and the counts have the same limits as the account's own. Readers add these up for the team's "also used by" lines. A snapshot without it is valid as before.

An account may also carry `days` and `recent`, plain counts the report's periods and device matrix are built from. `days` is at most 90 token counts, the account's input plus output tokens on the device per UTC day, newest first, starting with the UTC day of `collected_at`, with trailing zeros left out. `recent` is at most 8 entries of `{window, start, tokens}`: the account's input plus output tokens since each window of its reading began, for the windows whose start is known and which had not reset. `window` is a window name in plain text and `start` a time.

A snapshot may carry `aliases`, at most 48 entries of `{provider, label, name, at}`: the short names `ai-usage alias` gave accounts on that device. `label` and `name` are sealed, `name` is left out when the entry clears a name, and `at` is when it was set. The newest entry for an account, from any device in the team, names it everywhere.

A relay older than `quota_from`, `linked`, `days`, `recent`, or `aliases` answers `422` to a snapshot that carries one, and a relay older than the 64 KB limit answers `413` to a snapshot over 32 KB. The collector then keeps that snapshot pending and does not read the team either, so the device sees only itself until the relay is updated. An older collector reading the team counts such a snapshot as unreadable and leaves that device out. Deploy the relay first, then update the collectors.

| Status | Meaning |
| --- | --- |
| `400` | the `PUT` body could not be read within 30 seconds |
| `401` | missing or bad signature, or a request time too far from the server's |
| `403` | the key is missing or does not match the team, or the team already has 50 devices without this one |
| `404` | not a valid team or device id |
| `409` | a newer snapshot for this device is already stored |
| `413` | the body is larger than 64 KB |
| `422` | the body is not a valid snapshot |
| `429` | a rate limit; `Retry-After` says in seconds when the window ends |
| `503` | the store is unavailable or not configured |
