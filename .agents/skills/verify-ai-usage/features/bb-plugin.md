# BB plugin

## Sub-features

- The AI Usage page: attention, subscriptions with their limiting windows and forecasts, cost and its questions, devices × subscriptions by period, devices with their notes and the Account Pooler mark, Invite and Join
- The page's states: looking, installing (with its log), install failed (Retry), not collected yet (Finish setup), error, newer schema, offline, not supported, and the example team at `/plugins/ai-usage/usage/example`
- The install of ai-usage when the plugin starts on a machine without it, once per machine
- Settings → AI Usage: this machine's ai-usage, Update, Pause or Resume collecting, Install menu bar app, Remove ai-usage, and the other BB machines with Install
- The sidebar footer's Team usage, and the palette's AI Usage: Open and AI Usage: Refresh

## How to get to it (user POV)

Install AI Usage in BB; its icon in the navigation rail opens the page.

## Driving it

`scripts/verify.sh plugin` runs `bun run check` in `bb/`: the types, the
tests (`bb/test/`, which read every `docs/demo/*.json`, an older report,
and an unknown kind of attention, and drive the host entry on a made-up
machine with fake binaries and a fake install.sh), and `bb plugin build`.

By hand, in a BB of your own: `cd bb && bun run build`, then
`bb plugin install "$PWD" --yes`, and open the page; the example team
needs no ai-usage. On a machine that already runs ai-usage the plugin
installs nothing. `bb plugin remove ai-usage` takes it out again; ai-usage
keeps running.

## Proof

The tests pass, and the page, the footer, and Settings read right in a
real BB, on the example team and on a machine's own report. The fresh
install runs install.sh of the latest release; prove it only in a home or
a machine of its own, never on one whose ai-usage you use.
