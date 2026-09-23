# Running a relay

The relay is the one shared piece of a team's setup. It is a small HTTP API that keeps the latest snapshot of each device, and it is the same code whether it runs as a Vercel function or as `ai-usage relay serve`.

It stores only what it can check: a snapshot of a fixed shape, at most 32 KB, signed by the key whose fingerprint names the team. Names, paths, and errors inside it are sealed with the team key, so the relay cannot read them. See [Privacy](../README.md#privacy) for what it can see.

Collectors find the relay by its base URL, without `/v1`:

```sh
ai-usage relay set https://relay.example.com
```

## On Vercel, with Upstash for Redis (formerly Vercel KV)

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
5. Point the collectors at it with `ai-usage relay set https://your-project.vercel.app`, with `AI_USAGE_RELAY` when installing, or by building your releases with it as the default (see [releasing.md](releasing.md)).

Use the production domain or a custom domain. Vercel's Deployment Protection can put preview and per-deployment URLs behind a Vercel login, which collectors cannot pass.

With the *Other* preset, Vercel also serves the repository's other files as static files. They are public in the repository anyway. Deploy from Git or from a clean clone, so that nothing else in a working copy, such as a `.env` file, is uploaded with them.

Each collector run makes two relay requests, a write and a read, and each costs a few KV commands. A device runs 96 times a day; check that against the store plan's command allowance.

A request to a path the relay does not serve costs no KV command. Once a function instance has seen a client go over a limit, it answers that client's further requests with `429` without a KV command until the window ends. Every request still costs a function invocation, and a signed team read returns up to about 1.4 MB. On a public deployment, also add a rate-limit rule for `/v1/` in the project's Vercel Firewall, so a flood is dropped before it reaches the function.

What the relay keeps in KV:

| Key | Contents |
| --- | --- |
| `aiu:t:{team}:d:{device}` | one device's latest snapshot, its signature, and when the relay first stored the device; expires as described under [Limits](#limits) |
| `aiu:t:{team}:devices` | the team's device ids |
| `aiu:rl:{counter}` | rate-limit counters, which expire with their window: an hour, or a day for new teams and devices; the per-IP counters have the IP address in the name |

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

## Limits

These are fixed in the code, in `DefaultLimits` in `relay/server.go`.

| Limit | Value |
| --- | --- |
| devices per team | 32 |
| writes per team | 400 an hour |
| requests per IP address | 600 an hour; IPv6 addresses count per /64 |
| new teams per IP address | 5 a day; IPv6 addresses count per /48 |
| new devices per IP address | 32 a day, in any teams; IPv6 addresses count per /48 |
| snapshot size | 32 KB |
| snapshot lifetime | after its last write, as long as the device has been writing: at least 7 days, at most 90 |
| clock difference for signed reads and deletes | 5 minutes |

A device writes 4 times an hour when only the scheduler runs it. Day windows follow UTC days.

The new-device and new-team limits bound what one address can add to the store: one full team's worth of snapshots a day, about 1 MB. A device is new when the relay holds no snapshot for it, so its first write, and the first after its snapshot expired, count. Writes from devices the relay already holds do not. Rolling out more than 32 devices from one address in a day, such as an office behind one NAT, leaves the rest waiting: their writes answer `429` until the next UTC day, and each collector keeps its newest snapshot to send then.

The lifetime rule means a key made only to fill the store leaves its snapshots for a week, not 90 days. A device that has reported for a month and then goes quiet is kept for a month.

## API

All paths are under `/v1`. Every response has `Cache-Control: no-store`. Errors are JSON, `{"error": "…"}`.

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

An account in a snapshot may carry `quota_from`, a provider name in plain text. It says that the account's quota windows are the reading of another provider's account on the same device, which this account is assumed to bill through: Hermes on a Codex or SuperGrok subscription shows the quota of the account logged in to `~/.codex` or `~/.grok`. It must name a known provider other than the account's own, and it comes only with windows. A snapshot without it is valid as before.

A relay older than `quota_from` answers `422` to a snapshot that carries it. The collector then keeps that snapshot pending and does not read the team either, so the device sees only itself until the relay is updated. An older collector reading the team counts such a snapshot as unreadable and leaves that device out. Deploy the relay first, then update the collectors.

| Status | Meaning |
| --- | --- |
| `400` | the `PUT` body could not be read within 30 seconds |
| `401` | missing or bad signature, or a request time too far from the server's |
| `403` | the key is missing or does not match the team, or the team already has 32 devices |
| `404` | not a valid team or device id |
| `409` | a newer snapshot for this device is already stored |
| `413` | the body is larger than 32 KB |
| `422` | the body is not a valid snapshot |
| `429` | a rate limit; `Retry-After` says in seconds when the window ends |
| `503` | the store is unavailable or not configured |
